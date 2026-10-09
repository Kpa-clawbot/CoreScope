package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/pgutil/pgtest"
	"github.com/meshcore-analyzer/users"
)

func TestImportRejectsGeneratedAccountColumnsBeforeNormalization(t *testing.T) {
	for _, storage := range []string{"STORED", "VIRTUAL"} {
		for _, column := range []string{"unsupported_note", "last_login_at"} {
			t.Run(storage+"/"+column, func(t *testing.T) {
				source := filepath.Join(t.TempDir(), "users.db")
				db, err := sql.Open("sqlite3", source)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				for _, stmt := range users.LegacyMigrations()[0] {
					if strings.HasPrefix(stmt, "CREATE TABLE users (") {
						if column == "last_login_at" {
							stmt = strings.Replace(stmt, "last_login_at INTEGER,", "last_login_at INTEGER GENERATED ALWAYS AS (created_at + 1) "+storage+",", 1)
						} else {
							stmt = strings.TrimSuffix(strings.TrimSpace(stmt), ")") + ", unsupported_note TEXT GENERATED ALWAYS AS (display_name || ':retained') " + storage + ")"
						}
					}
					if _, err := db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.Exec(`INSERT INTO users(id,email,display_name,password_hash,role,status,created_at) VALUES(42,'synthetic@example.invalid','synthetic','hash','user','active',1)`); err != nil {
					t.Fatal(err)
				}
				var value string
				if err := db.QueryRow(`SELECT ` + quote(column) + ` FROM users WHERE id=42`).Scan(&value); err != nil || value == "" {
					t.Fatal("generated fixture value missing", err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				before, err := sourceFingerprint(source)
				if err != nil {
					t.Fatal(err)
				}
				dsn, state := pgtest.NewSchema(t), t.TempDir()
				report, err := importSQLite(context.Background(), importOptions{Source: source, DatabaseURL: dsn, StateDir: state, Kind: "accounts"})
				if err == nil || !strings.Contains(err.Error(), "generated or hidden") {
					t.Errorf("generated source must be explicitly refused before normalization: verified=%v error=%v", report.Verified, err)
				}
				target := openImportDB(t, dsn)
				var tables int
				if err := target.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema()`).Scan(&tables); err != nil || tables != 0 {
					t.Errorf("unsupported source changed the target: tables=%d error=%v", tables, err)
				}
				if err := users.AssertReady(target); err == nil {
					t.Error("unsupported source became runtime-ready")
				}
				normalized, err := sql.Open("sqlite3", sqliteURL(filepath.Join(state, "accounts", "normalized.sqlite"), "ro"))
				if err != nil {
					t.Fatal(err)
				}
				defer normalized.Close()
				var version int
				if err := normalized.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != 1 {
					t.Errorf("unsupported source reached account normalization: version=%d error=%v", version, err)
				}
				after, err := sourceFingerprint(source)
				if err != nil || before != after {
					t.Fatal("original source changed", err)
				}
			})
		}
	}
}
