// migrate-fixture-hashes updates only an explicit disposable legacy SQLite fixture.
// Run from cmd/migrate before offline PostgreSQL import. It shares the runtime
// packet identity algorithm and refuses collisions instead of dropping records.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/packetpath"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: migrate-fixture-hashes <disposable-sqlite-fixture>")
	}
	if err := migrateFixtureHashes(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}

func migrateFixtureHashes(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("fixture must be an existing regular file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absolute = filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" {
		absolute = "/" + absolute
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{"mode": {"rw"}, "_foreign_keys": {"on"}, "_synchronous": {"FULL"}}
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
	rows, err := db.Query(`SELECT id,raw_hex,hash FROM transmissions ORDER BY id LIMIT 10001`)
	if err != nil {
		return err
	}
	type update struct {
		id   int64
		hash string
	}
	var updates []update
	seen := make(map[string]int64)
	count := 0
	for rows.Next() {
		var id int64
		var raw, old string
		if err := rows.Scan(&id, &raw, &old); err != nil {
			rows.Close()
			return err
		}
		count++
		if count > 10000 {
			rows.Close()
			return fmt.Errorf("fixture exceeds 10000 transmissions; this is not a production repair tool")
		}
		hash := packetpath.ContentHash(raw)
		if previous, exists := seen[hash]; exists {
			rows.Close()
			return fmt.Errorf("content-hash collision between fixture IDs %d and %d requires explicit review; no rows changed", previous, id)
		}
		seen[hash] = id
		if hash != old {
			updates = append(updates, update{id, hash})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE transmissions SET hash=? WHERE id=?`, u.hash, u.id); err != nil {
			return fmt.Errorf("fixture hash update failed; no changes committed: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("Updated %d fixture hashes; preserved %d transmissions and every observation.\n", len(updates), count)
	return nil
}
