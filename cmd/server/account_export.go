package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/users"
)

// accountExport is GET /api/account/export: everything users.db holds about
// the logged-in account (docs/specs/2026-10-08-account-export-and-users-backup-design.md).
// Credentials are left out on purpose: the password hash, session, link and
// unsubscribe tokens; of a pending email change only the new address is
// exported. Other accounts appear as ids only (activatedBy, audit).
type accountExport struct {
	FormatVersion int                 `json:"formatVersion"`
	ExportedAt    string              `json:"exportedAt"`
	Instance      string              `json:"instance"`
	Profile       exportProfile       `json:"profile"`
	Sessions      []exportSession     `json:"sessions"`
	Settings      *exportSettings     `json:"settings"`
	Proposals     []exportProposal    `json:"proposals"`
	Notifications exportNotifications `json:"notifications"`
	Audit         []exportAuditEntry  `json:"audit"`
	Mail          []exportMail        `json:"mail"`
}

type exportProfile struct {
	ID            int64        `json:"id"`
	Email         string       `json:"email"`
	PendingEmail  *string      `json:"pendingEmail"` // unconfirmed email change: the address only
	DisplayName   string       `json:"displayName"`
	Role          users.Role   `json:"role"`
	Status        users.Status `json:"status"`
	CreatedAt     string       `json:"createdAt"`
	LastLoginAt   *string      `json:"lastLoginAt"`
	EmailBouncing bool         `json:"emailBouncing"`
	ActivatedAt   *string      `json:"activatedAt"`
	ActivatedBy   *int64       `json:"activatedBy"`
}

type exportSession struct {
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	UserAgent  string `json:"userAgent,omitempty"`
}

type exportSettings struct {
	Revision  int64       `json:"revision"`
	UpdatedAt string      `json:"updatedAt"`
	Doc       settingsDoc `json:"doc"`
}

type exportProposal struct {
	Kind      string               `json:"kind"`
	Subject   string               `json:"subject"`
	Status    users.ProposalStatus `json:"status"`
	Note      string               `json:"note"`
	CreatedAt string               `json:"createdAt"`
	DecidedAt *string              `json:"decidedAt"`
}

type exportNotifications struct {
	Prefs   *exportNotifyPrefs `json:"prefs"`
	Watches []exportWatch      `json:"watches"`
}

type exportNotifyPrefs struct {
	Enabled bool     `json:"enabled"`
	Events  []string `json:"events"`
}

type exportWatch struct {
	Pubkey    string `json:"pubkey"`
	CreatedAt string `json:"createdAt"`
}

type exportAuditEntry struct {
	At           string            `json:"at"`
	Action       string            `json:"action"`
	ActorUserID  *int64            `json:"actorUserId"`
	TargetUserID *int64            `json:"targetUserId"`
	Detail       map[string]string `json:"detail"`
}

type exportMail struct {
	To       string          `json:"to"`
	Purpose  string          `json:"purpose"`
	SentAt   string          `json:"sentAt"`
	Status   string          `json:"status"`
	StatusAt string          `json:"statusAt"`
	Reason   string          `json:"reason,omitempty"`
	Events   []mailEventJSON `json:"events"`
}

