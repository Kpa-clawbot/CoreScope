package users

import (
	"database/sql"
	"errors"
	"testing"
	"time"
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
			if got.PasswordHash != u.PasswordHash || got.DisplayName != u.DisplayName || string(got.Status) != state {
				t.Fatal("stale registration changed an account that is no longer pending")
			}
		})
	}
}

// SQLite's writer lock holds activation uncommitted while registration starts.
// Observe the store's only connection in use, then commit the winning activation.
// The UPDATE must test the newly committed status rather than the stale snapshot.
func TestUpdatePendingRegistrationCannotOverwriteConcurrentActivation(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "activation@example.org", "Original")
	var file string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &file); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	activated := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE users SET status='active', role='admin', activated_at=? WHERE id=?`, activated, u.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- st.UpdatePendingRegistration(u.ID, "replacement-hash", "Replacement")
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("registration did not finish after releasing the activation writer")
		}
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for st.db.Stats().InUse == 0 {
		select {
		case err := <-result:
			t.Fatalf("registration returned before activation committed: %v", err)
		case <-deadline.C:
			t.Fatal("registration did not acquire its store connection")
		case <-ticker.C:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAccountChanged) {
			t.Fatalf("concurrent activation error = %v, want ErrAccountChanged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish")
	}
	got, err := st.GetByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive || got.Role != RoleAdmin || got.ActivatedAt == nil || got.ActivatedAt.Unix() != activated ||
		got.PasswordHash != u.PasswordHash || got.DisplayName != u.DisplayName {
		t.Fatal("registration overwrote an account activated after its pending lookup")
	}
}

func TestUpdatePendingRegistrationCompetingRequestsKeepFieldsTogether(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "competing@example.org", "Original")
	start := make(chan struct{})
	result := make(chan error, 2)
	for _, name := range []string{"First", "Second"} {
		go func(name string) {
			<-start
			result <- st.UpdatePendingRegistration(u.ID, name+"-hash", name)
		}(name)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if (got.DisplayName != "First" && got.DisplayName != "Second") ||
		got.PasswordHash != got.DisplayName+"-hash" || got.Status != StatusPending {
		t.Fatal("competing registrations mixed fields from different requests")
	}
}
