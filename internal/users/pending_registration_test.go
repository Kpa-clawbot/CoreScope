package users

import (
	"errors"
	"testing"
)

func TestUpdatePendingRegistrationUpdatesBothFields(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "pending@example.org", "Original")
	if err := st.UpdatePendingRegistration(u.ID, "replacement-hash", "Replacement"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "replacement-hash" || got.DisplayName != "Replacement" || got.Status != StatusPending {
		t.Fatal("pending registration was not updated together")
	}
}

// The caller holds a stale pending snapshot after activation/disable/deletion.
// Both SQLite and PostgreSQL must evaluate status at the write, not rely on it.
func TestUpdatePendingRegistrationRejectsChangedAccount(t *testing.T) {
	for _, state := range []string{"active", "disabled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			st, _ := newTestStore(t)
			u := mustCreate(t, st, "changed@example.org", "Original")
			if err := st.Activate(u.ID, RoleUser, nil); err != nil {
				t.Fatal(err)
			}
			if state == "disabled" {
				if err := st.SetStatus(u.ID, StatusDisabled); err != nil {
					t.Fatal(err)
				}
			}
			if state == "deleted" {
				if err := st.Delete(u.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.UpdatePendingRegistration(u.ID, "replacement-hash", "Replacement"); !errors.Is(err, ErrAccountChanged) {
				t.Fatalf("stale registration error = %v, want ErrAccountChanged", err)
			}
			got, err := st.GetByID(u.ID)
			if state == "deleted" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("deleted account lookup = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.PasswordHash != u.PasswordHash || got.DisplayName != u.DisplayName {
				t.Fatal("stale registration changed an account that is no longer pending")
			}
		})
	}
}
