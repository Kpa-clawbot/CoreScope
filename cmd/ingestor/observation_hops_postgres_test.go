package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// The optional accelerator uses SQLite JSON triggers. PostgreSQL must keep
// ingesting with either setting and leave reach queries on their native scan.
func TestPostgresHopIndexConfigurationUsesScanFallback(t *testing.T) {
	if os.Getenv("CORESCOPE_TEST_BACKEND") != "postgres" {
		t.Skip("requires the explicit native PostgreSQL matrix")
	}
	s := newTestStore(t)
	var diagnostic bytes.Buffer
	old := log.Writer()
	log.SetOutput(&diagnostic)
	t.Cleanup(func() { log.SetOutput(old) })
	for _, days := range []int{0, 7, 0} {
		if err := s.ConfigureObservationHops(days); err != nil {
			t.Fatalf("PostgreSQL startup with hopIndexDays=%d: %v", days, err)
		}
		if s.hopIndexDays != 0 {
			t.Fatal("PostgreSQL claimed an enabled SQLite accelerator")
		}
		s.ScheduleObservationHopsBackfill()
		if s.hopBackfillCancel != nil {
			t.Fatal("SQLite hop backfill was scheduled on PostgreSQL")
		}
		if n, err := s.TrimObservationHops(); err != nil || n != 0 {
			t.Fatal("PostgreSQL ran SQLite hop maintenance", n, err)
		}
	}
	if !strings.Contains(diagnostic.String(), "retention.hopIndexDays is SQLite-only; PostgreSQL uses the native reach scan") {
		t.Fatal("unsupported accelerator setting was silently ignored")
	}
	// Maintenance entrypoints enforce the backend independently of startup.
	s.hopIndexDays = 7
	s.ScheduleObservationHopsBackfill()
	if s.hopBackfillCancel != nil {
		t.Fatal("PostgreSQL scheduled SQLite backfill")
	}
	if n, err := s.TrimObservationHops(); err != nil || n != 0 {
		t.Fatal("PostgreSQL entered SQLite trim", n, err)
	}
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal("PostgreSQL entered SQLite backfill", err)
	}
	s.hopIndexDays = 0
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('observation_hops','observation_hops_coverage')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("PostgreSQL acquired a false hop coverage claim", count, err)
	}
	if _, err := s.db.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(1,'00','hop-fixture','raw timestamp')`); err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{`["aB","CD"]`, `{malformed`, ``} {
		if _, err := s.db.Exec(`INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES(1,$1,$2,$3)`, i, path, time.Now().Unix()); err != nil {
			t.Fatal("PostgreSQL ingest fallback rejected raw path", err)
		}
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&count); err != nil || count != 3 {
		t.Fatal("PostgreSQL ingest lost observations", count, err)
	}
}
