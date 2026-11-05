package users

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

var pkConfirm = strings.Repeat("A1", 32)

func mustBinding(t *testing.T, st *Store, pk string) ObserverBinding {
	t.Helper()
	all, err := st.ObserverBindings()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range all {
		if b.Pubkey == pk {
			return b
		}
	}
	t.Fatalf("no binding for %s", pk)
	return ObserverBinding{}
}

func TestMigrationV8ConfirmsAutoBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (7)`}
	for _, m := range migrations[:7] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts,
		`INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`,
		`INSERT INTO observer_bindings (pubkey, user_id, bound_at, bound_by) VALUES ('AUTOKEY', 1, 1234, 'auto')`,
		`INSERT INTO observer_bindings (pubkey, user_id, bound_at, bound_by) VALUES ('ADMINKEY', 1, 5678, 'admin')`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v7 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v7 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != 8 || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want 8", v, err)
	}
	auto, admin := mustBinding(t, st, "AUTOKEY"), mustBinding(t, st, "ADMINKEY")
	if auto.ConfirmedAt == nil || !auto.ConfirmedAt.Equal(auto.BoundAt) {
		t.Fatalf("auto binding = %+v; want ConfirmedAt == BoundAt", auto)
	}
	if admin.ConfirmedAt != nil {
		t.Fatalf("admin binding = %+v; want ConfirmedAt nil", admin)
	}
}

func TestBindObserverAutoConfirms(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	if _, _, err := st.BindObserver(pkConfirm, a.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	b := mustBinding(t, st, pkConfirm)
	if b.ConfirmedAt == nil || !b.ConfirmedAt.Equal(clk.Now()) {
		t.Fatalf("binding = %+v", b)
	}

	pk2 := strings.Repeat("B2", 32)
	if _, _, err := st.BindObserver(pk2, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if b := mustBinding(t, st, pk2); b.ConfirmedAt != nil {
		t.Fatalf("admin bind confirmed: %+v", b)
	}
}

func TestConfirmObserverAdminAssignment(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	if err := st.SetObserverOwner(pkConfirm, &a.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustBinding(t, st, pkConfirm); got.ConfirmedAt != nil {
		t.Fatalf("admin assignment confirmed: %+v", got)
	}
	if ok, err := st.ConfirmObserver(pkConfirm, b.ID); err != nil || ok {
		t.Fatalf("confirm by non-owner = %v, %v; want false", ok, err)
	}
	if got := mustBinding(t, st, pkConfirm); got.ConfirmedAt != nil {
		t.Fatalf("non-owner confirmed: %+v", got)
	}
	if ok, err := st.ConfirmObserver(strings.ToLower(pkConfirm), a.ID); err != nil || !ok {
		t.Fatalf("confirm by owner = %v, %v; want true", ok, err)
	}
	got := mustBinding(t, st, pkConfirm)
	if got.ConfirmedAt == nil || !got.ConfirmedAt.Equal(clk.Now()) {
		t.Fatalf("binding = %+v", got)
	}
	if ok, err := st.ConfirmObserver(pkConfirm, a.ID); err != nil || ok {
		t.Fatalf("second confirm = %v, %v; want false", ok, err)
	}
}

func TestSetObserverOwnerClearsConfirmOnNewOwner(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	if _, _, err := st.BindObserver(pkConfirm, a.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetObserverOwner(pkConfirm, &a.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustBinding(t, st, pkConfirm); got.ConfirmedAt == nil {
		t.Fatalf("same owner cleared confirm: %+v", got)
	}
	if err := st.SetObserverOwner(pkConfirm, &b.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustBinding(t, st, pkConfirm); got.ConfirmedAt != nil || *got.UserID != b.ID {
		t.Fatalf("new owner kept confirm: %+v", got)
	}
	if err := st.SetObserverOwner(pkConfirm, nil); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ObserverBindings()
	if len(all) != 0 {
		t.Fatalf("row not deleted: %+v", all)
	}
}

func TestSetObserverOwnerOrphanGetsNullConfirm(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	if _, _, err := st.BindObserver(pkConfirm, a.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE observer_bindings SET user_id = NULL WHERE pubkey = ?`, pkConfirm); err != nil {
		t.Fatal(err)
	}
	if err := st.SetObserverOwner(pkConfirm, &b.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustBinding(t, st, pkConfirm); got.ConfirmedAt != nil {
		t.Fatalf("orphan reassigned kept confirm: %+v", got)
	}
}
