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
	doc, rev, err := st.GetSettings(u.ID)
	if err != nil || doc != "" || rev != 0 {
		t.Fatalf("GetSettings = %q, %d, %v; want \"\", 0, nil", doc, rev, err)
	}
}

func TestSettingsPutRevisions(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")

	rev, err := st.PutSettings(u.ID, 0, `{"v":1,"keys":{"a":"1"}}`)
	if err != nil || rev != 1 {
		t.Fatalf("first write = %d, %v; want 1, nil", rev, err)
	}
	// A second device still at revision 0 is stale.
	rev, err = st.PutSettings(u.ID, 0, `{"v":1,"keys":{"b":"2"}}`)
	if !errors.Is(err, ErrSettingsConflict) || rev != 1 {
		t.Fatalf("stale write = %d, %v; want 1, ErrSettingsConflict", rev, err)
	}
	// A base ahead of the stored revision is stale too.
	if rev, err = st.PutSettings(u.ID, 5, `x`); !errors.Is(err, ErrSettingsConflict) || rev != 1 {
		t.Fatalf("future base = %d, %v; want 1, ErrSettingsConflict", rev, err)
	}
	clk.Advance(time.Minute)
	rev, err = st.PutSettings(u.ID, 1, `{"v":1,"keys":{"c":"3"}}`)
	if err != nil || rev != 2 {
		t.Fatalf("matching write = %d, %v; want 2, nil", rev, err)
	}
	doc, got, err := st.GetSettings(u.ID)
	if err != nil || got != 2 || doc != `{"v":1,"keys":{"c":"3"}}` {
		t.Fatalf("GetSettings = %q, %d, %v", doc, got, err)
	}
	var at int64
	if err := st.db.QueryRow(`SELECT updated_at FROM user_settings WHERE user_id = ?`, u.ID).Scan(&at); err != nil || at != unix(clk.Now()) {
		t.Fatalf("updated_at = %d, %v; want %d", at, err, unix(clk.Now()))
	}
}

func TestSettingsWithoutRowNeedBaseZero(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if rev, err := st.PutSettings(u.ID, 3, `{}`); !errors.Is(err, ErrSettingsConflict) || rev != 0 {
		t.Fatalf("PutSettings(base 3, no row) = %d, %v; want 0, ErrSettingsConflict", rev, err)
	}
}

func TestSettingsDelete(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, 0, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatal(err)
	}
	if doc, rev, err := st.GetSettings(u.ID); err != nil || doc != "" || rev != 0 {
		t.Fatalf("after delete = %q, %d, %v", doc, rev, err)
	}
	if err := st.DeleteSettings(u.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// The next write starts a new document.
	if rev, err := st.PutSettings(u.ID, 0, `{}`); err != nil || rev != 1 {
		t.Fatalf("write after delete = %d, %v; want 1, nil", rev, err)
	}
}

func TestSettingsGoWithTheAccount(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "sam@example.org", "Sam")
	if _, err := st.PutSettings(u.ID, 0, `{}`); err != nil {
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
	if _, err := st.PutSettings(u.ID, 0, `{"v":1,"keys":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, rev, err := st.GetSettings(u.ID); err != nil || rev != 1 {
		t.Fatalf("settings after disable: rev %d, %v; want 1", rev, err)
	}
}

// A users.db written by a v1 binary gains user_settings and keeps its rows.
func TestMigrateV1DatabaseToV2(t *testing.T) {
	if len(migrations) != 2 {
		t.Fatalf("len(migrations) = %d; this test pins the v1 to v2 step", len(migrations))
	}
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
	if v, err := st.SchemaVersion(); err != nil || v != 2 {
		t.Fatalf("SchemaVersion = %d, %v; want 2", v, err)
	}
	u, err := st.GetByEmail("old@example.org")
	if err != nil {
		t.Fatalf("v1 user lost: %v", err)
	}
	if rev, err := st.PutSettings(u.ID, 0, `{}`); err != nil || rev != 1 {
		t.Fatalf("PutSettings on migrated db = %d, %v", rev, err)
	}
}
