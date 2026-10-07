package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	nPkA = strings.Repeat("a", 64)
	nPkB = strings.Repeat("b", 64)
	nPkC = strings.Repeat("c", 64)
)

func mustPrefs(t *testing.T, st *Store, uid int64) NotifyPrefs {
	t.Helper()
	p, err := st.NotifyPrefsFor(uid)
	if err != nil {
		t.Fatalf("NotifyPrefsFor(%d): %v", uid, err)
	}
	return p
}

func nState(uid int64, event, subject, state string, at time.Time) NotifyState {
	return NotifyState{NotifyKey: NotifyKey{UserID: uid, Event: event, Subject: subject}, State: state, ChangedAt: at}
}

func statesByKey(t *testing.T, st *Store) map[NotifyKey]NotifyState {
	t.Helper()
	list, err := st.AllNotifyStates()
	if err != nil {
		t.Fatal(err)
	}
	out := map[NotifyKey]NotifyState{}
	for _, s := range list {
		out[s.NotifyKey] = s
	}
	return out
}

// A users.db written by a v4 binary gains the notification tables and keeps its rows.
func TestMigrateV4DatabaseToV5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (4)`}
	for _, m := range migrations[:4] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v4 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v4 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	if len(migrations) < 5 {
		t.Fatal("migration v5 missing")
	}
	u, err := st.GetByEmail("old@example.org")
	if err != nil {
		t.Fatalf("v4 user lost: %v", err)
	}
	if p := mustPrefs(t, st, u.ID); !p.Enabled {
		t.Fatalf("prefs after migration = %+v", p)
	}
}

func TestNotifyPrefsDefaultsAndToken(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	p := mustPrefs(t, st, a.ID)
	if !p.Enabled || !reflect.DeepEqual(p.Events, []string{NotifyNodeOffline, NotifyNodeBattery}) ||
		len(p.UnsubToken) != 43 || !p.UpdatedAt.Equal(clk.Now()) {
		t.Fatalf("defaults = %+v", p)
	}
	if again := mustPrefs(t, st, a.ID); again.UnsubToken != p.UnsubToken {
		t.Fatal("a second read issued a new token")
	}
	if other := mustPrefs(t, st, b.ID); other.UnsubToken == p.UnsubToken {
		t.Fatal("two users share an unsubscribe token")
	}
	if _, err := st.NotifyPrefsFor(9999); err == nil {
		t.Fatal("prefs created for a user that does not exist")
	}
	all, err := st.AllNotifyPrefs()
	if err != nil || len(all) != 2 || all[0].UserID != a.ID || all[1].UserID != b.ID {
		t.Fatalf("AllNotifyPrefs = %+v, %v", all, err)
	}
}

func TestNotifyEventHelpers(t *testing.T) {
	for _, e := range []string{NotifyNodeOffline, NotifyNodeBattery, NotifyForeignNew, NotifyObserverOffline} {
		if !ValidNotifyEvent(e) {
			t.Errorf("%s not valid", e)
		}
	}
	if ValidNotifyEvent("node.reboot") || ValidNotifyEvent("") {
		t.Error("unknown event accepted")
	}
	if IsAdminNotifyEvent(NotifyNodeOffline) || !IsAdminNotifyEvent(NotifyForeignNew) || !IsAdminNotifyEvent(NotifyObserverOffline) {
		t.Error("admin event classification wrong")
	}
	p := NotifyPrefs{Events: []string{NotifyNodeBattery}}
	if !p.Has(NotifyNodeBattery) || p.Has(NotifyNodeOffline) {
		t.Error("Has wrong")
	}
}

func TestSetNotifyPrefsCanonicalAndDropsStates(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	tok := mustPrefs(t, st, u.ID).UnsubToken
	if err := st.WriteNotifyStates([]NotifyState{
		nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, clk.Now()),
		nState(u.ID, NotifyNodeBattery, nPkA, NotifyBad, clk.Now()),
	}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	p, err := st.SetNotifyPrefs(u.ID, false, []string{NotifyObserverOffline, NotifyNodeOffline, "bogus", NotifyNodeOffline})
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled || !reflect.DeepEqual(p.Events, []string{NotifyNodeOffline, NotifyObserverOffline}) ||
		p.UnsubToken != tok || !p.UpdatedAt.Equal(clk.Now()) {
		t.Fatalf("after set = %+v", p)
	}
	got := statesByKey(t, st)
	if _, ok := got[NotifyKey{u.ID, NotifyNodeOffline, nPkA}]; !ok || len(got) != 1 {
		t.Fatalf("states after dropping node.battery = %+v", got)
	}
	if _, err := st.SetNotifyPrefs(u.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := statesByKey(t, st); len(got) != 0 {
		t.Fatalf("states with no events chosen = %+v", got)
	}
}

func TestDisableNotifyByToken(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	tok := mustPrefs(t, st, u.ID).UnsubToken
	uid, was, err := st.DisableNotifyByToken(tok)
	if err != nil || uid != u.ID || !was {
		t.Fatalf("first = %d, %v, %v", uid, was, err)
	}
	if p := mustPrefs(t, st, u.ID); p.Enabled {
		t.Fatal("still enabled")
	}
	if uid, was, err = st.DisableNotifyByToken(tok); err != nil || uid != u.ID || was {
		t.Fatalf("second = %d, %v, %v; want the user, wasEnabled false", uid, was, err)
	}
	for _, bad := range []string{"", "nope"} {
		if _, _, err := st.DisableNotifyByToken(bad); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("token %q: err = %v; want ErrTokenInvalid", bad, err)
		}
	}
}

func TestAddWatchesLimitAndDuplicates(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	added, already, over, err := st.AddWatches(u.ID, []string{nPkA, nPkB, nPkC, nPkA}, 2)
	if err != nil || added != 2 || already != 1 || over != 1 {
		t.Fatalf("AddWatches = %d added, %d already, %d over, %v; want 2, 1, 1", added, already, over, err)
	}
	if err := st.AddWatch(u.ID, nPkC, 2); !errors.Is(err, ErrWatchLimit) {
		t.Fatalf("AddWatch over the limit = %v", err)
	}
	if err := st.AddWatch(u.ID, nPkA, 2); err != nil {
		t.Fatalf("AddWatch of a watched node at the limit = %v", err)
	}
	if err := st.AddWatch(u.ID, nPkC, 0); err != nil {
		t.Fatalf("AddWatch without a limit = %v", err)
	}
	ws, err := st.WatchesFor(u.ID)
	if err != nil || len(ws) != 3 || ws[0].Pubkey != nPkA || ws[0].UserID != u.ID || !ws[0].CreatedAt.Equal(clk.Now()) {
		t.Fatalf("WatchesFor = %+v, %v", ws, err)
	}
	watches, watching, err := st.NotifyWatchStats()
	if err != nil || watches != 3 || watching != 1 {
		t.Fatalf("NotifyWatchStats = %d, %d, %v", watches, watching, err)
	}
}

func TestRemoveWatchDeletesItsNodeStates(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	if _, _, _, err := st.AddWatches(u.ID, []string{nPkA, nPkB}, 0); err != nil {
		t.Fatal(err)
	}
	now := clk.Now()
	if err := st.WriteNotifyStates([]NotifyState{
		nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, now), nState(u.ID, NotifyNodeBattery, nPkA, NotifyBad, now),
		nState(u.ID, NotifyNodeOffline, nPkB, NotifyGood, now), nState(u.ID, NotifyObserverOffline, nPkA, NotifyGood, now),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveWatch(u.ID, nPkA); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveWatch(u.ID, nPkA); err != nil {
		t.Fatalf("second remove = %v; want idempotent", err)
	}
	got := statesByKey(t, st)
	_, keepB := got[NotifyKey{u.ID, NotifyNodeOffline, nPkB}]
	_, keepObs := got[NotifyKey{u.ID, NotifyObserverOffline, nPkA}]
	if len(got) != 2 || !keepB || !keepObs {
		t.Fatalf("states after removing the watch = %+v", got)
	}
	all, err := st.AllWatches()
	if err != nil || len(all) != 1 || all[0].Pubkey != nPkB {
		t.Fatalf("AllWatches = %+v, %v", all, err)
	}
}

func TestWriteNotifyStatesUpsertsAndSkipsDeletedUsers(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	gone := mustCreate(t, st, "b@example.org", "B")
	if err := st.Delete(gone.ID); err != nil {
		t.Fatal(err)
	}
	t0 := clk.Now()
	if err := st.WriteNotifyStates([]NotifyState{
		nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, t0),
		nState(gone.ID, NotifyNodeOffline, nPkA, NotifyGood, t0),
	}); err != nil {
		t.Fatalf("write with a deleted user = %v; want the row skipped", err)
	}
	t1 := t0.Add(time.Hour)
	if err := st.WriteNotifyStates([]NotifyState{nState(u.ID, NotifyNodeOffline, nPkA, NotifyBad, t1)}); err != nil {
		t.Fatal(err)
	}
	got := statesByKey(t, st)
	s, ok := got[NotifyKey{u.ID, NotifyNodeOffline, nPkA}]
	if len(got) != 1 || !ok || s.State != NotifyBad || !s.ChangedAt.Equal(t1) {
		t.Fatalf("states = %+v", got)
	}
}

func TestDeleteNotifyStatesRemovesOnlyTheKeys(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	now := clk.Now()
	if err := st.WriteNotifyStates([]NotifyState{
		nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, now),
		nState(u.ID, NotifyForeignNew, "*", NotifyGood, now),
		nState(u.ID, NotifyForeignNew, nPkB, NotifyTold, now),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNotifyStates(nil); err != nil {
		t.Fatalf("empty delete: %v", err)
	}
	if err := st.DeleteNotifyStates([]NotifyKey{
		{u.ID, NotifyForeignNew, "*"},
		{u.ID, NotifyForeignNew, nPkB},
		{u.ID, NotifyObserverOffline, "OBS1"}, // no such row
	}); err != nil {
		t.Fatal(err)
	}
	got := statesByKey(t, st)
	if _, ok := got[NotifyKey{u.ID, NotifyNodeOffline, nPkA}]; len(got) != 1 || !ok {
		t.Fatalf("states after delete = %+v", got)
	}
}

func TestNotifyRowsCascadeOnUserDelete(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "a@example.org", "A")
	mustPrefs(t, st, u.ID)
	if _, _, _, err := st.AddWatches(u.ID, []string{nPkA}, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteNotifyStates([]NotifyState{nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, clk.Now())}); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"notification_prefs", "notification_watches", "notification_state"} {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows after delete = %d, %v", table, n, err)
		}
	}
}

func TestNotifyMailCounts(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	log := func(uid *int64, purpose string) {
		t.Helper()
		if _, err := st.LogMail(uid, "x@example.org", purpose, ""); err != nil {
			t.Fatal(err)
		}
	}
	log(&a.ID, NotifyMailPurpose) // 25 hours before the count: outside the window
	clk.Advance(2 * time.Hour)
	log(&a.ID, NotifyMailPurpose)
	log(&a.ID, NotifyMailPurpose)
	log(&b.ID, NotifyMailPurpose)
	log(nil, NotifyMailPurpose) // account deleted since; still counts for the instance
	log(&a.ID, "activate")
	clk.Advance(23 * time.Hour)
	total, per, err := st.NotifyMailCounts(clk.Now().Add(-24 * time.Hour))
	if err != nil || total != 4 || per[a.ID] != 2 || per[b.ID] != 1 || len(per) != 2 {
		t.Fatalf("NotifyMailCounts = %d, %v, %v; want 4, {a: 2, b: 1}", total, per, err)
	}
}
