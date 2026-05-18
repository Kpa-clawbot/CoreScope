package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	_ "modernc.org/sqlite"
)

type tableSpec struct {
	name    string
	columns []string
}

var tables = []tableSpec{
	{"nodes", []string{"public_key", "name", "role", "lat", "lon", "last_seen", "first_seen", "advert_count", "battery_mv", "temperature_c", "foreign_advert"}},
	{"inactive_nodes", []string{"public_key", "name", "role", "lat", "lon", "last_seen", "first_seen", "advert_count", "battery_mv", "temperature_c", "foreign_advert"}},
	{"observers", []string{"rowid", "id", "name", "iata", "last_seen", "first_seen", "packet_count", "model", "firmware", "client_version", "radio", "battery_mv", "uptime_secs", "noise_floor", "inactive", "last_packet_at"}},
	{"transmissions", []string{"id", "raw_hex", "hash", "first_seen", "route_type", "payload_type", "payload_version", "decoded_json", "from_pubkey", "channel_hash", "created_at"}},
	{"observations", []string{"id", "transmission_id", "observer_idx", "direction", "snr", "rssi", "score", "path_json", "timestamp", "raw_hex", "resolved_path"}},
	{"observer_metrics", []string{"observer_id", "timestamp", "noise_floor", "tx_air_secs", "rx_air_secs", "recv_errors", "battery_mv", "packets_sent", "packets_recv"}},
	{"dropped_packets", []string{"id", "hash", "raw_hex", "reason", "observer_id", "observer_name", "node_pubkey", "node_name", "dropped_at"}},
	{"neighbor_edges", []string{"node_a", "node_b", "count", "last_seen"}},
	{"_migrations", []string{"name"}},
}

func main() {
	sqlitePath := flag.String("sqlite", "", "source CoreScope SQLite database")
	postgresURL := flag.String("postgres", os.Getenv("DATABASE_URL"), "target Postgres URL")
	truncate := flag.Bool("truncate", false, "truncate target tables before copying")
	flag.Parse()

	if *sqlitePath == "" || *postgresURL == "" {
		log.Fatal("usage: migrate-postgres -sqlite data/meshcore.db -postgres postgres://user:pass@host/db?sslmode=disable [-truncate]")
	}

	warnSQLiteSidecars(*sqlitePath)

	src, err := sql.Open("sqlite", *sqlitePath+"?mode=ro")
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer src.Close()

	dst, err := sql.Open(dbconfig.Settings{Driver: dbconfig.DriverPostgres, URL: *postgresURL}.SQLDriverName(), *postgresURL)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer dst.Close()

	if err := src.Ping(); err != nil {
		log.Fatalf("ping sqlite: %v", err)
	}
	if err := dst.Ping(); err != nil {
		log.Fatalf("ping postgres: %v", err)
	}

	if err := dbconfig.ApplyPostgresSchema(dst); err != nil {
		log.Fatalf("apply postgres schema: %v", err)
	}

	if *truncate {
		if _, err := dst.Exec(`TRUNCATE observations, transmissions, observer_metrics, dropped_packets, neighbor_edges, nodes, inactive_nodes, observers, _migrations RESTART IDENTITY CASCADE`); err != nil {
			log.Fatalf("truncate target: %v", err)
		}
	}

	for _, t := range tables {
		n, err := copyTable(src, dst, t)
		if err != nil {
			log.Fatalf("copy %s: %v", t.name, err)
		}
		log.Printf("copied %-16s %d rows", t.name, n)
	}
	if err := resetSequences(dst); err != nil {
		log.Fatalf("reset sequences: %v", err)
	}
	if err := validateCounts(src, dst); err != nil {
		log.Fatalf("validate: %v", err)
	}
	if err := validateTransmissionHashes(src, dst, 32); err != nil {
		log.Fatalf("validate hashes: %v", err)
	}
	log.Println("migration complete")
}

func warnSQLiteSidecars(sqlitePath string) {
	walPath := sqlitePath + "-wal"
	shmPath := sqlitePath + "-shm"
	if _, err := os.Stat(walPath); err == nil {
		log.Printf("found SQLite WAL sidecar %s; keep it beside %s for hot snapshots", walPath, sqlitePath)
		if _, err := os.Stat(shmPath); os.IsNotExist(err) {
			log.Printf("warning: SQLite WAL sidecar exists but %s is missing; copy meshcore.db, meshcore.db-wal, and meshcore.db-shm together or stop the source container before copying", shmPath)
		}
	}
}

