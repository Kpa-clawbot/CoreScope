package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/users"
)

type okResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type meResponse struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Role        users.Role `json:"role"`
	CSRFToken   string     `json:"csrfToken"`
}

type registerRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type deviceTokenRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

type deviceTokenUser struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

type deviceTokenResponse struct {
	Token     string          `json:"token"`
	ExpiresAt string          `json:"expiresAt"`
	User      deviceTokenUser `json:"user"`
}

type companionChallengeRequest struct {
	Pubkey string `json:"pubkey"`
}

type companionChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expiresAt"`
	// Host is what the client signs: the host of userManagement.publicBaseUrl,
	// the same one linkMessage verifies against.
	Host string `json:"host"`
}

type companionLinkRequest struct {
	Pubkey    string `json:"pubkey"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
	Name      string `json:"name"`
}

type companionLinkResponse struct {
	Pubkey   string `json:"pubkey"`
	Name     string `json:"name"`
	LinkedAt string `json:"linkedAt"`
}

type companionJSON struct {
	Pubkey     string  `json:"pubkey"`
	Name       string  `json:"name"`
	LinkedAt   string  `json:"linkedAt"`
	LastSeenAt *string `json:"lastSeenAt"` // newest client_receptions.rx_at, or null
}

type emailRequest struct {
	Email string `json:"email"`
}

type tokenRequest struct {
	Token string `json:"token"`
}

type activateRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type profileRequest struct {
	DisplayName string `json:"displayName"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type emailChangeRequest struct {
	NewEmail        string `json:"newEmail"`
	CurrentPassword string `json:"currentPassword"`
}

type passwordConfirmRequest struct {
	CurrentPassword string `json:"currentPassword"`
}

type roleRequest struct {
	Role users.Role `json:"role"`
}

type sessionJSON struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`  // "web" or "device"
	Label      string `json:"label"` // device name; "" for web sessions
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	UserAgent  string `json:"userAgent"`
	Current    bool   `json:"current"`
}

type mailEventJSON struct {
	Event  string `json:"event"`
	At     string `json:"at"`
	Reason string `json:"reason,omitempty"`
}

type mailJSON struct {
	ID          int64           `json:"id"`
	Purpose     string          `json:"purpose"`
	To          string          `json:"to"`
	SentAt      string          `json:"sentAt"`
	LastEvent   string          `json:"lastEvent"`
	LastEventAt string          `json:"lastEventAt"`
	LastReason  string          `json:"lastReason,omitempty"`
	Events      []mailEventJSON `json:"events,omitempty"`
}

type adminUserJSON struct {
	ID                int64        `json:"id"`
	Email             string       `json:"email"`
	DisplayName       string       `json:"displayName"`
	Role              users.Role   `json:"role"`
	Status            users.Status `json:"status"`
	CreatedAt         string       `json:"createdAt"`
	ActivatedAt       *string      `json:"activatedAt"`
	ActivatedManually bool         `json:"activatedManually"`
	ActivatedBy       *int64       `json:"activatedBy"`
	LastLoginAt       *string      `json:"lastLoginAt"`
	EmailBouncing     bool         `json:"emailBouncing"`
	ConfigAdmin       bool         `json:"configAdmin"`
	LastMail          *mailJSON    `json:"lastMail"`
}

type auditJSON struct {
	ID           int64             `json:"id"`
	At           string            `json:"at"`
	ActorUserID  *int64            `json:"actorUserId"`
	Action       string            `json:"action"`
	TargetUserID *int64            `json:"targetUserId"`
	Detail       map[string]string `json:"detail"`
}

type adminUserDetailJSON struct {
	User       adminUserJSON   `json:"user"`
	Sessions   []sessionJSON   `json:"sessions"`
	Companions []companionJSON `json:"companions"`
	Mail       []mailJSON      `json:"mail"`
	Audit      []auditJSON     `json:"audit"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func meFrom(u *users.User, sess *users.Session) meResponse {
	return meResponse{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, CSRFToken: sess.CSRFToken}
}

func sessionToJSON(s users.Session, currentID int64) sessionJSON {
	return sessionJSON{ID: s.ID, Kind: s.Kind, Label: s.Label, CreatedAt: rfc3339(s.CreatedAt), LastSeenAt: rfc3339(s.LastSeenAt),
		ExpiresAt: rfc3339(s.ExpiresAt), UserAgent: s.UserAgent, Current: s.ID == currentID}
}

func mailToJSON(m users.MailRecord) mailJSON {
	out := mailJSON{ID: m.ID, Purpose: m.Purpose, To: m.ToEmail, SentAt: rfc3339(m.SentAt),
		LastEvent: m.LastEvent, LastEventAt: rfc3339(m.LastEventAt), LastReason: m.LastReason}
	for _, e := range m.Events {
		out.Events = append(out.Events, mailEventJSON{Event: e.Event, At: rfc3339(e.At), Reason: e.Reason})
	}
	return out
}

func (a *authService) adminRow(u users.User, last map[int64]users.MailRecord) adminUserJSON {
	row := adminUserJSON{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, Status: u.Status,
		CreatedAt: rfc3339(u.CreatedAt), ActivatedAt: rfc3339Ptr(u.ActivatedAt), ActivatedManually: u.ActivatedBy != nil,
		ActivatedBy: u.ActivatedBy, LastLoginAt: rfc3339Ptr(u.LastLoginAt), EmailBouncing: u.EmailBouncing,
		ConfigAdmin: u.Role == users.RoleAdmin && a.isConfigAdmin(u.Email)}
	if m, ok := last[u.ID]; ok {
		mj := mailToJSON(m)
		row.LastMail = &mj
	}
	return row
}

// decodeJSON reads a small JSON body into dst, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONMax(w, r, dst, 16<<10, http.StatusBadRequest)
}

// decodeJSONMax is decodeJSON with a body cap of limit bytes. A body over
// the cap answers tooLarge (decodeJSON keeps its historical 400).
func decodeJSONMax(w http.ResponseWriter, r *http.Request, dst any, limit int64, tooLarge int) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) && tooLarge != http.StatusBadRequest {
			writeError(w, tooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// writeValidation writes 400 for a users.ValidationError and reports
// whether it did.
func writeValidation(w http.ResponseWriter, err error) bool {
	var ve *users.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return true
	}
	return false
}

func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, users.ErrTokenExpired):
		writeError(w, http.StatusGone, "this link has expired")
	case errors.Is(err, users.ErrTokenInvalid):
		writeError(w, http.StatusGone, "this link is invalid or was already used")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
