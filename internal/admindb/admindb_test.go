package admindb

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "admin.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndAuthenticateAdmin(t *testing.T) {
	s := openTestStore(t)

	a, err := s.CreateAdmin("alice", "correct horse battery staple", RoleSuperAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if a.ID == 0 || a.Username != "alice" || a.Role != RoleSuperAdmin {
		t.Fatalf("unexpected admin: %+v", a)
	}

	got, err := s.Authenticate("alice", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != a.ID || got.Role != RoleSuperAdmin {
		t.Fatalf("unexpected authenticated admin: %+v", got)
	}

	// Case-insensitive username lookup (COLLATE NOCASE).
	if _, err := s.Authenticate("ALICE", "correct horse battery staple"); err != nil {
		t.Fatalf("case-insensitive Authenticate: %v", err)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateAdmin("bob", "hunter2000", RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := s.Authenticate("bob", "wrong password"); err != ErrInvalidCredentials {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateUnknownUsername(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Authenticate("nobody", "whatever"); err != ErrInvalidCredentials {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateDisabledAdmin(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("carol", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE admins SET disabled = 1 WHERE id = ?`, a.ID); err != nil {
		t.Fatalf("disable admin: %v", err)
	}
	if _, err := s.Authenticate("carol", "password12345"); err != ErrInvalidCredentials {
		t.Fatalf("got %v, want ErrInvalidCredentials for disabled admin", err)
	}
}

func TestCreateAdminDuplicateUsername(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateAdmin("dave", "password12345", RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := s.CreateAdmin("dave", "another-password", RoleAdmin, nil); err != ErrUsernameTaken {
		t.Fatalf("got %v, want ErrUsernameTaken", err)
	}
	// Case-insensitive collision too.
	if _, err := s.CreateAdmin("DAVE", "another-password", RoleAdmin, nil); err != ErrUsernameTaken {
		t.Fatalf("got %v, want ErrUsernameTaken (case-insensitive)", err)
	}
}

func TestCreateAdminInvalidRole(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateAdmin("erin", "password12345", Role("owner"), nil); err == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestCreateAdminRecordsCreatedBy(t *testing.T) {
	s := openTestStore(t)
	super, err := s.CreateAdmin("frank", "password12345", RoleSuperAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin super: %v", err)
	}
	created, err := s.CreateAdmin("grace", "password12345", RoleAdmin, &super.ID)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if created.CreatedBy == nil || *created.CreatedBy != super.ID {
		t.Fatalf("expected CreatedBy=%d, got %+v", super.ID, created.CreatedBy)
	}
}

func TestSessionCreateAndValidate(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("heidi", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, expiresAt, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expected future expiry, got %v", expiresAt)
	}

	got, err := s.ValidateSession(token)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if got.ID != a.ID {
		t.Fatalf("got admin id %d, want %d", got.ID, a.ID)
	}
}

func TestValidateSessionUnknownToken(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.ValidateSession("not-a-real-token"); err != ErrSessionInvalid {
		t.Fatalf("got %v, want ErrSessionInvalid", err)
	}
	if _, err := s.ValidateSession(""); err != ErrSessionInvalid {
		t.Fatalf("empty token: got %v, want ErrSessionInvalid", err)
	}
}

func TestValidateSessionExpired(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("ivan", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Force the session into the past directly, bypassing the TTL.
	past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE admin_sessions SET expires_at = ? WHERE token_hash = ?`, past, hashToken(token)); err != nil {
		t.Fatalf("force-expire session: %v", err)
	}
	if _, err := s.ValidateSession(token); err != ErrSessionInvalid {
		t.Fatalf("got %v, want ErrSessionInvalid", err)
	}
}

func TestValidateSessionSlidesExpiry(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("judy", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, firstExpiry, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Pull expires_at backward slightly so the post-validate slide is measurable.
	nudged := firstExpiry.Add(-time.Minute).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE admin_sessions SET expires_at = ? WHERE token_hash = ?`, nudged, hashToken(token)); err != nil {
		t.Fatalf("nudge expiry: %v", err)
	}
	if _, err := s.ValidateSession(token); err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	var newExpiry string
	if err := s.db.QueryRow(`SELECT expires_at FROM admin_sessions WHERE token_hash = ?`, hashToken(token)).Scan(&newExpiry); err != nil {
		t.Fatalf("query expiry: %v", err)
	}
	got, err := time.Parse(time.RFC3339, newExpiry)
	if err != nil {
		t.Fatalf("parse expiry: %v", err)
	}
	if !got.After(firstExpiry.Add(-time.Minute)) {
		t.Fatalf("expected slid expiry after nudged value, got %v", got)
	}
}

func TestDeleteSession(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("kevin", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	token, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.DeleteSession(token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.ValidateSession(token); err != ErrSessionInvalid {
		t.Fatalf("got %v, want ErrSessionInvalid after logout", err)
	}
	// Deleting an already-gone / unknown token is a no-op, not an error.
	if err := s.DeleteSession(token); err != nil {
		t.Fatalf("DeleteSession on missing token: %v", err)
	}
	if err := s.DeleteSession(""); err != nil {
		t.Fatalf("DeleteSession empty token: %v", err)
	}
}

func TestListAdmins(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.CreateAdmin("laura", "password12345", RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if _, err := s.CreateAdmin("mallory", "password12345", RoleAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	admins, err := s.ListAdmins()
	if err != nil {
		t.Fatalf("ListAdmins: %v", err)
	}
	if len(admins) != 2 {
		t.Fatalf("got %d admins, want 2", len(admins))
	}
	if admins[0].Username != "laura" || admins[1].Username != "mallory" {
		t.Fatalf("unexpected order/usernames: %+v", admins)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.CreateAdmin("nina", "password12345", RoleSuperAdmin, nil); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	admins, err := s2.ListAdmins()
	if err != nil {
		t.Fatalf("ListAdmins: %v", err)
	}
	if len(admins) != 1 || admins[0].Username != "nina" {
		t.Fatalf("expected data to survive reopen, got %+v", admins)
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("oscar", "original-password", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if err := s.ChangePassword(a.ID, "original-password", "new-password-123"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	if _, err := s.Authenticate("oscar", "original-password"); err != ErrInvalidCredentials {
		t.Fatalf("old password should no longer work, got %v", err)
	}
	if _, err := s.Authenticate("oscar", "new-password-123"); err != nil {
		t.Fatalf("new password should work: %v", err)
	}
}

func TestChangePasswordWrongOldPassword(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("pat", "original-password", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if err := s.ChangePassword(a.ID, "wrong-old-password", "new-password-123"); err != ErrInvalidCredentials {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
	// Original password must still work — the change must not have applied.
	if _, err := s.Authenticate("pat", "original-password"); err != nil {
		t.Fatalf("original password should still work after a rejected change: %v", err)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("quinn", "original-password", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if err := s.ChangePassword(a.ID, "original-password", "short"); err != ErrPasswordTooShort {
		t.Fatalf("got %v, want ErrPasswordTooShort", err)
	}
	if _, err := s.Authenticate("quinn", "original-password"); err != nil {
		t.Fatalf("original password should still work after a rejected change: %v", err)
	}
}

func TestChangePasswordUnknownAdminID(t *testing.T) {
	s := openTestStore(t)
	if err := s.ChangePassword(999999, "whatever", "new-password-123"); err != ErrInvalidCredentials {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
}

func TestDeleteOtherSessionsKeepsCurrent(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("river", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	keepToken, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession (keep): %v", err)
	}
	otherToken, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession (other): %v", err)
	}

	if err := s.DeleteOtherSessions(a.ID, keepToken); err != nil {
		t.Fatalf("DeleteOtherSessions: %v", err)
	}

	if _, err := s.ValidateSession(keepToken); err != nil {
		t.Fatalf("kept session should still validate: %v", err)
	}
	if _, err := s.ValidateSession(otherToken); err != ErrSessionInvalid {
		t.Fatalf("other session should be invalidated, got %v", err)
	}
}

func TestDeleteOtherSessionsAllWhenKeepEmpty(t *testing.T) {
	s := openTestStore(t)
	a, err := s.CreateAdmin("sam", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	tok1, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	tok2, _, err := s.CreateSession(a.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.DeleteOtherSessions(a.ID, ""); err != nil {
		t.Fatalf("DeleteOtherSessions: %v", err)
	}
	if _, err := s.ValidateSession(tok1); err != ErrSessionInvalid {
		t.Fatalf("tok1 should be invalidated, got %v", err)
	}
	if _, err := s.ValidateSession(tok2); err != ErrSessionInvalid {
		t.Fatalf("tok2 should be invalidated, got %v", err)
	}
}

func TestDeleteOtherSessionsDoesNotAffectOtherAdmins(t *testing.T) {
	s := openTestStore(t)
	a1, err := s.CreateAdmin("tina", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin a1: %v", err)
	}
	a2, err := s.CreateAdmin("uma", "password12345", RoleAdmin, nil)
	if err != nil {
		t.Fatalf("CreateAdmin a2: %v", err)
	}
	a1Token, _, err := s.CreateSession(a1.ID)
	if err != nil {
		t.Fatalf("CreateSession a1: %v", err)
	}
	a2Token, _, err := s.CreateSession(a2.ID)
	if err != nil {
		t.Fatalf("CreateSession a2: %v", err)
	}

	if err := s.DeleteOtherSessions(a1.ID, ""); err != nil {
		t.Fatalf("DeleteOtherSessions: %v", err)
	}
	if _, err := s.ValidateSession(a1Token); err != ErrSessionInvalid {
		t.Fatalf("a1's session should be invalidated, got %v", err)
	}
	if _, err := s.ValidateSession(a2Token); err != nil {
		t.Fatalf("a2's session should be untouched: %v", err)
	}
}
