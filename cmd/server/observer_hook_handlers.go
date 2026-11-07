package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	hookSecretHeader = "X-CoreScope-Hook-Secret"

	// mqttAuthTouchEvery bounds last-auth writes: the broker authenticates on
	// every (re)connect, and users.db has one writer.
	mqttAuthTouchEvery = 10 * time.Minute

	observerBoundMailPurpose = "observer-bound"

	// hookBodyLimit caps the broker request body.
	hookBodyLimit = 16 << 10
)

// emqxAuthnReq is the body EMQX's HTTP authentication sends.
type emqxAuthnReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	ClientID string `json:"clientid"`
}

// emqxAuthzReq is the body EMQX's HTTP authorization sends. Action is
// "publish" or "subscribe".
type emqxAuthzReq struct {
	Username string `json:"username"`
	Topic    string `json:"topic"`
	Action   string `json:"action"`
}

// emqxHookResp is the answer: allow, deny or ignore.
type emqxHookResp struct {
	Result hookResult `json:"result"`
}

// authTouches throttles TouchMQTTAuth per user. It holds at most one entry
// per login.
type authTouches struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

// due reports whether uid should be written at now, and records it if so.
func (t *authTouches) due(uid int64, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.last[uid]; ok && now.Sub(prev) < mqttAuthTouchEvery {
		return false
	}
	if t.last == nil {
		t.last = map[int64]time.Time{}
	}
	t.last[uid] = now
	return true
}

// hookAuthed checks the shared secret and decodes the body into v. It has
// answered the request when it returns false.
func (s *Server) hookAuthed(w http.ResponseWriter, r *http.Request, v any) bool {
	if !constantTimeEqual(r.Header.Get(hookSecretHeader), s.auth.set.observerLogins.hookSecret) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if s.auth.obsIdx == nil {
		writeError(w, http.StatusServiceUnavailable, "observer logins unavailable")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, hookBodyLimit)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return false
	}
	return true
}

func (s *Server) handleEMQXAuthn(w http.ResponseWriter, r *http.Request) {
	var req emqxAuthnReq
	if !s.hookAuthed(w, r, &req) {
		return
	}
	a := s.auth
	res := a.obsIdx.authenticate(req.Username, req.Password)
	if res == hookAllow {
		if uid, ok := a.obsIdx.userIDFor(req.Username); ok && a.touches.due(uid, time.Now()) {
			a.auditWG.Add(1)
			go func() {
				defer a.auditWG.Done()
				if err := a.st.TouchMQTTAuth(uid, time.Now()); err != nil {
					log.Printf("[observer-logins] touch last auth: user #%d: %v", uid, err)
				}
			}()
		}
	}
	writeJSON(w, emqxHookResp{Result: res})
}

func (s *Server) handleEMQXAuthz(w http.ResponseWriter, r *http.Request) {
	var req emqxAuthzReq
	if !s.hookAuthed(w, r, &req) {
		return
	}
	a := s.auth
	res, pk, created := a.obsIdx.authorize(req.Username, req.Action, req.Topic, func(pk string, uid int64) (*int64, bool, error) {
		return a.st.BindObserver(pk, uid, "auto")
	}, a.st.ConfirmObserver)
	writeJSON(w, emqxHookResp{Result: res})
	if res == hookAllow && created {
		uid, _ := a.obsIdx.userIDFor(req.Username)
		a.auditWG.Add(1)
		go func() {
			defer a.auditWG.Done()
			s.mailAdminsIfKnownObserver(pk, uid)
		}()
	}
}

// observerKnown reports whether the analyzer already has an observer with
// this pubkey, through the read-only connection.
func (s *Server) observerKnown(pk string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var one int
	err := s.db.conn.QueryRow(`SELECT 1 FROM observers WHERE UPPER(id) = ?`, strings.ToUpper(pk)).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// mailAdminsIfKnownObserver tells the admins that a user just claimed an
// observer that was already reporting, so they can check the claim.
func (s *Server) mailAdminsIfKnownObserver(pk string, uid int64) {
	a := s.auth
	known, err := s.observerKnown(pk)
	if err != nil {
		log.Printf("[observer-logins] observer lookup %s: %v", pk[:8], err)
		return
	}
	if !known {
		return
	}
	claimer, err := a.st.GetByID(uid)
	if err != nil {
		log.Printf("[observer-logins] claimer #%d: %v", uid, err)
		return
	}
	admins, err := a.st.List(users.ListFilter{Role: users.RoleAdmin, Status: users.StatusActive})
	if err != nil {
		log.Printf("[observer-logins] list admins: %v", err)
		return
	}
	for i := range admins {
		adm := &admins[i]
		if adm.EmailBouncing {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), notifySendTimeout)
		msg := a.render(adm.Email, adm.DisplayName, observerBoundMailPurpose, mailContent{
			subject:  "An existing observer was claimed",
			greeting: "Hello " + adm.DisplayName + ",",
			paragraphs: []string{
				"Observer " + pk[:12] + " was already reporting to " + a.set.baseURL.Host + ". " +
					mailSafeText(claimer.DisplayName) + " (user #" + strconv.FormatInt(uid, 10) + ") has just bound it to their personal MQTT login by publishing as it.",
				"If that is not the owner, reassign the observer.",
			},
			actionLabel: "Users", actionURL: a.set.baseURL.String() + "/#/admin",
		})
		_ = a.sendMail(ctx, adm, observerBoundMailPurpose, msg) // failure logged by sendMail
		cancel()
	}
}
