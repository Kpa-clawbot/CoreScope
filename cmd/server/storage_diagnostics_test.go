package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestDatabaseDiagnosticsMatchSelectedBackend(t *testing.T) {
	fixture := setupTestDB(t)
	reader, err := openFixtureReader(t, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	s := &Server{db: reader}
	w := httptest.NewRecorder()
	s.handlePerfPostgres(w, httptest.NewRequest("GET", "/api/perf/database", nil))
	var response struct {
		Engine       string   `json:"engine"`
		PageCount    *int64   `json:"pageCount"`
		WalSize      *int64   `json:"walSize"`
		CacheHitRate *float64 `json:"cacheHitRate"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if reader.Backend() == dbconfig.SQLite {
		if response.Engine != "sqlite" || response.PageCount == nil || *response.PageCount <= 0 || response.WalSize == nil {
			t.Fatalf("missing native SQLite diagnostics: %s", w.Body.String())
		}
		if response.CacheHitRate != nil {
			t.Fatal("SQLite must not label PacketStore hits as database cache hits")
		}
		var stats int
		if err := reader.conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='sqlite_stat1'`).Scan(&stats); err != nil || stats != 0 {
			t.Fatal("reader diagnostics attempted planner maintenance", stats, err)
		}
	} else if response.Engine != "postgresql" || response.PageCount != nil || response.WalSize != nil {
		t.Fatalf("PostgreSQL must not publish SQLite counters: %s", w.Body.String())
	}
}
