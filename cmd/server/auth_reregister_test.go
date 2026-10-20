package main

import (
	"testing"

	"github.com/meshcore-analyzer/users"
)

// Re-registration changes the password and name together or neither.
func TestAuthReregisterProfileFailureKeepsPassword(t *testing.T) {
	f := newAuthFixture(t)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "pending@example.org", DisplayName: "Original", Password: pw}), 200)
	u, err := f.st.GetByEmail("pending@example.org")
	if err != nil {
		t.Fatal(err)
	}
	sent := len(f.fake.Sent())
	token := f.lastToken(t)
	f.execDB(t, `CREATE TRIGGER reject_name BEFORE UPDATE OF display_name ON users BEGIN SELECT RAISE(ABORT, 'profile update failed'); END`)
	w := f.do("POST", "/api/auth/register", registerRequest{Email: u.Email, DisplayName: "Replacement", Password: "replacement password"})
	expectStatus(t, w, 200)
	if response := decode[okResponse](t, w); !response.OK || response.Message != msgCheckMail {
		t.Fatalf("failed re-registration disclosed a different result: %+v", response)
	}
	got, err := f.st.GetByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != u.PasswordHash || got.DisplayName != u.DisplayName {
		t.Fatal("failed re-registration changed part of the account")
	}
	if len(f.fake.Sent()) != sent || f.unusedTokens(t, u.ID, users.PurposeActivate) != 1 {
		t.Fatal("failed re-registration replaced the original activation link")
	}
	// The original link and password must still activate the unchanged account.
	expectStatus(t, f.do("POST", "/api/auth/activate", activateRequest{Token: token, Password: pw}), 200)
}
