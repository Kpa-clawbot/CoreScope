package main

import (
	"github.com/meshcore-analyzer/dbconfig"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	// approved is the snapshot of approved hashtag channel names, filled at
	// construction and reloaded from users.db after every proposal decision.
	// approvedMu serialises the reloads so an older read never replaces a
	// newer one.
	approvedMu sync.Mutex
	approved   atomic.Pointer[[]string]

	// notify evaluates node notifications; nil unless
	// userManagement.notifications.enabled.
	notify *notifier

	warnIndistinct sync.Once
	stop           chan struct{}
	stopOnce       sync.Once
	wg             sync.WaitGroup // the janitor and the notifier
	auditWG        sync.WaitGroup // in-flight auditAsync writes
}

func newAuthService(set *userMgmtSettings, st *users.Store, m mailer.Mailer) *authService {
	a := &authService{
		st: st, mail: m, set: set,
		ipr:         &wsLimiter{trustedProxies: set.trustedProxies},
		login:       newRateLimiter(10, 15*time.Minute),
		signup:      newRateLimiter(5, time.Hour),
		hook:        newRateLimiter(600, time.Minute),
		settingsPut: newRateLimiter(60, time.Hour),
		stop:        make(chan struct{}),
	}
	a.refreshApproved()
	return a
}

// initUserManagement builds s.auth when the feature is on. Call it after
// NewServer and before RegisterRoutes. measurementDatabaseURL is passed to
// users.Open as a forbidden database, so accounts cannot share telemetry storage.
func (s *Server) initUserManagement(measurementTarget string) error {
	if !s.cfg.UserManagementEnabled() {
		return nil
	}
	set, err := resolveUserManagement(s.cfg.UserManagement, runtimeStateDir, os.Getenv)
	if err != nil {
		return err
	}
	storage := dbconfig.Storage{Backend: dbconfig.SQLite, DBPath: measurementTarget, UsersDBPath: set.databaseURL, StateDir: runtimeStateDir}
	if strings.HasPrefix(measurementTarget, "postgres://") || strings.HasPrefix(measurementTarget, "postgresql://") {
		storage = dbconfig.Storage{Backend: dbconfig.Postgres, ReaderDatabaseURL: measurementTarget, UsersDatabaseURL: set.databaseURL, StateDir: runtimeStateDir}
	}
	return s.initUserManagementStorage(storage)
}
func (s *Server) initUserManagementStorage(storage dbconfig.Storage) error {
	if !s.cfg.UserManagementEnabled() {
		return nil
	}
	set, err := resolveUserManagement(s.cfg.UserManagement, storage.StateDir, os.Getenv)
	if err != nil {
		return err
	}
	st, err := users.OpenStorage(storage)
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
	if set.notify.enabled {
		s.auth.notify = newNotifier(s.auth, serverNotifySource{s: s}, time.Now)
		s.auth.wg.Add(1)
		go func() {
			defer s.auth.wg.Done()
			s.auth.notify.loop(s.auth.stop, set.notify.interval)
		}()
		log.Printf("[notify] node notifications enabled: every %s, at most %d mails per user and %d in total per 24 hours",
			set.notify.interval, set.notify.perUserPerDay, set.notify.maxMailsPerDay)
	}
	return nil
}

// closeUserManagement stops the janitor and closes the account connection pool. Safe when off.
func (s *Server) closeUserManagement() {
	if s.auth == nil {
		return
	}
	s.auth.stopOnce.Do(func() {
		close(s.auth.stop)
		s.auth.wg.Wait()
		s.auth.waitAudits()
		if err := s.auth.st.Close(); err != nil {
			log.Printf("[users] close: %v", err)
		}
	})
}

func (a *authService) logStartup() {
	log.Printf("[users] user management enabled: %s, %d config admin(s), webhook=%v",
		a.st.Backend(), len(a.set.adminEmails), a.set.webhookSecret != "")
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
		a.maybeBackup(time.Now())
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
	// Login rows only; every other audit action is kept (admin-dashboard spec).
	if _, err := a.st.PruneAudit([]string{"user.login", "user.login.failed"}, 90*24*time.Hour); err != nil {
		log.Printf("[users] prune login audit: %v", err)
	}
	if _, err := a.st.PruneProposals(proposalRetention); err != nil {
		log.Printf("[users] prune proposals: %v", err)
	}
	a.login.gc()
	a.signup.gc()
	a.hook.gc()
	a.settingsPut.gc()
}

func (a *authService) isConfigAdmin(email string) bool { return a.set.adminEmails[email] }

// refreshApproved reloads the approved hashtag channel names from users.db,
// oldest approval first and capped at maxApproved like the ingestor. The
// mutex spans the read and the store, so concurrent decisions cannot leave
// an older read in place. A failure is logged and keeps the previous
// snapshot. No-op with proposals off.
func (a *authService) refreshApproved() {
	if !a.set.proposals.enabled {
		return
	}
	a.approvedMu.Lock()
	defer a.approvedMu.Unlock()
	list, err := a.st.ApprovedSubjects(users.KindHashtagChannel, a.set.proposals.maxApproved)
	if err != nil {
		log.Printf("[users] load approved channels: %v", err)
		return
	}
	a.approved.Store(&list)
}

// approvedChannels returns the current snapshot; never nil.
func (a *authService) approvedChannels() []string {
	if p := a.approved.Load(); p != nil {
		return *p
	}
	return []string{}
}

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

// auditAsync writes an audit row in the background, so neither the write
// nor a locked users.db delays the response. Best-effort: a failure is
// logged by a.audit, and a row is lost if the process stops first.
func (a *authService) auditAsync(actor *int64, action string, target *int64, detail map[string]string) {
	a.auditWG.Add(1)
	go func() {
		defer a.auditWG.Done()
		a.audit(actor, action, target, detail)
	}()
}

// waitAudits blocks until every auditAsync write has finished.
func (a *authService) waitAudits() { a.auditWG.Wait() }

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

// absForLog resolves path for a log line, so a relative users.db path shows
// which file it means; the path as given when it cannot be resolved.
func absForLog(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