func copyTable(src, dst *sql.DB, spec tableSpec) (int, error) {
	if !sqliteTableExists(src, spec.name) {
		return 0, nil
	}
	cols := existingSQLiteColumns(src, spec.name, spec.columns)
	if len(cols) == 0 {
		return 0, nil
	}
	selectCols := cols
	if spec.name == "observers" && contains(cols, "rowid") {
		selectCols = append([]string{"rowid"}, without(cols, "rowid")...)
	}
	selectExprs := sqliteSelectExpressions(spec.name, selectCols)
	rows, err := src.Query("SELECT " + strings.Join(selectExprs, ", ") + " FROM " + spec.name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := dst.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	placeholders := make([]string, len(selectCols))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	stmt, err := tx.Prepare("INSERT INTO " + spec.name + " (" + strings.Join(selectCols, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ") ON CONFLICT DO NOTHING")
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for rows.Next() {
		values := make([]interface{}, len(selectCols))
		ptrs := make([]interface{}, len(selectCols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return count, err
		}
		for i, v := range values {
			if ts, ok := v.(time.Time); ok {
				values[i] = ts.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
		if _, err := stmt.Exec(values...); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	return count, tx.Commit()
}

func sqliteSelectExpressions(table string, cols []string) []string {
	exprs := make([]string, len(cols))
	copy(exprs, cols)
	if table != "observations" {
		return exprs
	}
	for i, col := range exprs {
		if col == "observer_idx" {
			exprs[i] = `CASE
				WHEN observer_idx IS NULL OR TRIM(CAST(observer_idx AS TEXT)) = '' THEN NULL
				WHEN typeof(observer_idx) IN ('integer', 'real') THEN observer_idx
				ELSE (SELECT rowid FROM observers WHERE observers.id = observations.observer_idx)
			END AS observer_idx`
		}
	}
	return exprs
}

func sqliteTableExists(db *sql.DB, name string) bool {
	var one int
	return db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&one) == nil
}

func existingSQLiteColumns(db *sql.DB, table string, desired []string) []string {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var typ sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk) == nil {
			have[name] = true
		}
	}
	out := make([]string, 0, len(desired))
	for _, c := range desired {
		if c == "rowid" || have[c] {
			out = append(out, c)
		}
	}
	return out
}

func resetSequences(db *sql.DB) error {
	specs := []struct {
		table  string
		column string
	}{
		{"transmissions", "id"},
		{"observations", "id"},
		{"observers", "rowid"},
		{"dropped_packets", "id"},
	}
	for _, spec := range specs {
		stmt := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s','%s'), COALESCE((SELECT MAX(%s) FROM %s), 1), (SELECT MAX(%s) IS NOT NULL FROM %s))`,
			spec.table, spec.column, spec.column, spec.table, spec.column, spec.table,
		)
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func validateCounts(src, dst *sql.DB) error {
	for _, t := range tables {
		if !sqliteTableExists(src, t.name) {
			continue
		}
		var srcCount, dstCount int
		if err := src.QueryRow("SELECT COUNT(*) FROM " + t.name).Scan(&srcCount); err != nil {
			return err
		}
		if err := dst.QueryRow("SELECT COUNT(*) FROM " + t.name).Scan(&dstCount); err != nil {
			return err
		}
		if srcCount != dstCount {
			return fmt.Errorf("%s row count mismatch: sqlite=%d postgres=%d", t.name, srcCount, dstCount)
		}
	}
	return nil
}

type hashSample struct {
	id   int64
	hash string
}

func validateTransmissionHashes(src, dst *sql.DB, limit int) error {
	if limit <= 0 || !sqliteTableExists(src, "transmissions") {
		return nil
	}
	samples, err := transmissionHashSamples(src, limit)
	if err != nil {
		return err
	}
	for _, sample := range samples {
		var dstHash sql.NullString
		if err := dst.QueryRow("SELECT hash FROM transmissions WHERE id=?", sample.id).Scan(&dstHash); err != nil {
			return fmt.Errorf("transmissions id %d hash sample missing in postgres: %w", sample.id, err)
		}
		if !dstHash.Valid || dstHash.String != sample.hash {
			return fmt.Errorf("transmissions id %d hash mismatch: sqlite=%q postgres=%q", sample.id, sample.hash, dstHash.String)
		}
	}
	log.Printf("validated %d transmission hash samples", len(samples))
	return nil
}

func transmissionHashSamples(src *sql.DB, limit int) ([]hashSample, error) {
	seen := make(map[int64]bool)
	samples := make([]hashSample, 0, limit*2)
	queries := []string{
		"SELECT id, hash FROM transmissions WHERE hash IS NOT NULL AND hash <> '' ORDER BY id ASC LIMIT ?",
		"SELECT id, hash FROM transmissions WHERE hash IS NOT NULL AND hash <> '' ORDER BY id DESC LIMIT ?",
	}
	for _, query := range queries {
		rows, err := src.Query(query, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sample hashSample
			if err := rows.Scan(&sample.id, &sample.hash); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[sample.id] {
				seen[sample.id] = true
				samples = append(samples, sample)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return samples, nil
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func without(values []string, remove string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != remove {
			out = append(out, v)
		}
	}
	return out
}
