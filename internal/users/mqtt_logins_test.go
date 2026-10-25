package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMigrateV7(t *testing.T) {
	st, _ := newTestStore(t)
	if v, err := st.SchemaVersion(); err != nil || v < 7 || v != len(migrations) {
		t.Fatalf("fresh SchemaVersion = %d, %v; want %d (>=7)", v, err, len(migrations))
	}

	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (6)`}
	for _, m := range migrations[:6] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v6 db: %v", err)
		}
	}
	db.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open v6 db: %v", err)
	}
	defer st2.Close()
	if v, err := st2.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	var email string
	if err := st2.db.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil || email != "old@example.org" {
		t.Fatalf("v6 user lost: %q %v", email, err)
	}
	if _, err := st2.db.Exec(`INSERT INTO observer_bindings (pubkey, user_id, bound_at, bound_by) VALUES ('X', 1, 1, 'other')`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("bound_by outside auto/admin accepted: %v", err)
	}
}

func mkLogin(uid int64, name string) MQTTLogin {
	return MQTTLogin{UserID: uid, Username: name, PasswordHash: "hash-" + name, PasswordEnc: "enc-" + name}
}

func TestMQTTLogin_CreateOncePerUser(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")

	if err := st.CreateMQTTLogin(mkLogin(a.ID, "obs-a")); err != nil {
		t.Fatal(err)
	}
	got, err := st.MQTTLoginForUser(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "obs-a" || got.PasswordHash != "hash-obs-a" || got.PasswordEnc != "enc-obs-a" ||
		!got.CreatedAt.Equal(clk.Now()) || got.RevokedAt != nil || got.LastAuthAt != nil {
		t.Fatalf("login = %+v", got)
	}
	if err := st.CreateMQTTLogin(mkLogin(a.ID, "obs-a2")); !errors.Is(err, ErrMQTTLoginExists) {
		t.Fatalf("second create = %v; want ErrMQTTLoginExists", err)
	}
	err = st.CreateMQTTLogin(mkLogin(b.ID, "obs-a"))
	if err == nil || errors.Is(err, ErrMQTTLoginExists) {
		t.Fatalf("duplicate username = %v; want a plain error", err)
	}
	if _, err := st.MQTTLoginForUser(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing login = %v; want ErrNotFound", err)
	}
	all, err := st.AllMQTTLogins()
	if err != nil || len(all) != 1 {
		t.Fatalf("AllMQTTLogins = %v, %v", all, err)
	}
}

func TestMQTTLogin_RevokeAndReissue(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	if err := st.RevokeMQTTLogin(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke without login = %v", err)
	}
	if err := st.CreateMQTTLogin(mkLogin(a.ID, "one")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := st.RevokeMQTTLogin(a.ID); err != nil {
		t.Fatal(err)
	}
	l, _ := st.MQTTLoginForUser(a.ID)
	if l.RevokedAt == nil || !l.RevokedAt.Equal(clk.Now()) {
		t.Fatalf("RevokedAt = %v", l.RevokedAt)
	}
	clk.Advance(time.Hour)
	if err := st.TouchMQTTAuth(a.ID, clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.ReissueMQTTLogin(mkLogin(a.ID, "two")); err != nil {
		t.Fatal(err)
	}
	l, _ = st.MQTTLoginForUser(a.ID)
	if l.Username != "two" || l.PasswordHash != "hash-two" || l.PasswordEnc != "enc-two" || l.RevokedAt != nil ||
		!l.CreatedAt.Equal(clk.Now()) {
		t.Fatalf("after reissue = %+v", l)
	}
	if l.LastAuthAt == nil || !l.LastAuthAt.Equal(clk.Now()) {
		t.Fatalf("LastAuthAt = %v", l.LastAuthAt)
	}
	if err := st.ReissueMQTTLogin(mkLogin(999, "x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reissue without login = %v", err)
	}
}

func TestBindObserver_FirstWins(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ab", 32)

	owner, created, err := st.BindObserver(pk, a.ID, "auto")
	if err != nil || !created || owner == nil || *owner != a.ID {
		t.Fatalf("A bind = %v %v %v", owner, created, err)
	}
	owner, created, err = st.BindObserver(pk, b.ID, "auto")
	if err != nil || created || owner == nil || *owner != a.ID {
		t.Fatalf("B bind = %v %v %v", owner, created, err)
	}
	if _, _, err := st.BindObserver(strings.Repeat("cd", 32), a.ID, "bogus"); err == nil {
		t.Fatal("bad bound_by accepted")
	}
	bs, err := st.ObserverBindings()
	if err != nil || len(bs) != 1 || bs[0].Pubkey != strings.ToUpper(pk) || bs[0].BoundBy != "auto" || *bs[0].UserID != a.ID {
		t.Fatalf("bindings = %+v %v", bs, err)
	}
}

func TestBindObserver_LowercaseNormalised(t *testing.T) {
	low := strings.Repeat("ab", 32)
	got, err := NormalizeObserverKey(" " + low + " ")
	if err != nil || got != strings.Repeat("AB", 32) {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NormalizeObserverKey(low[:63]); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("63 chars = %v", err)
	}
	if _, err := NormalizeObserverKey(strings.Repeat("zz", 32)); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("non-hex = %v", err)
	}
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	if _, _, err := st.BindObserver(low[:63], a.ID, "auto"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bind short = %v", err)
	}
}

func TestDeleteUser_OrphansBindings(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("cd", 32)
	if err := st.CreateMQTTLogin(mkLogin(a.ID, "obs-a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BindObserver(pk, a.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MQTTLoginForUser(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("login survived delete: %v", err)
	}
	bs, err := st.ObserverBindings()
	if err != nil || len(bs) != 1 || bs[0].UserID != nil {
		t.Fatalf("bindings = %+v %v", bs, err)
	}
	owner, created, err := st.BindObserver(pk, b.ID, "auto")
	if err != nil || created || owner != nil {
		t.Fatalf("rebind orphan = %v %v %v", owner, created, err)
	}
}

func TestSetObserverOwner_ReassignAndUnbind(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ef", 32)
	st.BindObserver(pk, a.ID, "auto")

	if err := st.SetObserverOwner(pk, &b.ID); err != nil {
		t.Fatal(err)
	}
	bs, _ := st.ObserverBindingsForUser(b.ID)
	if len(bs) != 1 || bs[0].BoundBy != "admin" {
		t.Fatalf("B bindings = %+v", bs)
	}
	if bs, _ := st.ObserverBindingsForUser(a.ID); len(bs) != 0 {
		t.Fatalf("A bindings = %+v", bs)
	}
	pk2 := strings.Repeat("12", 32)
	if err := st.SetObserverOwner(pk2, &a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetObserverOwner(pk, nil); err != nil {
		t.Fatal(err)
	}
	owner, created, _ := st.BindObserver(pk, a.ID, "auto")
	if !created || owner == nil || *owner != a.ID {
		t.Fatalf("bind after unbind = %v %v", owner, created)
	}
	if err := st.SetObserverOwner("nope", nil); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad key = %v", err)
	}
}
