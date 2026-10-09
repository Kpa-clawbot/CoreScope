package main

import (
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresConfigurationPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, generic, writer, want string }{
		{"config", "", "", "postgres://configured@localhost/telemetry"},
		{"generic", "postgres://generic@localhost/telemetry", "", "postgres://generic@localhost/telemetry"},
		{"writer", "postgres://generic@localhost/telemetry", "postgres://writer@localhost/telemetry", "postgres://writer@localhost/telemetry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CORESCOPE_DATABASE_URL", tc.generic)
			t.Setenv("CORESCOPE_WRITER_DATABASE_URL", tc.writer)
			t.Setenv("CORESCOPE_STATE_DIR", "private-state")
			t.Setenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL", "postgres://channels@localhost/accounts")
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(`{"databaseURL":"postgres://configured@localhost/telemetry","dbPath":"legacy.db"}`), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DatabaseURL != tc.want || cfg.StateDir != "private-state" || cfg.ApprovedChannelsURL() != "postgres://channels@localhost/accounts" {
				t.Fatal("PostgreSQL overrides did not resolve correctly")
			}
		})
	}
}

func TestSelectedPostgresCannotFallBackToSQLiteFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "database.db")
	if s, err := OpenStoreStorage(dbconfig.Storage{Backend: dbconfig.Postgres, DBPath: path}, nil); err == nil {
		s.Close()
		t.Fatal("missing PostgreSQL URL fell back to SQLite")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("legacy path created a directory")
	}
}

func TestOpenStoreConnectionErrorDoesNotLeakCredentials(t *testing.T) {
	_, err := OpenStore("postgres://reader:do-not-log-this@localhost:1/test?sslmode=disable")
	if err == nil {
		t.Fatal("closed port unexpectedly connected")
	}
	if strings.Contains(err.Error(), "do-not-log-this") || strings.Contains(err.Error(), "postgres://") {
		t.Fatal("connection error leaked credentials")
	}
}

func TestStatsDefaultUsesStateDirectory(t *testing.T) {
	t.Setenv("CORESCOPE_INGESTOR_STATS", "")
	t.Setenv("CORESCOPE_STATE_DIR", "private-state")
	if got := statsFilePath(); got != filepath.Join("private-state", "ingestor-stats.json") {
		t.Fatalf("stats path %q", got)
	}
}
