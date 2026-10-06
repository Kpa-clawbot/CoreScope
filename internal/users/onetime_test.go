package users

import (
	"errors"
	"testing"
	"time"
)

func TestTokenConsumeOnce(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "t@example.org", "Tok")
	raw, err := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	uid, newEmail, err := st.ConsumeToken(raw, PurposeActivate)
	if err != nil || uid != u.ID || newEmail != "" {
		t.Fatalf("ConsumeToken = %d, %q, %v", uid, newEmail, err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second consume err = %v", err)
	}
}

func TestTokenPurposeMismatchDoesNotConsume(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "m@example.org", "Mis")
	raw, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeReset); err != nil {
		t.Fatalf("token was consumed by the mismatched attempt: %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "e@example.org", "Exp")
	raw, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	clk.Advance(time.Hour)
	if _, _, err := st.ConsumeToken(raw, PurposeReset); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestNewTokenInvalidatesOlderSamePurpose(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "n@example.org", "New")
	old, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	keep, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	fresh, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if _, _, err := st.ConsumeToken(old, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("old activation link still works: %v", err)
	}
	if _, _, err := st.ConsumeToken(fresh, PurposeActivate); err != nil {
		t.Fatalf("fresh link: %v", err)
	}
	if _, _, err := st.ConsumeToken(keep, PurposeReset); err != nil {
		t.Fatalf("other purpose was invalidated: %v", err)
	}
}

func TestEmailChangeTokenCarriesNewEmail(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "c@example.org", "Chg")
	raw, _ := st.IssueToken(u.ID, PurposeEmailChange, time.Hour, "new@example.org")
	_, ne, err := st.ConsumeToken(raw, PurposeEmailChange)
	if err != nil || ne != "new@example.org" {
		t.Fatalf("ConsumeToken = %q, %v", ne, err)
	}
}

func TestInvalidateAndPruneTokens(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "i@example.org", "Inv")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err := st.InvalidateTokens(u.ID, PurposeActivate); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ConsumeToken(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("invalidated token still works: %v", err)
	}
	clk.Advance(8 * 24 * time.Hour)
	if n, err := st.PruneTokens(7 * 24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("PruneTokens = %d, %v", n, err)
	}
}

func TestPruneStalePending(t *testing.T) {
	st, clk := newTestStore(t)
	stale := mustCreate(t, st, "stale@example.org", "Stale")
	st.IssueToken(stale.ID, PurposeActivate, 48*time.Hour, "")
	fresh := mustCreate(t, st, "fresh@example.org", "Fresh")
	_ = fresh
	clk.Advance(49 * time.Hour)
	resent := mustCreate(t, st, "resent@example.org", "Resent")
	_ = resent
	n, err := st.PruneStalePending(48 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// stale: old + token expired → pruned. fresh: old, no token → pruned.
	// resent: created after the advance → kept.
	if n != 2 {
		t.Fatalf("pruned %d; want 2", n)
	}
	if _, err := st.GetByEmail("resent@example.org"); err != nil {
		t.Fatalf("recent pending pruned: %v", err)
	}
	// An old pending account with a live (re-sent) token survives.
	old := mustCreate(t, st, "old@example.org", "Old")
	clk.Advance(49 * time.Hour)
	st.IssueToken(old.ID, PurposeActivate, 48*time.Hour, "")
	if n, _ := st.PruneStalePending(48 * time.Hour); n != 1 { // only "resent" goes now
		t.Fatalf("second prune = %d; want 1", n)
	}
	if _, err := st.GetByEmail("old@example.org"); err != nil {
		t.Fatal("pending user with a live token was pruned")
	}
}

func TestTokenUserDoesNotConsume(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "look@example.org", "Look")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	for i := 0; i < 2; i++ {
		uid, err := st.TokenUser(raw, PurposeActivate)
		if err != nil || uid != u.ID {
			t.Fatalf("TokenUser #%d = %d, %v", i, uid, err)
		}
	}
	if _, err := st.TokenUser(raw, PurposeReset); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("purpose mismatch err = %v", err)
	}
	if _, err := st.TokenUser("not-a-token", PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown token err = %v", err)
	}
	if uid, _, err := st.ConsumeToken(raw, PurposeActivate); err != nil || uid != u.ID {
		t.Fatalf("token burned by TokenUser: %d, %v", uid, err)
	}
	if _, err := st.TokenUser(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("used token err = %v", err)
	}
	exp, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	clk.Advance(time.Hour)
	if _, err := st.TokenUser(exp, PurposeReset); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired err = %v", err)
	}
}

func TestActivateWithTokenActivatesAndBurns(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "act@example.org", "Act")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err := st.ActivateWithToken(raw, u.ID, RoleAdmin, u.PasswordHash); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetByID(u.ID)
	if got.Status != StatusActive || got.Role != RoleAdmin || got.ActivatedAt == nil || got.ActivatedBy != nil {
		t.Fatalf("after ActivateWithToken: %+v", got)
	}
	if _, err := st.TokenUser(raw, PurposeActivate); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("token still usable after activation: %v", err)
	}
}

func TestActivateWithTokenHashChangedIsAccountChanged(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "race@example.org", "Race")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	verified := u.PasswordHash
	if err := st.SetPassword(u.ID, "$argon2id$replaced"); err != nil { // a re-register in between
		t.Fatal(err)
	}
	if err := st.ActivateWithToken(raw, u.ID, RoleUser, verified); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("err = %v, want ErrAccountChanged", err)
	}
	if got, _ := st.GetByID(u.ID); got.Status != StatusPending {
		t.Fatalf("activated with a stale hash: %+v", got)
	}
	if _, err := st.TokenUser(raw, PurposeActivate); err != nil {
		t.Fatalf("token burned although nothing was activated: %v", err)
	}
}

func TestActivateWithTokenNotPendingIsAccountChanged(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "manual@example.org", "Manual")
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err := st.Activate(u.ID, RoleUser, nil); err != nil { // an admin activated in between
		t.Fatal(err)
	}
	if err := st.ActivateWithToken(raw, u.ID, RoleAdmin, u.PasswordHash); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("err = %v, want ErrAccountChanged", err)
	}
	if got, _ := st.GetByID(u.ID); got.Role != RoleUser {
		t.Fatalf("role changed on an active account: %+v", got)
	}
}

func TestActivateWithTokenRejectsBadTokens(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "bad@example.org", "Bad")
	other := mustCreate(t, st, "other@example.org", "Other")
	reset, _ := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if err := st.ActivateWithToken(reset, u.ID, RoleUser, u.PasswordHash); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("reset token: err = %v, want ErrTokenInvalid", err)
	}
	foreign, _ := st.IssueToken(other.ID, PurposeActivate, time.Hour, "")
	if err := st.ActivateWithToken(foreign, u.ID, RoleUser, u.PasswordHash); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("other user's token: err = %v, want ErrTokenInvalid", err)
	}
	raw, _ := st.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	clk.Advance(2 * time.Hour)
	if err := st.ActivateWithToken(raw, u.ID, RoleUser, u.PasswordHash); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token: err = %v, want ErrTokenExpired", err)
	}
	if got, _ := st.GetByID(u.ID); got.Status != StatusPending {
		t.Fatalf("activated by a bad token: %+v", got)
	}
}
