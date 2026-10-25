package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	// observerResendLimit is the most credentials mails a user gets in
	// observerResendWindow, the first one included.
	observerResendLimit  = 3
	observerResendWindow = 24 * time.Hour

	observerMailTimeout = 15 * time.Second
)

// observerLoginJSON never carries credential material: not the password,
// its hash or its ciphertext.
type observerLoginJSON struct {
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
	Revoked   bool   `json:"revoked"`
}

type accountObserverJSON struct {
	Pubkey     string  `json:"pubkey"`
	Name       string  `json:"name"`
	LastSeenAt *string `json:"lastSeenAt"`
}

// observerLoginEmpty is the answer for a user without a login.
type observerLoginEmpty struct {
	Login *observerLoginJSON `json:"login"`
}

type observerLoginResponse struct {
	Login     *observerLoginJSON    `json:"login"`
	Observers []accountObserverJSON `json:"observers"`
}

type sentResponse struct {
	Sent bool `json:"sent"`
}

// observerFeature answers 503 when the feature is on but the key or the
// login index failed to load at startup.
func (a *authService) observerFeature(w http.ResponseWriter) bool {
	if a.sealbox == nil || a.obsIdx == nil {
		writeError(w, http.StatusServiceUnavailable, "observer logins unavailable")
		return false
	}
	return true
}

