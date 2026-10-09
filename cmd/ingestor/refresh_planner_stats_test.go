package main

import (
	"bytes"
	"github.com/meshcore-analyzer/dbconfig"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestPostgresOwnerAnalyzesTelemetry(t *testing.T) {
	if testBackend(t) != dbconfig.Postgres {
		t.Skip("PostgreSQL planner matrix not selected")
	}
	s := newTestStore(t)
	owner := testAdmin(t, s)
	if _, err := owner.Exec(`INSERT INTO transmissions(raw_hex,hash,first_seen) SELECT 'aa', n::text,'2026-01-01T00:00:00Z' FROM generate_series(1,1000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`ANALYZE transmissions`); err != nil {
		t.Fatal(err)
	}
	var estimate float64
	if err := owner.QueryRow(`SELECT reltuples FROM pg_class WHERE oid='transmissions'::regclass`).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	if estimate != 1000 {
		t.Fatalf("planner row estimate=%v, want 1000", estimate)
	}
}
func TestPostgresRuntimeCannotAlterPlannerTargets(t *testing.T) {
	if testBackend(t) != dbconfig.Postgres {
		t.Skip("PostgreSQL planner matrix not selected")
	}
	s := newTestStore(t)
	if _, err := s.db.Exec(`ALTER TABLE transmissions ALTER COLUMN payload_type SET STATISTICS 1000`); err == nil {
		t.Fatal("runtime role changed owner planner policy")
	}
}

func TestRefreshPlannerStatsWritesStatistics_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

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
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

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
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

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
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

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

func TestEnsurePlannerStatsBuildsWhenAbsent_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	s := newTestStore(t)
	defer s.Close()

	if !s.EnsurePlannerStats(10000) {
		t.Fatal("EnsurePlannerStats did not build statistics on a database that has none")
	}
	if !hasStat1(t, s) {
		t.Error("sqlite_stat1 absent after EnsurePlannerStats")
	}
}

func TestEnsurePlannerStatsSkipsWhenPresent_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	s := newTestStore(t)
	defer s.Close()

	s.RefreshPlannerStats(10000)
	// The point of the skip: on every restart after the first this must cost one
	// sqlite_master query, not an ANALYZE. If it ever returns true here it is
	// running the 2s refresh on every boot.
	if s.EnsurePlannerStats(10000) {
		t.Error("EnsurePlannerStats rebuilt statistics that were already there")
	}
}

func TestEnsurePlannerStatsRespectsDisabled_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	s := newTestStore(t)
	defer s.Close()

	if s.EnsurePlannerStats(-1) {
		t.Error("a negative limit must not build statistics")
	}
	if hasStat1(t, s) {
		t.Error("sqlite_stat1 built despite the refresh being disabled")
	}
}

func TestPlannerStatsSurviveReopen_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	dir := t.TempDir()
	dbPath := dir + "/reopen.db"

	first, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	first.WaitForAsyncMigrations()
	if !first.EnsurePlannerStats(10000) {
		t.Fatal("first store did not build statistics")
	}
	first.Close()

	second, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.WaitForAsyncMigrations()

	if !hasStat1(t, second) {
		t.Fatal("statistics did not survive reopening the database")
	}
	if second.EnsurePlannerStats(10000) {
		t.Error("the reopened store rebuilt statistics, so a restart would pay for an ANALYZE it does not need")
	}
}

func TestEnsurePlannerStatsWarnsBeforeBuilding_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	s := newTestStore(t)
	defer s.Close()

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	s.EnsurePlannerStats(10000)

	out := buf.String()
	for _, want := range []string{"no planner statistics", "write connection", "Once per database"} {
		if !strings.Contains(out, want) {
			t.Errorf("the first-build warning does not mention %q; an operator seeing ingest stall gets no explanation.\ngot: %s", want, out)
		}
	}
	if i, j := strings.Index(out, "no planner statistics"), strings.Index(out, "statistics built in"); i == -1 || j == -1 || i > j {
		t.Errorf("the warning must come before the completion line, so it is visible while the write path is held; got: %s", out)
	}
}

func TestEnsurePlannerStatsIsQuietWhenPresent_Issue2058(t *testing.T) {
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite planner matrix not selected")
	}

	s := newTestStore(t)
	defer s.Close()
	s.RefreshPlannerStats(10000)

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	s.EnsurePlannerStats(10000)

	if out := buf.String(); strings.Contains(out, "no planner statistics") {
		t.Errorf("warned about building on a database that already has statistics: %s", out)
	}
}

func hasStat1(t *testing.T, s *Store) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return n > 0
}
