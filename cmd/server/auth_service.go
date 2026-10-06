package main

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// authService is the optional user-management layer. Server.auth is nil
// unless userManagement.enabled, and nothing in this file runs then.
type authService struct {
	st   *users.Store
	mail mailer.Mailer
	set  *userMgmtSettings
	ipr  *wsLimiter // only its clientIP rule is used

	login       *rateLimiter
	signup      *rateLimiter // register, forgot, self-service resend
	hook        *rateLimiter
	settingsPut *rateLimiter // PUT /api/account/settings, per user

	warnIndistinct sync.Once
	stop           chan struct{}
	stopOnce       sync.Once
	wg             sync.WaitGroup // the janitor
}

func newAuthService(set *userMgmtSettings, st *users.Store, m mailer.Mailer) *authService {
	return &authService{
		st: st, mail: m, set: set,
		ipr:         &wsLimiter{trustedProxies: set.trustedProxies},
		login:       newRateLimiter(10, 15*time.Minute),
		signup:      newRateLimiter(5, time.Hour),
		hook:        newRateLimiter(600, time.Minute),
		settingsPut: newRateLimiter(60, time.Hour),
		stop:        make(chan struct{}),
	}
}

// initUserManagement builds s.auth when the feature is on. Call it after
// NewServer and before RegisterRoutes. measurementDBPath is passed to
// users.Open as a forbidden path, so users.db can never be the analyzer DB.
func (s *Server) initUserManagement(measurementDBPath string) error {
	if !s.cfg.UserManagementEnabled() {
		return nil
	}
	set, err := resolveUserManagement(s.cfg.UserManagement, measurementDBPath, os.Getenv)
	if err != nil {
		return err
	}
	st, err := users.Open(set.dbPath, measurementDBPath)
	if err != nil {
		return err
	}
	var m mailer.Mailer
	if set.provider == "fake" {
		m = &mailer.Fake{}
	} else {
		m = mailer.NewBrevo(set.brevoAPIKey, set.fromEmail, set.fromName)
	}
	s.auth = newAuthService(set, st, m)
	s.auth.logStartup()
	s.auth.wg.Add(1)
	go func() {
		defer s.auth.wg.Done()
		s.auth.janitor(time.Hour)
	}()
	return nil
}

// closeUserManagement stops the janitor and closes users.db. Safe when off.
func (s *Server) closeUserManagement() {
	if s.auth == nil {
		return
	}
	s.auth.stopOnce.Do(func() {
		close(s.auth.stop)
		s.auth.wg.Wait()
		if err := s.auth.st.Close(); err != nil {
			log.Printf("[users] close: %v", err)
		}
	})
}

func (a *authService) logStartup() {
	log.Printf("[users] user management enabled: db=%s, %d config admin(s), webhook=%v",
		a.set.dbPath, len(a.set.adminEmails), a.set.webhookSecret != "")
	admins, err := a.st.List(users.ListFilter{Role: users.RoleAdmin})
	if err != nil {
		log.Printf("[users] list admins: %v", err)
		return
	}
	for _, u := range admins {
		if !a.isConfigAdmin(u.Email) {
			log.Printf("[users] note: admin #%d is not in adminEmails (promoted in the UI, or removed from config)", u.ID)
		}
	}
}

func (a *authService) janitor(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		a.prune()
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
	}
}

func (a *authService) prune() {
	if _, err := a.st.PruneStalePending(48 * time.Hour); err != nil {
		log.Printf("[users] prune pending: %v", err)
	}
	if _, err := a.st.PruneExpiredSessions(); err != nil {
		log.Printf("[users] prune sessions: %v", err)
	}
	if _, err := a.st.PruneTokens(7 * 24 * time.Hour); err != nil {
		log.Printf("[users] prune tokens: %v", err)
	}
	if _, err := a.st.PruneMail(90 * 24 * time.Hour); err != nil {
		log.Printf("[users] prune mail log: %v", err)
	}
	a.login.gc()
	a.signup.gc()
	a.hook.gc()
	a.settingsPut.gc()
}

func (a *authService) isConfigAdmin(email string) bool { return a.set.adminEmails[email] }

// roleFor is the role an address gets on activation.
func (a *authService) roleFor(email string) users.Role {
	if a.isConfigAdmin(email) {
		return users.RoleAdmin
	}
	return users.RoleUser
}

func (a *authService) audit(actor *int64, action string, target *int64, detail map[string]string) {
	if err := a.st.Audit(actor, action, target, detail); err != nil {
		log.Printf("[users] audit %s: %v", action, err)
	}
}

func idPtr(id int64) *int64 { return &id }

// invalidateTokens burns uid's outstanding links of each purpose. A failure
// is logged (user id only) and returned; callers answer 500.
func (a *authService) invalidateTokens(op string, uid int64, ps ...users.Purpose) error {
	for _, p := range ps {
		if err := a.st.InvalidateTokens(uid, p); err != nil {
			log.Printf("[users] %s: invalidate %s tokens for user #%d: %v", op, p, uid, err)
			return err
		}
	}
	return nil
}
