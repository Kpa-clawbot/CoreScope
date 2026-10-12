package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsGetWithoutRow(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	doc, v, err := st.GetSettings(u.ID)
	if err != nil || doc != "" || v != (SettingsVersion{}) {
		t.Fatalf("GetSettings = %q, %+v, %v; want \"\", zero, nil", doc, v, err)
	}
}

func TestSettingsPutRevisions(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")

	v1, err := st.PutSettings(u.ID, SettingsVersion{}, `{"v":1,"keys":{"a":"1"}}`)
	if err != nil || v1.Revision != 1 || len(v1.Generation) != 32 {
		t.Fatalf("first write = %+v, %v; want revision 1 and a 32-hex generation", v1, err)
	}
	// A second device still at revision 0 is stale.
	v, err := st.PutSettings(u.ID, SettingsVersion{}, `{"v":1,"keys":{"b":"2"}}`)
	if !errors.Is(err, ErrSettingsConflict) || v != v1 {
		t.Fatalf("stale write = %+v, %v; want %+v, ErrSettingsConflict", v, err, v1)
	}
	// A base ahead of the stored revision is stale too.
	if v, err = st.PutSettings(u.ID, SettingsVersion{Revision: 5, Generation: v1.Generation}, `x`); !errors.Is(err, ErrSettingsConflict) || v != v1 {
		t.Fatalf("future base = %+v, %v; want %+v, ErrSettingsConflict", v, err, v1)
	}
	clk.Advance(time.Minute)
	v2, err := st.PutSettings(u.ID, v1, `{"v":1,"keys":{"c":"3"}}`)
	if err != nil || v2.Revision != 2 || v2.Generation != v1.Generation {
		t.Fatalf("matching write = %+v, %v; want revision 2 in generation %s", v2, err, v1.Generation)
	}
	doc, got, err := st.GetSettings(u.ID)
	if err != nil || got != v2 || doc != `{"v":1,"keys":{"c":"3"}}` {
		t.Fatalf("GetSettings = %q, %+v, %v", doc, got, err)
	}
	var at int64
	if err := st.db.QueryRow(`SELECT updated_at FROM user_settings WHERE user_id = ?`, u.ID).Scan(&at); err != nil || at != unix(clk.Now()) {
		t.Fatalf("updated_at = %d, %v; want %d", at, err, unix(clk.Now()))
	}
}

func TestSettingsWithoutRowNeedBaseZero(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if v, err := st.PutSettings(u.ID, SettingsVersion{Revision: 3, Generation: "old"}, `{}`); !errors.Is(err, ErrSettingsConflict) || v != (SettingsVersion{}) {
		t.Fatalf("PutSettings(base 3, no row) = %+v, %v; want zero, ErrSettingsConflict", v, err)
	}
}

func TestSettingsDelete(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	old, err := st.PutSettings(u.ID, SettingsVersion{}, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatal(err)
	}
	if doc, v, err := st.GetSettings(u.ID); err != nil || doc != "" || v != (SettingsVersion{}) {
		t.Fatalf("after delete = %q, %+v, %v", doc, v, err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// The next write starts a new document in a new generation.
	v, err := st.PutSettings(u.ID, SettingsVersion{Generation: old.Generation}, `{}`)
	if err != nil || v.Revision != 1 || v.Generation == old.Generation || v.Generation == "" {
		t.Fatalf("write after delete = %+v, %v; want revision 1 in a new generation (old %s)", v, err, old.Generation)
	}
}

// After a delete the revisions restart, so a revision alone cannot tell the
// new document from the old one: a device that synced revision 2 of the old
// document must not overwrite revision 2 of the new one.
func TestSettingsStaleGenerationAtSameRevisionConflicts(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	old, _ := st.PutSettings(u.ID, SettingsVersion{}, `{"old":1}`)
	old, _ = st.PutSettings(u.ID, old, `{"old":2}`)
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := st.PutSettings(u.ID, SettingsVersion{}, `{"new":1}`)
	cur, err := st.PutSettings(u.ID, cur, `{"new":2}`)
	if err != nil || cur.Revision != old.Revision {
		t.Fatalf("setup: new document at %+v, %v; want revision %d", cur, err, old.Revision)
	}
	v, err := st.PutSettings(u.ID, old, `{"stale":1}`)
	if !errors.Is(err, ErrSettingsConflict) || v != cur {
		t.Fatalf("stale generation = %+v, %v; want %+v, ErrSettingsConflict", v, err, cur)
	}
	if doc, _, _ := st.GetSettings(u.ID); doc != `{"new":2}` {
		t.Fatalf("document overwritten: %s", doc)
	}
}

func TestSettingsGoWithTheAccount(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, SettingsVersion{}, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM user_settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("user_settings rows after account delete = %d, %v; want 0", n, err)
	}
}

func TestSettingsSurviveDisable(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, SettingsVersion{}, `{"v":1,"keys":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, v, err := st.GetSettings(u.ID); err != nil || v.Revision != 1 {
		t.Fatalf("settings after disable: %+v, %v; want revision 1", v, err)
	}
}

// A users.db written by a v1 binary gains user_settings and keeps its rows.
func TestMigrateV1DatabaseToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
	}, migrations[0]...)
	stmts = append(stmts, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v1 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	u, err := st.GetByEmail("old@example.org")
	if err != nil {
		t.Fatalf("v1 user lost: %v", err)
	}
	if v, err := st.PutSettings(u.ID, SettingsVersion{}, `{}`); err != nil || v.Revision != 1 {
		t.Fatalf("PutSettings on migrated db = %+v, %v", v, err)
	}
}
