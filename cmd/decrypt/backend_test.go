package main

import (
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/channel"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

const testChannel = "#decrypt-test"

// buildGRPTxt returns the raw hex of a FLOOD GRP_TXT packet carrying
// "sender: msg" encrypted for the channel.
func buildGRPTxt(t *testing.T, ch, text string) string {
	t.Helper()
	key := channel.DeriveKey(ch)
	plain := make([]byte, 5, 32)
	binary.LittleEndian.PutUint32(plain, 1_700_000_000)
	plain = append(plain, []byte(text)...)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, 0)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)[:2]
	pkt := []byte{0x15, 0x00, channel.ChannelHash(key), mac[0], mac[1]}
	pkt = append(pkt, ct...)
	return hex.EncodeToString(pkt)
}

// seed inserts two observers and one GRP_TXT transmission with two
// observations. The observers' idx values are 7 and 8 so that idx and id
// (text) can never be confused.
func seed(t *testing.T, db *sql.DB) {
	t.Helper()
	raw := buildGRPTxt(t, testChannel, "Alice: hello world")
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO observers (idx, id, name) VALUES (?, ?, ?)`, []any{7, "OBSID-A", "Observer Alpha"}},
		{`INSERT INTO observers (idx, id, name) VALUES (?, ?, ?)`, []any{8, "OBSID-B", "Observer Beta"}},
		{`INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{1, raw, "hash-1", "2026-04-12T00:00:00Z", 5, `{"path":{"hops":["A3","B0"]}}`}},
		{`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp) VALUES (?, ?, ?, ?, ?)`, []any{1, 7, 9.5, -56.0, 1_700_000_001}},
		{`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, timestamp) VALUES (?, ?, ?, ?, ?)`, []any{1, 8, 4.25, -80.0, 1_700_000_002}},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("seed %q: %v", s.q, err)
		}
	}
}

func runDecrypt(t *testing.T, db *sql.DB) string {
	t.Helper()
	key := channel.DeriveKey(testChannel)
	msgs, total, decrypted, err := decryptChannel(db, testChannel, key, channel.ChannelHash(key))
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || decrypted != 1 {
		t.Fatalf("total=%d decrypted=%d, want 1/1", total, decrypted)
	}
	out, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// seededSQLite builds a SQLite DB file, seeds it, and reopens it through
// openDB (read-only), as the CLI does.
func seededSQLite(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "decrypt.db")
	rw, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(dbschema.SQLiteEndState); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed(t, rw)
	rw.Close()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Regression: observations.observer_idx references observers.idx, but the join
// compared it to observers.id, so observer names were always empty.
func TestObserverNamesResolved(t *testing.T) {
	out := runDecrypt(t, seededSQLite(t))
	for _, want := range []string{"Observer Alpha", "Observer Beta"} {
		if !strings.Contains(out, want) {
			t.Fatalf("observer name %q missing from output:\n%s", want, out)
		}
	}
}

func TestPostgresMatchesSQLite(t *testing.T) {
	want := runDecrypt(t, seededSQLite(t))

	pg := dbxtest.OpenPostgres(t)
	if err := pgschema.Apply(context.Background(), pg, t.Logf); err != nil {
		t.Fatalf("pgschema: %v", err)
	}
	seed(t, pg)
	if got := runDecrypt(t, pg); got != want {
		t.Fatalf("Postgres output differs from SQLite\n--- sqlite\n%s\n--- postgres\n%s", want, got)
	}
}

func TestOpenDBPostgresIsReadOnly(t *testing.T) {
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	db, err := openDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SHOW default_transaction_read_only`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "on" {
		t.Fatalf("default_transaction_read_only = %q, want on", v)
	}
	if _, err := db.Exec(`CREATE TEMP TABLE should_fail (x int)`); err == nil {
		t.Fatal("write succeeded on a read-only connection")
	}
}

func TestOpenDBRedactsPassword(t *testing.T) {
	const pw = "s3cr3tPassw0rd"
	for _, u := range []string{
		"postgres://user:" + pw + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",     // unreachable
		"postgresql://user:" + pw + "@127.0.0.1:notaport/db",                              // unparseable
		"postgres://user@127.0.0.1:1/db?sslmode=disable&connect_timeout=2&password=" + pw, // query password
	} {
		_, err := openDB(u)
		if err == nil {
			t.Fatalf("expected error for %q", u)
		}
		if strings.Contains(err.Error(), pw) {
			t.Fatalf("error leaks password: %v", err)
		}
	}
}

func TestRedactErrQueryPassword(t *testing.T) {
	const pw = "q-s3cret"
	u, err := url.Parse("postgres://user@h/db?password=" + pw)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"dial failed for " + pw, "conninfo host=h password=" + pw + " user=u"} {
		got := redactErr(errors.New(in), u).Error()
		if strings.Contains(got, pw) {
			t.Fatalf("leaks password: %q", got)
		}
	}
}
