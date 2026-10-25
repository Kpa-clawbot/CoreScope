// Run from cmd/migrate:
// go run ../../scripts/prepare-postgres-fixture.go -source ../../test-fixtures/e2e-fixture.db -destination /private/fixture.sqlite
// This explicitly repairs the historical test fixture on a new disposable copy.
// Production duplicates require a reviewed repair/upgrade with the last SQLite
// release while retaining the raw recovery snapshot; the importer never repairs
// or discards incompatible source records silently.
package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func main() {
	source := flag.String("source", "", "closed historical SQLite fixture")
	destination := flag.String("destination", "", "new disposable destination copy (must not exist)")
	flag.Parse()
	if err := prepareFixture(*source, *destination); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func prepareFixture(source, destination string) error {
	if source == "" || destination == "" {
		return errors.New("-source and -destination are required")
	}
	if info, err := os.Stat(source + "-wal"); err == nil && info.Size() > 0 {
		return errors.New("fixture has WAL data; close/checkpoint its writer before preparing a disposable test copy")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	before, err := fileHash(source)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	abs = filepath.ToSlash(abs)
	if runtime.GOOS == "windows" {
		abs = "/" + abs
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"mode": {"rw"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA trusted_schema=OFF`); err != nil {
		return err
	}
	beforeRows, err := counts(db)
	if err != nil {
		return err
	}
	// This step is deliberate and visible in the before/after counts. It is
	// kept outside the production import command and runs only on destination.
	if err := legacy.Apply(db, nil); err != nil {
		return err
	}
	if err := legacy.Normalize(db, nil); err != nil {
		return err
	}
	afterRows, err := counts(db)
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	after, err := fileHash(source)
	if err != nil {
		return err
	}
	if after != before {
		return errors.New("original fixture changed during preparation; discard the disposable destination")
	}
	if info, err := os.Stat(source + "-wal"); err == nil && info.Size() > 0 {
		return errors.New("fixture writer became active during preparation; discard the disposable destination")
	}
	fmt.Printf("transmissions: %d -> %d; observations: %d -> %d; original fixture unchanged\n", beforeRows[0], afterRows[0], beforeRows[1], afterRows[1])
	return nil
}

func counts(db *sql.DB) ([2]int64, error) {
	var result [2]int64
	for i, table := range []string{"transmissions", "observations"} {
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&result[i]); err != nil {
			return result, err
		}
	}
	return result, nil
}

func fileHash(path string) ([32]byte, error) {
	var result [32]byte
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return result, err
	}
	copy(result[:], h.Sum(nil))
	return result, nil
}
