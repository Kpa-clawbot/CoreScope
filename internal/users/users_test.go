package users

import (
	"errors"
	"testing"
)

func mustCreate(t *testing.T, st *Store, email, name string) *User {
	t.Helper()
	u, err := st.CreatePending(email, name, "$argon2id$placeholder")
	if err != nil {
		t.Fatalf("CreatePending(%s): %v", email, err)
	}
	return u
}

func TestCreatePendingAndDuplicate(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "alice@example.org", "Alice")
	if u.ID == 0 || u.Status != StatusPending || u.Role != RoleUser || !u.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("unexpected user: %+v", u)
	}
	if _, err := st.CreatePending("alice@example.org", "Other", "x"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate err = %v; want ErrEmailTaken", err)
	}
	got, err := st.GetByEmail("alice@example.org")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetByEmail = %+v, %v", got, err)
	}
	if _, err := st.GetByID(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID(missing) err = %v", err)
	}
}

func TestActivateOnlyPending(t *testing.T) {
	st, _ := newTestStore(t)
	admin := mustCreate(t, st, "admin@example.org", "Admin")
	u := mustCreate(t, st, "bob@example.org", "Bob")
	by := admin.ID
	if err := st.Activate(u.ID, RoleAdmin, &by); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetByID(u.ID)
	if got.Status != StatusActive || got.Role != RoleAdmin || got.ActivatedAt == nil || got.ActivatedBy == nil || *got.ActivatedBy != admin.ID {
		t.Fatalf("after Activate: %+v", got)
	}
	if err := st.Activate(u.ID, RoleUser, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Activate err = %v; want ErrNotFound", err)
	}
}

func TestSettersAndEmailUniqueness(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "Aa")
	mustCreate(t, st, "b@example.org", "Bb")
	if err := st.SetEmail(a.ID, "b@example.org"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("SetEmail to taken = %v", err)
	}
	if err := st.SetEmail(a.ID, "c@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDisplayName(a.ID, "New name"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(a.ID, "newhash"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(a.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole(a.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailBouncing(a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchLogin(a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetByID(a.ID)
	if got.Email != "c@example.org" || got.DisplayName != "New name" || got.PasswordHash != "newhash" ||
		got.Status != StatusDisabled || got.Role != RoleAdmin || !got.EmailBouncing ||
		got.LastLoginAt == nil || !got.LastLoginAt.Equal(clk.Now()) {
		t.Fatalf("after setters: %+v", got)
	}
	if err := st.SetStatus(9999, StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetStatus(missing) = %v", err)
	}
}

func TestListFiltersAndAdminCount(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "alice@example.org", "Alice")
	b := mustCreate(t, st, "bob@example.org", "Bob_1")
	mustCreate(t, st, "carol@example.org", "Carol")
	st.Activate(a.ID, RoleAdmin, nil)
	st.Activate(b.ID, RoleUser, nil)

	all, err := st.List(ListFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List all = %d, %v", len(all), err)
	}
	pending, _ := st.List(ListFilter{Status: StatusPending})
	if len(pending) != 1 || pending[0].Email != "carol@example.org" {
		t.Fatalf("pending = %+v", pending)
	}
	admins, _ := st.List(ListFilter{Role: RoleAdmin})
	if len(admins) != 1 || admins[0].ID != a.ID {
		t.Fatalf("admins = %+v", admins)
	}
	q, _ := st.List(ListFilter{Query: "BOB"})
	if len(q) != 1 || q[0].ID != b.ID {
		t.Fatalf("query BOB = %+v", q)
	}
	// LIKE wildcards in the query are literal.
	if w, _ := st.List(ListFilter{Query: "_"}); len(w) != 1 || w[0].ID != b.ID {
		t.Fatalf("query _ = %+v", w)
	}
	// Bouncing narrows on the server, combined with the other filters.
	if err := st.SetEmailBouncing(b.ID, true); err != nil {
		t.Fatal(err)
	}
	if bo, err := st.List(ListFilter{Bouncing: true}); err != nil || len(bo) != 1 || bo[0].ID != b.ID {
		t.Fatalf("bouncing = %+v, %v", bo, err)
	}
	if bo, _ := st.List(ListFilter{Bouncing: true, Role: RoleAdmin}); len(bo) != 0 {
		t.Fatalf("bouncing admins = %+v", bo)
	}
	n, err := st.CountActiveAdmins()
	if err != nil || n != 1 {
		t.Fatalf("CountActiveAdmins = %d, %v", n, err)
	}
	st.SetStatus(a.ID, StatusDisabled)
	if n, _ := st.CountActiveAdmins(); n != 0 {
		t.Fatalf("disabled admin still counted: %d", n)
	}
}
