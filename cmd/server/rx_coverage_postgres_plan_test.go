package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// A generic plan estimates an unknown bbox as tiny. With a wide viewport it
// instead visits all 128K receptions through the geo index, rejecting 127936
// rows after fetching them. The first five custom plans hide this regression.
func TestCoverageQueriesKeepValueSpecificPlans(t *testing.T) {
	postgresOnly(t)
	dsn := postgresTestDSN(t)
	writer, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := applyTestSchema(t, writer); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 2000)
	for i := range keys {
		seed := sha256.Sum256([]byte(fmt.Sprintf("corescope-bench/%d/node/%d", 20261008, i)))
		key := ed25519.NewKeyFromSeed(seed[:])
		keys[i] = hex.EncodeToString(key.Public().(ed25519.PublicKey))
	}
	copyTestRows(t, writer, "nodes", []string{"public_key", "name"}, len(keys), func(i int) []any {
		return []any{keys[i], fmt.Sprintf("Synthetic-%04d", i)}
	})
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05")
	copyTestRows(t, writer, "client_receptions", []string{"rx_pubkey", "heard_key", "heard_keylen", "rssi", "snr", "lat", "lon", "rx_at", "ingested_at", "src"}, 128000, func(i int) []any {
		size := []int{32, 8, 3, 2}[i%4]
		var snr, rssi any = 6.5, -95
		if i == 0 {
			snr, rssi = nil, nil
		}
		lat, lon := 20+float64(i%2000)/10000, 30+float64(i%2000)/10000
		if i == 2000 {
			lat, lon = 20.0123456789, 30.07654321
		}
		return []any{keys[(i+100)%len(keys)], keys[i%len(keys)][:size*2], size, rssi, snr, lat, lon, fmt.Sprintf("%s.%09dZ", stamp, i), stamp + "Z", "synthetic"}
	})
	if _, err := writer.Exec("ANALYZE nodes; ANALYZE client_receptions"); err != nil {
		t.Fatal(err)
	}
	db, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Keep this test on one session so pg_prepared_statements observes every
	// execution. Production pools retain the same per-connection plan behavior.
	db.conn.SetMaxOpenConns(1)
	srv := &Server{db: db, cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	for _, tc := range []struct {
		name, pattern string
		query         func(bbox) ([]coverageRow, error)
	}{
		{"node", "%WHERE heard_key IN%", func(b bbox) ([]coverageRow, error) { return srv.queryCoverageRows(keys[0], b) }},
		{"dashboard", "%WHERE lat BETWEEN%", func(b bbox) ([]coverageRow, error) { return srv.queryCoverageFiltered(keys[0], "", 0, b) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var first []coverageRow
			for n := 0; n < 10; n++ {
				rows, err := tc.query(bbox{19, 29, 22, 32})
				if err != nil || len(rows) != 64 {
					t.Fatalf("pass %d: rows=%d, err=%v", n, len(rows), err)
				}
				sort.Slice(rows, func(i, j int) bool { return rows[i].RxAt < rows[j].RxAt })
				if n == 0 {
					first = rows
				} else if !reflect.DeepEqual(rows, first) {
					t.Fatal("repeated coverage query changed its rows")
				}
			}
			if first[0].SNR != nil || first[0].RSSI != nil || first[1].SNR == nil || *first[1].SNR != 6.5 || first[1].RSSI == nil || *first[1].RSSI != -95 || first[1].Lat != 20.0123456789 || first[1].Lon != 30.07654321 {
				t.Fatalf("coverage null/numeric values changed: %+v", first[:2])
			}
			if rows, err := tc.query(bbox{20, 30, 20, 30}); err != nil || len(rows) != 63 {
				t.Fatalf("inclusive narrow bbox: rows=%d, err=%v", len(rows), err)
			}
			if rows, err := tc.query(bbox{0, 0, 1, 1}); err != nil || len(rows) != 0 {
				t.Fatalf("empty bbox: rows=%d, err=%v", len(rows), err)
			}
			var generic int64
			if err := db.conn.QueryRow(`SELECT COALESCE(SUM(generic_plans),0)::bigint FROM pg_prepared_statements
				WHERE statement LIKE $1 AND statement LIKE $2 AND statement LIKE $3`,
				"%SELECT lat, lon, snr, rssi, heard_key, rx_at%", "%FROM client_receptions%", tc.pattern).Scan(&generic); err != nil {
				t.Fatal(err)
			}
			if generic != 0 {
				t.Fatalf("bbox-dependent coverage SELECT reused %d generic plans; broad views must retain value-specific planning", generic)
			}
		})
	}
	rr := serveRxCoverage(srv, "/api/nodes/"+keys[0]+"/rx-coverage?bbox=19,29,22,32&z=14")
	var fc CoverageFeatureCollection
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &fc) != nil {
		t.Fatalf("coverage API status=%d", rr.Code)
	}
	count := 0
	for _, feature := range fc.Features {
		count += feature.Properties.Count
	}
	if count != 64 || fc.MobileReceptions != 64 || fc.MobileClients != 1 {
		t.Fatalf("coverage API totals changed: cells=%d node=%d clients=%d", count, fc.MobileReceptions, fc.MobileClients)
	}
}
