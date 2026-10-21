package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

// BenchmarkScanReachRows compares the path_json LIKE scan with the
// observation_hops index on a synthetic DB: 11 hops per path on average (the
// real mean is 9.6 to 12.2), a 7-day window inside 14 days of data, one hot
// relay token in about 3% of paths and one rare token in about 0.01%.
//
//	go test ./cmd/server -run xxx -bench ScanReachRows -benchtime 5x
//
// The cold-cache, EBS-bound case that motivated the index is not reproducible
// in a unit benchmark; see the PR description for measurements on real data.
func BenchmarkScanReachRows(b *testing.B) {
	const (
		nObs   = 600_000
		window = 7 * 86400
		span   = 14 * 86400
		now    = int64(1_800_000_000)
	)
	conn, err := sql.Open("sqlite3", "file:"+filepath.Join(b.TempDir(), "r.db")+"?_journal_mode=WAL")
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	for _, s := range []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, from_pubkey TEXT, payload_type INTEGER)`,
		`CREATE TABLE observers (id TEXT, inactive INTEGER)`,
		`INSERT INTO observers (id) VALUES ('OBS1')`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, snr REAL, path_json TEXT, timestamp INTEGER)`,
		`CREATE INDEX idx_observations_timestamp ON observations(timestamp)`,
	} {
		if _, err := conn.Exec(s); err != nil {
			b.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(1))
	tx, _ := conn.Begin()
	for i := 1; i <= nObs; i++ {
		tx.Exec(`INSERT INTO transmissions (id, from_pubkey, payload_type) VALUES (?, 'FF00', 4)`, i)
		p := "["
		for h, n := 0, 1+rng.Intn(21); h < n; h++ {
			if h > 0 {
				p += ","
			}
			tok := fmt.Sprintf("%02X%02X", rng.Intn(256), rng.Intn(256))
			switch r := rng.Intn(10000); {
			case r < 300:
				tok = "HOT1"
			case r < 301:
				tok = "RARE"
			}
			p += `"` + tok + `"`
		}
		p += "]"
		tx.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES (?,?,1,-5,?,?)`,
			i, i, p, now-int64(rng.Intn(span)))
	}
	tx.Commit()
	if err := dbschema.EnsureObservationHopsSchema(conn, nil); err != nil {
		b.Fatal(err)
	}
	// Index everything and claim full coverage, as a finished backfill does.
	conn.Exec(`INSERT OR IGNORE INTO observation_hops SELECT DISTINCT j.value, o.timestamp, o.id FROM observations o, json_each(o.path_json) j`)
	srv := &Server{db: &DB{conn: conn}}
	since := now - window
	for _, tc := range []struct{ name, tok string }{{"hot_token", "HOT1"}, {"rare_token", "RARE"}} {
		for _, indexed := range []bool{false, true} {
			mode := "like"
			if indexed {
				mode = "indexed"
				conn.Exec(`UPDATE observation_hops_coverage SET since = 0`)
			} else {
				conn.Exec(`UPDATE observation_hops_coverage SET since = ?`, now+1) // not covered: LIKE path
			}
			b.Run(tc.name+"/"+mode, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					rows, err := srv.scanReachRows(context.Background(), map[string]bool{tc.tok: true}, since)
					if err != nil {
						b.Fatal(err)
					}
					if len(rows) == 0 {
						b.Fatal("no rows")
					}
				}
			})
		}
	}
}
