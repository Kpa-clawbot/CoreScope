package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestUsersDBReadOnlyDSNRefusesInjection(t *testing.T) {
	for _, p := range []string{"/d/users.db?mode=rwc&x=", "/d/u#.db", "/d/u\x00.db", ""} {
		if dsn, err := usersDBReadOnlyDSN(p); err == nil {
			t.Fatalf("path %q accepted: %s", p, dsn)
		}
	}
}

func TestUsersDBReadOnlyDSNRoundTripsAwkwardPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my data %41 dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "users 100%.db")
	w, err := sql.Open("sqlite3", "file:"+strings.NewReplacer("%", "%25", " ", "%20").Replace(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	w.Close()

	dsn, err := usersDBReadOnlyDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(dsn, "?") != 1 || !strings.HasSuffix(dsn, "?mode=ro&_busy_timeout=5000") {
		t.Fatalf("unexpected DSN %q", dsn)
	}
	r, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var x int
	if err := r.QueryRow(`SELECT x FROM t`).Scan(&x); err != nil || x != 7 {
		t.Fatalf("read through DSN %q: %d, %v", dsn, x, err)
	}
	if _, err := r.Exec(`INSERT INTO t VALUES (8)`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("write must fail read-only, got %v", err)
	}
}

func TestReadRegularFile(t *testing.T) {
	dir := t.TempDir()
	normal := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(normal, []byte(`{"a":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := readRegularFile(normal, 1024); err != nil || string(data) != `{"a":"b"}` {
		t.Fatalf("normal: %q, %v", data, err)
	}

	if _, err := readRegularFile(filepath.Join(dir, "absent"), 1024); !os.IsNotExist(err) {
		t.Fatalf("absent: want IsNotExist, got %v", err)
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(normal, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(link, 1024); err == nil {
		t.Fatal("symlink was followed")
	}

	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(big, 1024); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize: %v", err)
	}

	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := readRegularFile(fifo, 1024); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readRegularFile blocked on a FIFO")
	}
}
