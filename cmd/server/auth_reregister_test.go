package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/users"
)

// Re-registration changes the password and name together or neither. This
// failure injection runs against both supported account backends.
func TestAuthReregisterProfileFailureKeepsPassword(t *testing.T) {
	f := newAuthFixture(t)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "pending@example.org", DisplayName: "Original", Password: pw}), 200)
	u, err := f.st.GetByEmail("pending@example.org")
	if err != nil {
		t.Fatal(err)
	}
	sent := len(f.fake.Sent())
	f.execDB(t, testNativeSQL(
		`CREATE TRIGGER reject_name BEFORE UPDATE OF display_name ON users BEGIN SELECT RAISE(ABORT, 'profile update failed'); END`,
		`CREATE FUNCTION reject_name() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'profile update failed'; END $$; CREATE TRIGGER reject_name BEFORE UPDATE OF display_name ON users FOR EACH ROW EXECUTE FUNCTION reject_name()`))
	w := f.do("POST", "/api/auth/register", registerRequest{Email: u.Email, DisplayName: "Replacement", Password: "replacement password"})
	expectStatus(t, w, 200) // enumeration-safe response even on a store failure
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
}

// Hold activation uncommitted so the HTTP registration sees the old pending
// row. Observe its UPDATE waiting on that exact transaction, then commit the
// activation. No scheduling sleeps or production test hooks are needed.
func TestAuthReregisterCannotOverwriteActivatedAccount(t *testing.T) {
	if testBackend(t) != dbconfig.Postgres {
		t.Skip("PostgreSQL lock-wait interleaving; pending-only store contract runs on both backends")
	}
	f := newAuthFixture(t)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "activation@example.org", DisplayName: "Original", Password: pw}), 200)
	u, err := f.st.GetByEmail("activation@example.org")
	if err != nil {
		t.Fatal(err)
	}
	sent := len(f.fake.Sent())
	owner, err := openAccountFixtureSQL(f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET status='active', activated_at=$1 WHERE id=$2`, time.Now().Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- f.serve("POST", "/api/auth/register", registerRequest{Email: u.Email, DisplayName: "Replacement", Password: "replacement password"})
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("registration did not finish after the activation lock was released")
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("registration did not reach the activation row lock")
		case <-ticker.C:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		expectStatus(t, w, 200)
	case <-ctx.Done():
		t.Fatal("registration did not finish")
	}
	got, err := f.st.GetByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != users.StatusActive || got.PasswordHash != u.PasswordHash || got.DisplayName != u.DisplayName {
		t.Fatal("registration overwrote an account activated after its pending lookup")
	}
	if len(f.fake.Sent()) != sent {
		t.Fatal("registration issued an activation mail after the account became active")
	}
}
