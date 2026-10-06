package users

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "s@example.org", "Sess")
	raw, sess, err := st.CreateSession(u.ID, time.Hour, strings.Repeat("A", 300))
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || sess.CSRFToken == "" || len(sess.UserAgent) != 200 {
		t.Fatalf("CreateSession: raw=%q sess=%+v", raw, sess)
	}
	got, err := st.LookupSession(raw)
	if err != nil || got.ID != sess.ID || got.UserID != u.ID || got.CSRFToken != sess.CSRFToken {
		t.Fatalf("LookupSession = %+v, %v", got, err)
	}
	if _, err := st.LookupSession("not-a-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token err = %v", err)
	}

	clk.Advance(30 * time.Minute)
	if err := st.ExtendSession(sess.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	clk.Advance(45 * time.Minute) // 75 min after creation, 45 after extend
	if _, err := st.LookupSession(raw); err != nil {
		t.Fatalf("extended session expired early: %v", err)
	}
	clk.Advance(16 * time.Minute)
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session err = %v", err)
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("expired session not deleted on lookup: %+v", list)
	}
}

func TestDeleteSessions(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "d@example.org", "Del")
	other := mustCreate(t, st, "o@example.org", "Oth")
	r1, s1, _ := st.CreateSession(u.ID, time.Hour, "a")
	_, s2, _ := st.CreateSession(u.ID, time.Hour, "b")
	_, s3, _ := st.CreateSession(u.ID, time.Hour, "c")
	_, so, _ := st.CreateSession(other.ID, time.Hour, "x")

	if err := st.DeleteSession(other.ID, s2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting someone else's session = %v", err)
	}
	if err := st.DeleteSession(u.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserSessions(u.ID, s1.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListSessions(u.ID)
	if len(list) != 1 || list[0].ID != s1.ID {
		t.Fatalf("after DeleteUserSessions except s1: %+v (s3=%d)", list, s3.ID)
	}
	if err := st.DeleteSessionByToken(r1); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("session survived DeleteSessionByToken: %+v", list)
	}
	if list, _ := st.ListSessions(other.ID); len(list) != 1 || list[0].ID != so.ID {
		t.Fatalf("other user's sessions touched: %+v", list)
	}
	// Deleting a user cascades to their sessions.
	st.Delete(other.ID)
	if list, _ := st.ListSessions(other.ID); len(list) != 0 {
		t.Fatal("sessions survived user delete")
	}
}

func TestPruneExpiredSessions(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "p@example.org", "Pru")
	st.CreateSession(u.ID, time.Hour, "")
	st.CreateSession(u.ID, 3*time.Hour, "")
	clk.Advance(2 * time.Hour)
	n, err := st.PruneExpiredSessions()
	if err != nil || n != 1 {
		t.Fatalf("PruneExpiredSessions = %d, %v", n, err)
	}
}
