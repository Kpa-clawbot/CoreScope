package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func TestGetObserverPacketCountsWindowAndIdentity(t *testing.T) {
	db := setupTestDB(t)
	for _, query := range []string{
		`INSERT INTO observers(rowid,id,inactive) VALUES(1,'obs-a',0),(2,'obs-b',1)`,
		`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES(1,'00','counts','2026-01-01')`,
		`INSERT INTO observations(transmission_id,observer_idx,timestamp) VALUES
		 (1,1,100),(1,1,101),(1,1,102),(1,2,101),(1,NULL,101),(1,99,101)`,
	} {
		if _, err := db.conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Strict cutoff and inner-join identity semantics are shared by both engines.
	want := map[string]int{"obs-a": 2, "obs-b": 1}
	if got := db.GetObserverPacketCounts(100); !reflect.DeepEqual(got, want) {
		t.Fatalf("observer counts=%v, want=%v", got, want)
	}
}

// Match the B corpus's cardinalities: 128 observers, 16 observations per
// transmission on average, eight days, and one eighth in the recent window.
// SQL-only data exercises the real endpoint pipeline without a PacketStore.
func observerPlannerFixture(t testing.TB, observations int) (*sql.DB, string, int64, map[string]int) {
	t.Helper()
	if testBackend(t) != dbconfig.SQLite {
		t.Skip("SQLite identity migration and planner fixture")
	}
	path := filepath.Join(t.TempDir(), "observers.db")
	dsn, err := legacy.WriterDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	w.SetMaxOpenConns(1)
	t.Cleanup(func() { w.Close() })
	if err = legacy.ApplyBase(w); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Apply(w, nil); err != nil {
		t.Fatal(err)
	}
	if err = dbschema.EnsureSQLiteObserverTimeIndex(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	epoch := time.Now().Unix()
	tx, err := w.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<128)
		 INSERT INTO observers(rowid,id,name,iata,last_seen,first_seen,packet_count)
		 SELECT i,printf('%064x',i),printf('Synthetic observer %d',i),'AAA','2026-01-01','2026-01-01',0 FROM n`,
		`INSERT INTO nodes(public_key,name,role,lat,lon) SELECT id,name,'repeater',20,30 FROM observers`,
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<%d)
		 INSERT INTO transmissions(id,raw_hex,hash,first_seen) SELECT i,'00',printf('fixture-%%d',i),'2026-01-01' FROM n`, observations/16),
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM n WHERE i+1<%d)
		 INSERT INTO observations(transmission_id,observer_idx,timestamp,path_json)
		 SELECT i/16+1,i%%128+1,CASE WHEN (i/160)%%8=0 THEN %d-2700+(i/16)%%1800
		 ELSE %d-((i/160)%%8)*86400-43200+(i/16)%%3600 END,'[]' FROM n`, observations, epoch, epoch),
	} {
		if _, err = tx.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Exec(`ANALYZE`); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]int)
	for i := 0; i < observations; i++ {
		if (i/160)%8 == 0 {
			want[fmt.Sprintf("%064x", i%128+1)]++
		}
	}
	return w, path, epoch - 3600, want
}

func observerPlannerReader(t testing.TB, path string, since int64, want map[string]int) *DB {
	t.Helper()
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if got := db.GetObserverPacketCounts(since); !reflect.DeepEqual(got, want) {
		t.Fatalf("recent observation counts changed: got %d observer groups, want %d", len(got), len(want))
	}
	return db
}

func observerAggregatePlan(t testing.TB, db *DB, since int64) []string {
	t.Helper()
	rows, err := db.conn.Query("EXPLAIN QUERY PLAN "+db.observerPacketCountsSQL(), since)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestObserverAggregatePlannerAfterSQLiteUpgrade(t *testing.T) {
	for _, repair := range []bool{false, true} {
		name := "legacy adoption"
		if repair {
			name = "already rebuilt without statistics"
		}
		t.Run(name, func(t *testing.T) {
			w, path, since, want := observerPlannerFixture(t, 128000)
			if repair {
				if err := dbschema.ApplySQLite(w, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Exec(`DELETE FROM sqlite_stat1 WHERE tbl='observers'; ANALYZE sqlite_schema`); err != nil {
					t.Fatal(err)
				}
			}
			if err := dbschema.ApplySQLite(w, nil); err != nil {
				t.Fatal(err)
			}
			// A new reader is important: the old writer can retain pre-rebuild
			// planner estimates and conceal the statistics lost by DROP TABLE.
			db := observerPlannerReader(t, path, since, want)
			plan := observerAggregatePlan(t, db, since)
			if strings.Contains(strings.Join(plan, "\n"), "TEMP B-TREE FOR GROUP BY") {
				t.Fatalf("observer statistics lost after upgrade; aggregate sorts the observation window: %q", plan)
			}
			if !strings.Contains(strings.Join(plan, "\n"), "observer_idx=? AND timestamp>?") {
				t.Fatalf("aggregate no longer uses observer/time range lookups: %q", plan)
			}
		})
	}
}

// Run explicitly with -benchtime=50x. Reports uncached response-build latency,
// not full concurrent HTTP latency or a replacement for the paired workload.
func BenchmarkObserverListSQLiteUpgrade(b *testing.B) {
	w, path, since, want := observerPlannerFixture(b, 2048000)
	var reference ObserverListResponse
	for _, stage := range []string{"legacy", "upgraded"} {
		if stage == "upgraded" {
			if err := dbschema.ApplySQLite(w, nil); err != nil {
				b.Fatal(err)
			}
		}
		b.Run(stage, func(b *testing.B) {
			db := observerPlannerReader(b, path, since, want)
			srv := &Server{db: db, cfg: &Config{}}
			b.Logf("observations=2048000 transmissions=128000 observers=128 recent=%d; plan=%q", 256000, observerAggregatePlan(b, db, since))
			response, err := srv.buildObserversDefaultResponse()
			if err != nil || len(response.Observers) != 128 {
				b.Fatalf("response rows=%d err=%v", len(response.Observers), err)
			}
			response.ServerTime = ""
			if stage == "legacy" {
				reference = response
			} else if !reflect.DeepEqual(reference, response) {
				b.Fatal("observer response semantics changed after identity migration")
			}
			durations := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				if _, err := srv.buildObserversDefaultResponse(); err != nil {
					b.Fatal(err)
				}
				durations = append(durations, time.Since(start))
			}
			b.StopTimer()
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(durations[(len(durations)-1)/2])/float64(time.Millisecond), "p50-ms")
			b.ReportMetric(float64(durations[(len(durations)*95+99)/100-1])/float64(time.Millisecond), "p95-ms")
		})
	}
}
