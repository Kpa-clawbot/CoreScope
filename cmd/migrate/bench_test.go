package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seedObservations adds nTx transmissions and nObs observations shaped like
// production rows (10-hop path_json, resolved_path, snr/rssi) to a source
// made by newSource.
func seedObservations(b *testing.B, path string, nTx, nObs int) {
	db := openWriter(b, path)
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	insTx, _ := tx.Prepare(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey, last_seen)
		VALUES (?, ?, ?, '2025-03-01T10:00:00Z', 1, 4, ?, ?, ?)`)
	raw := strings.Repeat("A1B2C3D4", 24)
	for i := 0; i < nTx; i++ {
		id := int64(2_000_000 + i)
		if _, err := insTx.Exec(id, raw, fmt.Sprintf("bench%012d", i), `{"type":"ADVERT","name":"Node `+strconv.Itoa(i)+`","lat":47.1,"lon":8.2}`,
			fmt.Sprintf("%064x", i), 1740823200+int64(i)); err != nil {
			b.Fatal(err)
		}
	}
	insObs, _ := tx.Prepare(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, resolved_path)
		VALUES (?, ?, ?, 'rx', ?, ?, ?, ?, ?, ?)`)
	hops := `["A1","B2","C3","D4","E5","F6","07","18","29","3A"]`
	resolved := `["a1b2c3d4","b2c3d4e5",null,"d4e5f607","e5f60718",null,"07182930","18293a4b","293a4b5c","3a4b5c6d"]`
	for i := 0; i < nObs; i++ {
		if _, err := insObs.Exec(int64(5_000_000+i), int64(2_000_000+i%nTx), []int64{3, 7, 42}[i%3],
			float64(i%40)/4-5, -120+float64(i%60), i%100, hops+strconv.Itoa(i%3), 1740823200+int64(i), resolved); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	db.Close()
}

var copiedLine = regexp.MustCompile(`copied observations\s+(\d+) rows in\s+(\S+) \((\d+) rows/s\)`)

// BenchmarkCopySQLiteToPostgres copies 200k observations (plus 100k
// transmissions) into a fresh Postgres schema per iteration and reports the
// observations COPY rate and the end-to-end time including verify.
//
//	go test -run '^$' -bench CopySQLiteToPostgres -benchtime 3x
func BenchmarkCopySQLiteToPostgres(b *testing.B) {
	src := filepath.Join(b.TempDir(), "bench.db")
	newSource(b, src, srcMigrations())
	seedObservations(b, src, 100_000, 200_000)

	var obsRate, total float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		_, pgURL := pgTarget(b)
		b.StartTimer()
		var buf bytes.Buffer
		start := time.Now()
		if code := runSubcommand("copy", []string{"-from", "sqlite:" + src, "-to", pgURL}, &buf); code != 0 {
			b.Fatalf("copy failed:\n%s", buf.String())
		}
		total += time.Since(start).Seconds()
		m := copiedLine.FindStringSubmatch(buf.String())
		if m == nil {
			b.Fatalf("no observations progress line:\n%s", buf.String())
		}
		r, _ := strconv.ParseFloat(m[3], 64)
		obsRate += r
		b.Logf("%s", m[0])
	}
	b.ReportMetric(obsRate/float64(b.N), "obs_rows/s")
	b.ReportMetric(300_000*float64(b.N)/total, "rows/s_incl_verify")
}

// BenchmarkCopyPostgresToSQLite is the reverse direction: the same 300k rows
// from Postgres into a new SQLite file per iteration.
func BenchmarkCopyPostgresToSQLite(b *testing.B) {
	dir := b.TempDir()
	src := filepath.Join(dir, "bench.db")
	newSource(b, src, srcMigrations())
	seedObservations(b, src, 100_000, 200_000)
	_, pgURL := pgTarget(b)
	var buf bytes.Buffer
	if code := runSubcommand("copy", []string{"-from", "sqlite:" + src, "-to", pgURL}, &buf); code != 0 {
		b.Fatalf("seed copy failed:\n%s", buf.String())
	}

	var obsRate, total float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		start := time.Now()
		dst := filepath.Join(dir, fmt.Sprintf("out%d.db", i))
		if code := runSubcommand("copy", []string{"-from", pgURL, "-to", "sqlite:" + dst}, &buf); code != 0 {
			b.Fatalf("copy failed:\n%s", buf.String())
		}
		total += time.Since(start).Seconds()
		m := copiedLine.FindStringSubmatch(buf.String())
		if m == nil {
			b.Fatalf("no observations progress line:\n%s", buf.String())
		}
		r, _ := strconv.ParseFloat(m[3], 64)
		obsRate += r
		b.Logf("%s", m[0])
	}
	b.ReportMetric(obsRate/float64(b.N), "obs_rows/s")
	b.ReportMetric(300_000*float64(b.N)/total, "rows/s_incl_verify")
}
