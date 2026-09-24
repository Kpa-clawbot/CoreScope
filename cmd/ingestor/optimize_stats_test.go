package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

// #2058: the planner has no cardinality statistics because ANALYZE has never
// run, so it picks a plain index over the partial index built for the query.
// These pin the three things that make the refresh work at all: the pragma
// reaches the connection, sqlite_stat1 actually appears, and a negative limit
// leaves the database untouched.

func hasStat1(t *testing.T, s *Store) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return n > 0
}

func TestOptimizeStatsBuildsPlannerStats_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if hasStat1(t, s) {
		t.Fatal("a fresh store already carries sqlite_stat1, so this test cannot tell whether OptimizeStats did anything")
	}

	if !s.OptimizeStats(400) {
		t.Fatal("OptimizeStats reported no refresh")
	}

	// The real assertion. "No error" would also hold if PRAGMA optimize had
	// decided there was nothing worth analyzing, which is the failure mode this
	// catches: on an empty schema SQLite can skip every table.
	if !hasStat1(t, s) {
		t.Error("sqlite_stat1 was not created, so the planner still has no statistics")
	}
}

func TestOptimizeStatsAppliesTheLimit_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.OptimizeStats(250)

	// analysis_limit is per connection. The store runs SetMaxOpenConns(1)
	// (db.go:142), which is the only reason setting it through Exec is sound
	// here: on a multi-connection pool the pragma could land on a connection
	// the ANALYZE never uses, and the limit would silently not apply.
	var limit int
	if err := s.db.QueryRow("PRAGMA analysis_limit").Scan(&limit); err != nil {
		t.Fatalf("read back analysis_limit: %v", err)
	}
	if limit != 250 {
		t.Errorf("analysis_limit did not reach the connection: want 250, got %d", limit)
	}
}

func TestOptimizeStatsNegativeLimitIsANoop_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if s.OptimizeStats(-1) {
		t.Error("a negative limit must not report a refresh")
	}
	if hasStat1(t, s) {
		t.Error("a negative limit still built sqlite_stat1; the refresh is not actually disabled")
	}
}

func TestOptimizeStatsIsRepeatable_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// The ticker calls this every 24h for the life of the process. A second
	// call must not error or undo the first, which is the part a single-call
	// test would not notice.
	s.OptimizeStats(400)
	if !s.OptimizeStats(400) {
		t.Fatal("the second refresh reported failure")
	}
	if !hasStat1(t, s) {
		t.Error("sqlite_stat1 disappeared across two refreshes")
	}
}

func TestAnalysisLimitConfigDefault_Issue2058(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want int
	}{
		// Zero means "no limit" to SQLite, so an unset config must not be
		// passed through as 0: that would turn the bounded refresh into a full
		// ANALYZE of every index.
		{"no db section", &Config{}, 400},
		{"db section, limit unset", &Config{DB: &dbconfig.DBConfig{}}, 400},
		{"explicit limit", &Config{DB: &dbconfig.DBConfig{AnalysisLimit: 1000}}, 1000},
		{"disabled", &Config{DB: &dbconfig.DBConfig{AnalysisLimit: -1}}, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.AnalysisLimit(); got != tc.want {
				t.Errorf("AnalysisLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAnalysisLimitSurvivesTheConfigFile_Issue2058(t *testing.T) {
	// The knob is only useful if it survives the config file. The field lives in
	// internal/dbconfig, so a wrong json tag there would leave the accessor
	// returning the default however the operator set it.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"db":{"analysisLimit":123}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.AnalysisLimit(); got != 123 {
		t.Errorf("analysisLimit did not survive the config file: got %d, want 123", got)
	}
}
