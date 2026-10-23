package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestSelectedTelemetryRejectsWrongOrUninitializedSchemaWithoutMutation(t *testing.T) {
	for _, query := range []string{`PRAGMA user_version=1`, `CREATE TABLE unrelated(value TEXT); INSERT INTO unrelated VALUES('keep')`} {
		path := filepath.Join(t.TempDir(), "existing.db")
		db, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
		db.Close()
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if s, err := OpenStoreStorage(dbconfig.Storage{Backend: dbconfig.SQLite, DBPath: path}, nil); err == nil {
			s.Close()
			t.Fatal("unrelated or uninitialized store accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("rejected telemetry store changed", err)
		}
	}
}

func TestSelectedTelemetryCannotRecreateRemovedVolume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing-volume")
	if s, err := OpenStoreStorage(dbconfig.Storage{Backend: dbconfig.SQLite, DBPath: filepath.Join(dir, "telemetry.db")}, nil); err == nil {
		s.Close()
		t.Fatal("missing volume accepted")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime recreated missing volume", err)
	}
}

func TestSQLiteTelemetryTargetRemovedAfterDSNValidation(t *testing.T) {
	for _, replaceEmpty := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "telemetry.db")
		seed, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		seed.Close()
		dsn, err := sqliteTelemetryWriterDSN(path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(path, path+".preserved"); err != nil {
			t.Fatal(err)
		}
		if replaceEmpty {
			if err = os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateExistingTelemetry(db); err == nil {
			t.Fatal("raced missing/empty target accepted")
		}
		db.Close()
		info, err := os.Stat(path)
		if replaceEmpty {
			if err != nil || info.Size() != 0 {
				t.Fatal("raced empty file was initialized", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("raced missing file was recreated", err)
		}
	}
}

func TestSelectedTelemetryRetainsRowsSequenceAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.db")
	seed, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = seed.InsertTransmission(&PacketData{Hash: "preserved", RawHex: "AA", Timestamp: "2026-01-01T00:00:00Z", PathJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	if _, err = seed.db.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(900,'AA','retired','2026-01-01T00:00:00Z'); DELETE FROM transmissions WHERE id=900`); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	s, err := OpenStoreStorage(dbconfig.Storage{Backend: dbconfig.SQLite, DBPath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count, sequence int
	if err = s.db.QueryRow(`SELECT count(*) FROM transmissions WHERE hash='preserved'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("stored row lost", count, err)
	}
	if err = s.db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='transmissions'`).Scan(&sequence); err != nil || sequence != 900 {
		t.Fatal("allocator reset", sequence, err)
	}
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1"} {
		var got string
		if err = s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%s: %v", pragma, got, err)
		}
	}
}
