package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// coverageBenchSQL is the dominant per-node coverage query (mirrors
// cmd/server queryCoverageRows): a bbox range plus a full-key/2-3-byte-prefix
// match on the heard node.
const coverageBenchSQL = `SELECT lat, lon, snr, rssi, heard_key, rx_at
	FROM client_receptions
	WHERE lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?
	  AND ( (heard_keylen = 32 AND heard_key = ?)
	     OR (heard_keylen IN (2,3) AND substr(?, 1, heard_keylen*2) = heard_key) )`

// BenchmarkCoverageQuery seeds ~1M receptions across a metro-area bbox and times
// the coverage query with the indexes (#5/#18) versus a forced full table scan.
// Run: go test -run x -bench BenchmarkCoverageQuery -benchtime 20x ./cmd/ingestor
func BenchmarkCoverageQuery(b *testing.B) {
	const n = 1_000_000
	const prefixPool = 2000 // distinct 3-byte heard_key prefixes

	dir := b.TempDir()
	s, err := OpenStore(dir + "/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()

	rng := rand.New(rand.NewSource(1))
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO client_receptions
		(rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		hk := fmt.Sprintf("%06x", rng.Intn(prefixPool))
		lat := 51.0 + rng.Float64()*0.4 // ~44 km metro span
		lon := 3.5 + rng.Float64()*0.4
		rxpk := fmt.Sprintf("%064x", rng.Intn(500))
		// rx_at carries i so (rx_pubkey,heard_key,rx_at) stays unique.
		if _, err := stmt.Exec(rxpk, hk, 3, -6.0, lat, lon, fmt.Sprintf("t%d", i), "x", "rxlog"); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	// Target node whose 3-byte prefix (0003e8 = 1000) is in the pool, queried
	// over a sub-bbox of the metro area.
	target := "0003e8" + strings.Repeat("ab", 29) // 6 + 58 = 64 hex

	// OR/substr query (original shape): bbox range OR'd with a non-sargable
	// substr prefix match.
	runOR := func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := s.db.Query(coverageBenchSQL, 51.1, 51.3, 3.6, 3.8, target, target)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
	}

	// IN-list query (sargable): the heard node's candidate keys are exactly the
	// full pubkey and its 2/3-byte prefixes, so an IN-list seeks them via the
	// heard_key-leading composite instead of scanning the bbox.
	inListSQL := `SELECT lat, lon, snr, rssi, heard_key, rx_at FROM client_receptions
		WHERE heard_key IN (?,?,?) AND lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?`
	runIN := func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, err := s.db.Query(inListSQL, target, target[:4], target[:6], 51.1, 51.3, 3.6, 3.8)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
	}

	b.Run("or_query_indexed", runOR)
	b.Run("inlist_query_indexed", runIN)

	// Drop the coverage indexes to measure the full-scan baseline.
	for _, idx := range []string{"idx_client_recept_heard_geo", "idx_client_recept_latlon", "idx_client_recept_rxpk"} {
		if _, err := s.db.Exec("DROP INDEX IF EXISTS " + idx); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("or_query_table_scan", runOR)
}