// buildAccountExport reads every section for u. It only reads users.db.
func (a *authService) buildAccountExport(u *users.User, now time.Time) (*accountExport, error) {
	x := &accountExport{
		FormatVersion: 1,
		ExportedAt:    rfc3339(now),
		Instance:      a.set.baseURL.String(),
		Profile: exportProfile{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, Status: u.Status,
			CreatedAt: rfc3339(u.CreatedAt), LastLoginAt: rfc3339Ptr(u.LastLoginAt), EmailBouncing: u.EmailBouncing,
			ActivatedAt: rfc3339Ptr(u.ActivatedAt), ActivatedBy: u.ActivatedBy},
		Sessions:      []exportSession{},
		Proposals:     []exportProposal{},
		Notifications: exportNotifications{Watches: []exportWatch{}},
		Audit:         []exportAuditEntry{},
		Mail:          []exportMail{},
	}
	pending, err := a.st.PendingEmailChange(u.ID)
	if err != nil {
		return nil, fmt.Errorf("pending email: %w", err)
	}
	if pending != "" {
		x.Profile.PendingEmail = &pending
	}
	sessions, err := a.st.ListSessions(u.ID)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	for _, s := range sessions {
		x.Sessions = append(x.Sessions, exportSession{CreatedAt: rfc3339(s.CreatedAt), LastSeenAt: rfc3339(s.LastSeenAt), UserAgent: s.UserAgent})
	}
	rec, err := a.st.SettingsRecordFor(u.ID)
	if err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if rec != nil {
		var doc settingsDoc
		if err := json.Unmarshal([]byte(rec.Doc), &doc); err != nil {
			return nil, fmt.Errorf("settings decode (%d bytes): %w", len(rec.Doc), err)
		}
		x.Settings = &exportSettings{Revision: rec.Version.Revision, UpdatedAt: rfc3339(rec.UpdatedAt), Doc: doc}
	}
	props, err := a.st.AllProposalsByUser(u.ID)
	if err != nil {
		return nil, fmt.Errorf("proposals: %w", err)
	}
	for _, p := range props {
		x.Proposals = append(x.Proposals, exportProposal{Kind: p.Kind, Subject: p.Subject, Status: p.Status, Note: p.Note,
			CreatedAt: rfc3339(p.CreatedAt), DecidedAt: rfc3339Ptr(p.DecidedAt)})
	}
	prefs, err := a.st.StoredNotifyPrefs(u.ID)
	if err != nil {
		return nil, fmt.Errorf("notification prefs: %w", err)
	}
	if prefs != nil {
		x.Notifications.Prefs = &exportNotifyPrefs{Enabled: prefs.Enabled, Events: prefs.Events}
	}
	watches, err := a.st.WatchesFor(u.ID)
	if err != nil {
		return nil, fmt.Errorf("watches: %w", err)
	}
	for _, w := range watches {
		x.Notifications.Watches = append(x.Notifications.Watches, exportWatch{Pubkey: w.Pubkey, CreatedAt: rfc3339(w.CreatedAt)})
	}
	entries, err := a.st.AuditAllFor(u.ID)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	for _, e := range entries {
		x.Audit = append(x.Audit, exportAuditEntry{At: rfc3339(e.At), Action: e.Action,
			ActorUserID: e.ActorUserID, TargetUserID: e.TargetUserID, Detail: e.Detail})
	}
	mails, err := a.st.MailAllForUser(u.ID)
	if err != nil {
		return nil, fmt.Errorf("mail: %w", err)
	}
	for _, m := range mails {
		em := exportMail{To: m.ToEmail, Purpose: m.Purpose, SentAt: rfc3339(m.SentAt), Status: m.LastEvent,
			StatusAt: rfc3339(m.LastEventAt), Reason: m.LastReason, Events: []mailEventJSON{}}
		for _, e := range m.Events {
			em.Events = append(em.Events, mailEventJSON{Event: e.Event, At: rfc3339(e.At), Reason: e.Reason})
		}
		x.Mail = append(x.Mail, em)
	}
	return x, nil
}

// handleAccountExport answers the export as a download. GET, so a plain
// link works and no CSRF token is needed. The user.export audit row is
// written after the export is built, so the file shows the state before it.
func (s *Server) handleAccountExport(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	now := time.Now().UTC()
	x, err := a.buildAccountExport(u, now)
	if err != nil {
		log.Printf("[users] export for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(u.ID), "user.export", idPtr(u.ID), nil)
	w.Header().Set("Content-Disposition", `attachment; filename="corescope-account-`+now.Format("2006-01-02")+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, x)
}
