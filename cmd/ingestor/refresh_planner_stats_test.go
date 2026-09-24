package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

// #2058: the planner has no cardinality statistics because ANALYZE has never
// run, so it picks a plain index over the partial index built for the query.
// These pin the three things that make the refresh work at all: the statement is
// one that actually writes statistics, the pragma reaches the connection, and a
// negative limit leaves the database untouched.

func hasStat1(t *testing.T, s *Store) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return n > 0
}

// This is the guard against going back to PRAGMA optimize, which is what the
// first draft of this change used. Measured against the 9.4 GB staging database:
// optimize analyzes only tables the calling connection has itself queried during
// the session, so from a maintenance call it writes nothing and sqlite_stat1
// never appears. A fresh store here has queried nothing either, so this test
// fails on that mistake instead of passing on a no-op.
func TestRefreshPlannerStatsWritesStatistics_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if hasStat1(t, s) {
		t.Fatal("a fresh store already carries sqlite_stat1, so this test cannot tell whether the refresh did anything")
	}

	if !s.RefreshPlannerStats(10000) {
		t.Fatal("RefreshPlannerStats reported no refresh")
	}

	if !hasStat1(t, s) {
		t.Error("sqlite_stat1 was not created, so the planner still has no statistics")
	}
}

func TestRefreshPlannerStatsAppliesTheLimit_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.RefreshPlannerStats(250)

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

func TestRefreshPlannerStatsNegativeLimitIsANoop_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if s.RefreshPlannerStats(-1) {
		t.Error("a negative limit must not report a refresh")
	}
	if hasStat1(t, s) {
		t.Error("a negative limit still built sqlite_stat1; the refresh is not actually disabled")
	}
}

func TestRefreshPlannerStatsIsRepeatable_Issue2058(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	// The ticker calls this every 24h for the life of the process. A second
	// call must not error or undo the first, which is the part a single-call
	// test would not notice.
	s.RefreshPlannerStats(10000)
	if !s.RefreshPlannerStats(10000) {
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
		// passed through as 0: that would turn a 2 second refresh into the
		// 242.9s unbounded ANALYZE measured on the staging database.
		{"no db section", &Config{}, 10000},
		{"db section, limit unset", &Config{DB: &dbconfig.DBConfig{}}, 10000},
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

// The default is not a free choice: 400 and 1000 were measured to leave the
// channel-query plan unchanged on the staging database, so a well-meant edit
// back to SQLite's documented 400 would quietly return this to a no-op.
func TestAnalysisLimitDefaultIsHighEnoughToMatter_Issue2058(t *testing.T) {
	const measuredIneffective = 1000
	if got := (&Config{}).AnalysisLimit(); got <= measuredIneffective {
		t.Errorf("default analysis_limit is %d; %d and below were measured to leave the plan unchanged on a 9.4 GB database",
			got, measuredIneffective)
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
