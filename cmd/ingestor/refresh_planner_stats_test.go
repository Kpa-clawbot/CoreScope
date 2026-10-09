package main

import (
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"path/filepath"
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
	s := newTestStore(t)
	if _, err := s.db.Exec(`ALTER TABLE transmissions ALTER COLUMN payload_type SET STATISTICS 1000`); err == nil {
		t.Fatal("runtime role changed owner planner policy")
	}
}