func (s *Server) handleObserverLoginGet(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	l, err := a.st.MQTTLoginForUser(u.ID)
	if errors.Is(err, users.ErrNotFound) {
		writeJSON(w, observerLoginEmpty{})
		return
	}
	if err != nil {
		log.Printf("[observer-logins] get login for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	bindings, err := a.st.ObserverBindingsForUser(u.ID)
	if err != nil {
		log.Printf("[observer-logins] list bindings for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, observerLoginResponse{
		Login:     &observerLoginJSON{Username: l.Username, CreatedAt: rfc3339(l.CreatedAt), Revoked: l.RevokedAt != nil},
		Observers: s.accountObserversJSON(bindings),
	})
}

// accountObserversJSON adds the analyzer's name and last-seen time to the
// bindings with one bulk query; always a non-nil slice.
func (s *Server) accountObserversJSON(bindings []users.ObserverBinding) []accountObserverJSON {
	pks := make([]string, 0, len(bindings))
	for _, b := range bindings {
		pks = append(pks, b.Pubkey)
	}
	info := s.observerInfo(pks)
	out := make([]accountObserverJSON, 0, len(bindings))
	for _, b := range bindings {
		o := accountObserverJSON{Pubkey: b.Pubkey}
		if i, ok := info[strings.ToUpper(b.Pubkey)]; ok {
			o.Name = i.name
			if i.lastSeen != "" {
				ls := i.lastSeen
				o.LastSeenAt = &ls
			}
		}
		out = append(out, o)
	}
	return out
}

type observerRow struct{ name, lastSeen string }

// observerInfo reads name and last_seen of the given observers from the
// analyzer DB (read-only), keyed by uppercase pubkey. It is informational:
// no DB or a failed query give an empty map.
func (s *Server) observerInfo(pks []string) map[string]observerRow {
	out := map[string]observerRow{}
	if len(pks) == 0 || s.db == nil || s.db.conn == nil {
		return out
	}
	args := make([]interface{}, len(pks))
	for i, pk := range pks {
		args[i] = strings.ToUpper(pk)
	}
	rows, err := s.db.conn.Query(`SELECT id, name, last_seen FROM observers WHERE UPPER(id) IN (`+sqlPlaceholders(len(pks))+`)`, args...)
	if err != nil {
		log.Printf("[observer-logins] observer info: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var name, seen sql.NullString
		if rows.Scan(&id, &name, &seen) == nil {
			out[strings.ToUpper(id)] = observerRow{name: name.String, lastSeen: seen.String}
		}
	}
	return out
}

func (s *Server) handleObserverLoginCreate(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.observerFeature(w) {
		return
	}
	switch l, err := a.st.MQTTLoginForUser(u.ID); {
	case err == nil && l.RevokedAt != nil:
		writeError(w, http.StatusForbidden, "your observer login was revoked")
		return
	case err == nil:
		writeError(w, http.StatusConflict, "you already have an observer login")
		return
	case !errors.Is(err, users.ErrNotFound):
		log.Printf("[observer-logins] get login for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	l, password, ok := a.newMQTTLogin(w, u.ID)
	if !ok {
		return
	}
	username := l.Username
	switch err := a.st.CreateMQTTLogin(l); {
	case err == nil:
	case errors.Is(err, users.ErrMQTTLoginExists):
		writeError(w, http.StatusConflict, "you already have an observer login")
		return
	default:
		log.Printf("[observer-logins] create login for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.obsIdx.setLogin(l)
	a.enableOwnObserverMails(u.ID)
	a.auditAsync(idPtr(u.ID), "observer.login.create", idPtr(u.ID), map[string]string{"username": username})
	if !a.sendObserverCredentials(r.Context(), w, u, username, password) {
		return
	}
	writeJSONStatus(w, http.StatusCreated, sentResponse{Sent: true})
}

func (s *Server) handleObserverLoginResend(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.observerFeature(w) {
		return
	}
	l, err := a.st.MQTTLoginForUser(u.ID)
	if errors.Is(err, users.ErrNotFound) {
		writeError(w, http.StatusNotFound, "you have no observer login")
		return
	}
	if err != nil {
		log.Printf("[observer-logins] get login for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if l.RevokedAt != nil {
		writeError(w, http.StatusForbidden, "your observer login was revoked")
		return
	}
	n, err := a.credentialsMailsSince(u.ID, a.now().Add(-observerResendWindow))
	if err != nil {
		log.Printf("[observer-logins] count mails for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n >= observerResendLimit {
		w.Header().Set("Retry-After", strconv.Itoa(int(observerResendWindow/time.Second)))
		writeError(w, http.StatusTooManyRequests, "too many credential mails, try again later")
		return
	}
	password, err := a.sealbox.Open(l.PasswordEnc)
	if err != nil {
		log.Printf("[observer-logins] open password of user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.auditAsync(idPtr(u.ID), "observer.login.resend", idPtr(u.ID), nil)
	if !a.sendObserverCredentials(r.Context(), w, u, l.Username, password) {
		return
	}
	writeJSON(w, sentResponse{Sent: true})
}

// newMQTTLogin generates fresh credentials for userID. It answers 500 itself
// and returns false on failure. The password is returned in the clear for the
// mail only; the login holds its hash and sealed form.
func (a *authService) newMQTTLogin(w http.ResponseWriter, userID int64) (users.MQTTLogin, string, bool) {
	username, password, err := generateMQTTCredentials()
	if err != nil {
		log.Printf("[observer-logins] generate credentials: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return users.MQTTLogin{}, "", false
	}
	enc, err := a.sealbox.Seal(password)
	if err != nil {
		log.Printf("[observer-logins] seal password: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return users.MQTTLogin{}, "", false
	}
	sum := sha256.Sum256([]byte(password))
	return users.MQTTLogin{UserID: userID, Username: username, PasswordHash: hex.EncodeToString(sum[:]), PasswordEnc: enc, CreatedAt: a.now()}, password, true
}

// credentialsMailsSince counts the user's credentials mails sent after
// since, from the mail log. It reads the newest 50 rows; the limit is far
// below that.
func (a *authService) credentialsMailsSince(uid int64, since time.Time) (int, error) {
	rows, err := a.st.MailForUser(uid, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range rows {
		if m.Purpose == observerCredentialsPurpose && m.SentAt.After(since) {
			n++
		}
	}
	return n, nil
}

// sendObserverCredentials mails the credentials and answers 503 itself when
// the mail fails (sendMail logs the error without the message body).
func (a *authService) sendObserverCredentials(ctx context.Context, w http.ResponseWriter, u *users.User, username, password string) bool {
	ctx, cancel := context.WithTimeout(ctx, observerMailTimeout)
	defer cancel()
	if err := a.sendMail(ctx, u, observerCredentialsPurpose, a.observerCredentialsMail(u, username, password)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not send the mail, try again later")
		return false
	}
	return true
}

// enableOwnObserverMails adds observer.offline.own to the user's notification
// events. A failure is logged only: the login exists and its mail goes out.
func (a *authService) enableOwnObserverMails(userID int64) {
	prefs, err := a.st.NotifyPrefsFor(userID)
	if err == nil && !prefs.Has(users.NotifyObserverOfflineOwn) {
		_, err = a.st.SetNotifyPrefs(userID, prefs.Enabled, append(prefs.Events, users.NotifyObserverOfflineOwn))
	}
	if err != nil {
		log.Printf("[observer-logins] enable owner mails for user #%d: %v", userID, err)
	}
}
