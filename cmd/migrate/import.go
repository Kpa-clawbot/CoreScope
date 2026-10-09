package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

type importOptions struct {
	Source, DatabaseURL, StateDir, Kind string
	BatchSize                           int
	Resume                              bool
	afterBatch                          func() error
	targetDigest                        string
}
type tableReport struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}
type importReport struct {
	Kind     string        `json:"kind"`
	Verified bool          `json:"verified"`
	Tables   []tableReport `json:"tables"`
}
type sourceManifest struct {
	Version          int    `json:"version"`
	RunID            string `json:"run_id"`
	Kind             string `json:"kind"`
	SourceDigest     string `json:"source_digest"`
	RawDigest        string `json:"raw_digest"`
	NormalizedDigest string `json:"normalized_digest"`
	TargetDigest     string `json:"target_digest"`
}

const importerLock int64 = 7289041410

const importDestinationObjects = `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relkind IN ('r','p','v','m','S','f')`

func fingerprint(ctx context.Context, path string) (string, error) {
	fp, err := sourceFingerprintContext(ctx, path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x", fp[0], fp[1]), nil
}

func prepareSource(ctx context.Context, o importOptions, tables []importTable, allowNew bool) (sourceManifest, string, error) {
	var manifest sourceManifest
	if o.Source == "" || o.StateDir == "" {
		return manifest, "", errors.New("source and state directory are required for import")
	}
	digest, err := fingerprint(ctx, o.Source)
	if err != nil {
		return manifest, "", err
	}
	dir := filepath.Join(o.StateDir, o.Kind)
	raw := filepath.Join(dir, "recovery.sqlite")
	normalized := filepath.Join(dir, "normalized.sqlite")
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err == nil {
		if !o.Resume {
			return manifest, "", errors.New("migration state already exists; use -resume with the same source and destination")
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return manifest, "", errors.New("invalid migration state manifest")
		}
		if manifest.Version != 1 || manifest.Kind != o.Kind || manifest.SourceDigest != digest || manifest.TargetDigest != o.targetDigest {
			return manifest, "", errors.New("migration resume source or destination does not match the immutable manifest")
		}
		for p, want := range map[string]string{raw: manifest.RawDigest, normalized: manifest.NormalizedDigest} {
			got, err := fingerprint(ctx, p)
			if err != nil {
				return manifest, "", err
			}
			if got != want {
				return manifest, "", errors.New("migration recovery or normalized snapshot changed; resume refused")
			}
		}
		return manifest, normalized, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest, "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return manifest, "", err
	}
	if len(entries) != 0 {
		return manifest, "", errors.New("preparation stopped before a complete manifest; preserve this state directory and the original SQLite files, then use a new state directory for this store only after confirming its PostgreSQL destination is empty")
	}
	if !allowNew {
		return manifest, "", errors.New("no complete migration state manifest exists to resume")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return manifest, "", err
	}
	if err := snapshotSource(ctx, o.Source, raw); err != nil {
		return manifest, "", err
	}
	if err := snapshotSource(ctx, raw, normalized); err != nil {
		return manifest, "", err
	}
	db, err := sql.Open(importSQLiteDriver, sqliteURL(normalized, "rw"))
	if err != nil {
		return manifest, "", err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA trusted_schema=OFF`); err == nil {
		err = normalizeSource(db, o.Kind, raw, tables)
	}
	closeErr := db.Close()
	if err != nil {
		return manifest, "", err
	}
	if closeErr != nil {
		return manifest, "", closeErr
	}
	manifest = sourceManifest{Version: 1, Kind: o.Kind, SourceDigest: digest, TargetDigest: o.targetDigest}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return manifest, "", err
	}
	manifest.RunID = hex.EncodeToString(random[:])
	if manifest.RawDigest, err = fingerprint(ctx, raw); err != nil {
		return manifest, "", err
	}
	if manifest.NormalizedDigest, err = fingerprint(ctx, normalized); err != nil {
		return manifest, "", err
	}
	after, err := fingerprint(ctx, o.Source)
	if err != nil {
		return manifest, "", err
	}
	if after != digest {
		return manifest, "", errors.New("SQLite source changed during preparation; keep every instance writer stopped")
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return manifest, "", err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr = f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return manifest, "", err
	}
	return manifest, normalized, nil
}

func importSQLite(ctx context.Context, o importOptions) (importReport, error) {
	report := importReport{Kind: o.Kind}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if o.BatchSize == 0 {
		o.BatchSize = 1000
	}
	if o.BatchSize < 1 || o.BatchSize > 10000 {
		return report, errors.New("import batch size must be between 1 and 10000")
	}
	tables, err := layouts(o.Kind)
	if err != nil {
		return report, err
	}
	config, err := pgutil.ParseConfig(o.DatabaseURL)
	if err != nil {
		return report, err
	}
	owner, err := pgutil.Open(o.DatabaseURL, false)
	if err != nil {
		return report, err
	}
	defer owner.Close()
	owner.SetMaxOpenConns(1)
	var database, schema string
	var databaseOID, schemaOID int64
	if err := owner.QueryRowContext(ctx, `SELECT current_database(),current_schema(),
 (SELECT oid::bigint FROM pg_database WHERE datname=current_database()),
 (SELECT oid::bigint FROM pg_namespace WHERE nspname=current_schema())`).Scan(&database, &schema, &databaseOID, &schemaOID); err != nil {
		return report, err
	}
	// Container IPs can change while their persistent volume and configured DNS
	// endpoint remain the same. Bind resume to that endpoint and logical OIDs;
	// the destination's random import run ID is verified separately below.
	identity, _ := json.Marshal([]any{database, schema, databaseOID, schemaOID, config.Host, config.Port})
	identityHash := sha256.Sum256(identity)
	o.targetDigest = hex.EncodeToString(identityHash[:])
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return report, errors.New("connect to PostgreSQL import destination failed")
	}
	defer conn.Close(context.Background())
	// One importer owns this database until completion/cancellation. Session lock
	// works across committed COPY batches; closing the connection always releases it.
	if err := lockImport(ctx, conn); err != nil {
		return report, err
	}
	allowNew := !o.Resume
	if o.Resume {
		var hasImport bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('corescope_import') IS NOT NULL`).Scan(&hasImport); err != nil {
			return report, err
		}
		if !hasImport {
			var occupied int
			if err := conn.QueryRow(ctx, importDestinationObjects).Scan(&occupied); err != nil {
				return report, err
			}
			if occupied != 0 {
				return report, errors.New("resume destination has no import marker and is not empty; existing data was not changed")
			}
			allowNew = true
		}
	}
	manifest, path, err := prepareSource(ctx, o, tables, allowNew)
	if err != nil {
		return report, err
	}
	source, err := sql.Open(importSQLiteDriver, sqliteURL(path, "ro"))
	if err != nil {
		return report, err
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	if _, err := source.ExecContext(ctx, `PRAGMA trusted_schema=OFF`); err != nil {
		return report, err
	}

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('corescope_import') IS NOT NULL`).Scan(&exists); err != nil {
		return report, err
	}
	if !exists {
		var occupied int
		if err := conn.QueryRow(ctx, importDestinationObjects).Scan(&occupied); err != nil {
			return report, err
		}
		if occupied != 0 {
			return report, errors.New("import destination is not empty; existing data was not changed")
		}
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		defer tx.Rollback()
		if o.Kind == "telemetry" {
			err = dbschema.ApplyForImportTx(tx, nil)
		} else {
			err = users.ApplyForImportTx(tx)
		}
		if err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE corescope_import (id INTEGER PRIMARY KEY CHECK(id=1),run_id TEXT NOT NULL,kind TEXT NOT NULL,source_digest TEXT NOT NULL,normalized_digest TEXT NOT NULL,verified BOOLEAN NOT NULL DEFAULT false,report TEXT NOT NULL DEFAULT '')
   ;CREATE TABLE corescope_import_progress (table_name TEXT PRIMARY KEY,rows_copied BIGINT NOT NULL,sha256 TEXT NOT NULL,complete BOOLEAN NOT NULL)`); err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO corescope_import(id,run_id,kind,source_digest,normalized_digest) VALUES(1,$1,$2,$3,$4)`, manifest.RunID, o.Kind, manifest.SourceDigest, manifest.NormalizedDigest); err != nil {
			return report, err
		}
		if err := tx.Commit(); err != nil {
			return report, err
		}
	} else if !o.Resume {
		return report, errors.New("destination has an import in progress; use -resume")
	}
	var runID, kind, sourceDigest, normalizedDigest string
	if err := conn.QueryRow(ctx, `SELECT run_id,kind,source_digest,normalized_digest FROM corescope_import WHERE id=1`).Scan(&runID, &kind, &sourceDigest, &normalizedDigest); err != nil {
		return report, err
	}
	if runID != manifest.RunID || kind != o.Kind || sourceDigest != manifest.SourceDigest || normalizedDigest != manifest.NormalizedDigest {
		return report, errors.New("destination import identity does not match this source/state directory")
	}
	var version int
	var ready bool
	if err := conn.QueryRow(ctx, `SELECT version,ready FROM corescope_schema WHERE kind=$1`, o.Kind).Scan(&version, &ready); err != nil {
		return report, err
	}
	if version != 1 {
		return report, errors.New("unsupported PostgreSQL schema version for import")
	}
	var recordedReport importReport
	if ready {
		// A process can stop between the two databases' readiness commits. Keep
		// this completed store unchanged, but never trust readiness by itself.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return report, err
		}
		defer tx.Rollback(context.Background())
		lockTables := []string{"corescope_import", "corescope_import_progress", "corescope_schema"}
		for _, table := range tables {
			lockTables = append(lockTables, quote(table.Name))
		}
		if _, err := tx.Exec(ctx, `LOCK TABLE `+strings.Join(lockTables, ",")+` IN SHARE MODE`); err != nil {
			return report, err
		}
		var verified bool
		var reportText string
		if err := tx.QueryRow(ctx, `SELECT verified,report FROM corescope_import WHERE id=1`).Scan(&verified, &reportText); err != nil {
			return report, err
		}
		if !verified || json.Unmarshal([]byte(reportText), &recordedReport) != nil || !recordedReport.Verified || recordedReport.Kind != o.Kind || len(recordedReport.Tables) != len(tables) {
			return report, errors.New("ready destination has no complete verified import report; resume refused")
		}
	}
	for _, table := range tables {
		got, err := copyTable(ctx, source, owner, conn, table, o, ready)
		if err != nil {
			return report, fmt.Errorf("import %s: %w", table.Name, err)
		}
		report.Tables = append(report.Tables, got)
	}
	if err := reseed(ctx, source, conn, tables, o.Kind, ready); err != nil {
		return report, err
	}
	// COPY does not populate planner statistics. Prepare only this store's
	// imported tables before declaring verification complete; the cost belongs
	// to migration timing, never to the steady-state ingestion benchmark.
	if !ready {
		for _, table := range tables {
			if _, err := conn.Exec(ctx, `ANALYZE `+quote(table.Name)); err != nil {
				return report, fmt.Errorf("analyze imported %s: %w", table.Name, err)
			}
		}
	}
	// Source stays frozen through verification, not merely while snapshotting.
	current, err := fingerprint(ctx, o.Source)
	if err != nil {
		return report, err
	}
	if current != manifest.SourceDigest {
		return report, errors.New("original SQLite source changed during import; keep services stopped")
	}
	normalized, err := fingerprint(ctx, path)
	if err != nil {
		return report, err
	}
	if normalized != manifest.NormalizedDigest {
		return report, errors.New("normalized snapshot changed during import")
	}
	report.Verified = true
	if ready {
		if !slices.Equal(report.Tables, recordedReport.Tables) {
			return report, errors.New("ready destination differs from its verified import report; resume refused")
		}
		return report, nil
	}
	data, err := json.Marshal(report)
	if err != nil {
		return report, err
	}
	if _, err := conn.Exec(ctx, `UPDATE corescope_import SET verified=true,report=$1 WHERE id=1`, string(data)); err != nil {
		return report, err
	}
	return report, nil
}

func columnNames(table importTable) ([]string, []string) {
	names := make([]string, len(table.Columns))
	quoted := make([]string, len(names))
	for i, col := range table.Columns {
		names[i] = col.Name
		quoted[i] = quote(col.Name)
	}
	return names, quoted
}

func orderBy(db *sql.DB, table importTable, postgres bool) (string, error) {
	// Sort by logical primary key with identical byte collation in both engines.
	rows, err := db.Query(`SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=ANY(i.indkey)
  WHERE i.indrelid=to_regclass($1) AND i.indisprimary ORDER BY array_position(i.indkey,a.attnum)`, table.Name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		keys = append(keys, name)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", errors.New("destination table lacks a stable primary key")
	}
	var out []string
	for _, name := range keys {
		colType := ""
		for _, col := range table.Columns {
			if col.Name == name {
				colType = col.Type
			}
		}
		expr := quote(name)
		if table.Name == "mail_events" && name == "id" && !postgres {
			expr = "rowid"
		}
		if colType == "TEXT" {
			if postgres {
				expr += ` COLLATE "C"`
			} else {
				expr += ` COLLATE BINARY`
			}
		}
		if postgres {
			expr += " NULLS FIRST"
		}
		out = append(out, expr)
	}
	return strings.Join(out, ","), nil
}

func canonical(value any, kind string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch kind {
	case "TEXT":
		var s string
		switch v := value.(type) {
		case string:
			s = v
		case []byte:
			s = string(v)
		default:
			return nil, errors.New("non-text value in a text column")
		}
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return nil, errors.New("text contains invalid UTF-8 or NUL, which PostgreSQL cannot preserve")
		}
		return s, nil
	case "BIGINT":
		switch v := value.(type) {
		case int64:
			return v, nil
		case int32:
			return int64(v), nil
		case int:
			return int64(v), nil
		case float64:
			if math.Trunc(v) == v && v >= math.MinInt64 && v < 9223372036854775808.0 {
				return int64(v), nil
			}
		}
		return nil, errors.New("non-integral or out-of-range value in an integer column")
	case "DOUBLE PRECISION":
		switch v := value.(type) {
		case float64:
			return v, nil
		case int64:
			if int64(float64(v)) != v {
				return nil, errors.New("integer cannot be represented exactly by the destination floating-point column")
			}
			return float64(v), nil
		case int32:
			return float64(v), nil
		}
		return nil, errors.New("non-numeric value in a floating-point column")
	}
	return nil, fmt.Errorf("unsupported destination column type %s", kind)
}

func hashRow(h hash.Hash, values []any) {
	for _, value := range values {
		switch v := value.(type) {
		case nil:
			h.Write([]byte{0})
		case int64:
			h.Write([]byte{1})
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(v))
			h.Write(b[:])
		case float64:
			h.Write([]byte{2})
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
			h.Write(b[:])
		case string:
			h.Write([]byte{3})
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(len(v)))
			h.Write(b[:])
			h.Write([]byte(v))
		}
	}
	h.Write([]byte{255})
}

func digestTarget(ctx context.Context, db *sql.DB, table importTable) (tableReport, error) {
	report := tableReport{Table: table.Name}
	_, quoted := columnNames(table)
	order, err := orderBy(db, table, true)
	if err != nil {
		return report, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+strings.Join(quoted, ",")+` FROM `+quote(table.Name)+` ORDER BY `+order)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		values := make([]any, len(quoted))
		dest := make([]any, len(quoted))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return report, err
		}
		for i, col := range table.Columns {
			values[i], err = canonical(values[i], col.Type)
			if err != nil {
				return report, err
			}
		}
		hashRow(h, values)
		report.Rows++
	}
	report.SHA256 = hex.EncodeToString(h.Sum(nil))
	return report, rows.Err()
}

func copyTable(ctx context.Context, source, owner *sql.DB, conn *pgx.Conn, table importTable, o importOptions, verifyOnly bool) (tableReport, error) {
	report := tableReport{Table: table.Name}
	var copied int64
	var storedDigest string
	var complete bool
	err := conn.QueryRow(ctx, `SELECT rows_copied,sha256,complete FROM corescope_import_progress WHERE table_name=$1`, table.Name).Scan(&copied, &storedDigest, &complete)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return report, err
	}
	if verifyOnly && !complete {
		return report, errors.New("ready destination lacks complete import progress; resume refused")
	}
	existing, err := digestTarget(ctx, owner, table)
	if err != nil {
		return report, err
	}
	emptyHash := sha256.Sum256(nil)
	if storedDigest == "" {
		storedDigest = hex.EncodeToString(emptyHash[:])
	}
	if existing.Rows != copied || existing.SHA256 != storedDigest {
		return report, errors.New("destination rows differ from committed import progress; resume refused")
	}
	var exists int
	if err := source.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table.Name).Scan(&exists); err != nil {
		return report, err
	}
	h := sha256.New()
	names, quoted := columnNames(table)
	var batch [][]any
	var batchBytes int
	flush := func(done bool) error {
		if len(batch) == 0 && !done {
			return nil
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		if len(batch) > 0 {
			if n, err := tx.CopyFrom(ctx, pgx.Identifier{table.Name}, names, pgx.CopyFromRows(batch)); err != nil {
				return err
			} else if n != int64(len(batch)) {
				return errors.New("COPY row count mismatch")
			}
		}
		digest := hex.EncodeToString(h.Sum(nil))
		if _, err := tx.Exec(ctx, `INSERT INTO corescope_import_progress(table_name,rows_copied,sha256,complete) VALUES($1,$2,$3,$4)
    ON CONFLICT(table_name) DO UPDATE SET rows_copied=excluded.rows_copied,sha256=excluded.sha256,complete=excluded.complete`, table.Name, report.Rows, digest, done); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		wrote := len(batch) > 0
		batch = nil
		batchBytes = 0
		if wrote && o.afterBatch != nil {
			return o.afterBatch()
		}
		return nil
	}
	if exists != 0 {
		order, err := orderBy(owner, table, false)
		if err != nil {
			return report, err
		}
		expressions := append([]string(nil), quoted...)
		if o.Kind == "accounts" && table.Name == "mail_events" {
			expressions[0] = "rowid"
		}
		// SQLite's unary + preserves the stored value/type while removing declared
		// column metadata. Otherwise the driver parses DATE/DATETIME/TIMESTAMP
		// values into time.Time before copy/resume hashing can preserve their text.
		for i, expression := range expressions {
			expressions[i] = "+" + expression
		}
		rows, err := source.QueryContext(ctx, `SELECT `+strings.Join(expressions, ",")+` FROM `+quote(table.Name)+` ORDER BY `+order)
		if err != nil {
			return report, err
		}
		defer rows.Close()
		for rows.Next() {
			values := make([]any, len(names))
			dest := make([]any, len(names))
			for i := range dest {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return report, err
			}
			rowBytes := 0
			for i, col := range table.Columns {
				values[i], err = canonical(values[i], col.Type)
				if err != nil {
					return report, fmt.Errorf("column %s: %w", col.Name, err)
				}
				if s, ok := values[i].(string); ok {
					rowBytes += len(s)
				} else {
					rowBytes += 8
				}
			}
			if rowBytes > maxImportRowBytes {
				return report, errors.New("a source row exceeds the 64 MiB migration safety limit")
			}
			hashRow(h, values)
			report.Rows++
			if report.Rows <= copied {
				if report.Rows == copied && hex.EncodeToString(h.Sum(nil)) != storedDigest {
					return report, errors.New("source prefix differs from import progress")
				}
				continue
			}
			if complete {
				return report, errors.New("completed source table contains unexpected rows")
			}
			batch = append(batch, values)
			batchBytes += rowBytes
			if len(batch) >= o.BatchSize || batchBytes >= 8*1024*1024 {
				if err := flush(false); err != nil {
					return report, err
				}
			}
		}
		if err := rows.Err(); err != nil {
			return report, err
		}
		rows.Close()
	}
	if report.Rows < copied {
		return report, errors.New("source table shorter than committed progress")
	}
	if !verifyOnly {
		if err := flush(true); err != nil {
			return report, err
		}
	}
	report.SHA256 = hex.EncodeToString(h.Sum(nil))
	final, err := digestTarget(ctx, owner, table)
	if err != nil {
		return report, err
	}
	if final != report {
		return report, errors.New("row count or logical digest verification failed")
	}
	return report, nil
}

func reseed(ctx context.Context, source *sql.DB, conn *pgx.Conn, tables []importTable, kind string, verifyOnly bool) error {
	highWater := map[string]int64{}
	var hasSequence int
	if err := source.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='sqlite_sequence'`).Scan(&hasSequence); err != nil {
		return err
	}
	if hasSequence > 0 {
		rows, err := source.Query(`SELECT name,seq FROM sqlite_sequence`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var n int64
			if err := rows.Scan(&name, &n); err != nil {
				rows.Close()
				return err
			}
			highWater[name] = n
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	references := map[string][][2]string{}
	if kind == "telemetry" {
		references["observers"] = [][2]string{{"observations", "observer_idx"}}
		references["transmissions"] = [][2]string{{"observations", "transmission_id"}, {"advert_route_evidence", "tx_id"}, {"advert_evidence_backfill", "tx_cursor"}}
		references["observations"] = [][2]string{{"advert_evidence_backfill", "obs_cursor"}}
	} else {
		references["users"] = [][2]string{{"audit_log", "actor_user_id"}, {"audit_log", "target_user_id"}, {"users", "activated_by"}, {"sessions", "user_id"}, {"tokens", "user_id"}, {"mail_log", "user_id"}, {"proposals", "proposer_id"}, {"proposals", "reviewer_id"}}
		references["mail_log"] = [][2]string{{"mail_events", "mail_id"}}
	}
	for _, table := range tables {
		for _, col := range table.Columns {
			if !col.Identity {
				continue
			}
			high := highWater[table.Name]
			var n int64
			if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(`+quote(col.Name)+`),0) FROM `+quote(table.Name)).Scan(&n); err != nil {
				return err
			}
			if n > high {
				high = n
			}
			for _, ref := range references[table.Name] {
				if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(`+quote(ref[1])+`),0) FROM `+quote(ref[0])).Scan(&n); err != nil {
					return err
				}
				if n > high {
					high = n
				}
			}
			called := high > 0
			if !called {
				high = 1
			}
			if verifyOnly {
				var sequence string
				if err := conn.QueryRow(ctx, `SELECT pg_get_serial_sequence($1,$2)`, table.Name, col.Name).Scan(&sequence); err != nil {
					return err
				}
				var actual int64
				var wasCalled bool
				// pg_get_serial_sequence returns a safely quoted qualified identifier.
				if err := conn.QueryRow(ctx, `SELECT last_value,is_called FROM `+sequence).Scan(&actual, &wasCalled); err != nil {
					return err
				}
				if actual != high || wasCalled != called {
					return fmt.Errorf("ready destination sequence for %s changed since import; resume refused", table.Name)
				}
			} else if _, err := conn.Exec(ctx, `SELECT setval(pg_get_serial_sequence($1,$2),$3,$4)`, table.Name, col.Name, high, called); err != nil {
				return err
			}
		}
	}
	return nil
}

// finalizeImport reports whether this call committed a closed-to-ready transition.
// The caller may roll back only transitions it owns if another store fails.
func finalizeImport(ctx context.Context, databaseURL, kind string) (bool, error) {
	tables, err := layouts(kind)
	if err != nil {
		return false, err
	}
	db, err := pgutil.Open(databaseURL, false)
	if err != nil {
		return false, err
	}
	defer db.Close()
	config, err := pgutil.ParseConfig(databaseURL)
	if err != nil {
		return false, err
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return false, errors.New("connect to PostgreSQL finalization destination failed")
	}
	defer conn.Close(context.Background())
	if err := lockImport(ctx, conn); err != nil {
		return false, err
	}
	var verified bool
	var recordedKind, reportText string
	if err := conn.QueryRow(ctx, `SELECT kind,verified,report FROM corescope_import WHERE id=1`).Scan(&recordedKind, &verified, &reportText); err != nil {
		return false, err
	}
	if !verified || recordedKind != kind {
		return false, errors.New("cannot finalize an unverified or mismatched import")
	}
	var report importReport
	if err := json.Unmarshal([]byte(reportText), &report); err != nil {
		return false, err
	}
	if !report.Verified || report.Kind != kind || len(report.Tables) != len(tables) {
		return false, errors.New("incomplete verification manifest")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var lockTables []string
	for _, table := range tables {
		lockTables = append(lockTables, quote(table.Name))
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+strings.Join(lockTables, ",")+` IN SHARE MODE`); err != nil {
		return false, err
	}
	for i, table := range tables {
		got, err := digestTarget(ctx, db, table)
		if err != nil {
			return false, err
		}
		if got != report.Tables[i] {
			return false, fmt.Errorf("final verification failed for %s; target remains unready", table.Name)
		}
	}
	var ready bool
	if err := tx.QueryRow(ctx, `SELECT ready FROM corescope_schema WHERE kind=$1 AND version=1`, kind).Scan(&ready); err != nil {
		return false, errors.New("missing or unsupported readiness marker")
	}
	if ready {
		return false, nil
	}
	result, err := tx.Exec(ctx, `UPDATE corescope_schema SET ready=true WHERE kind=$1 AND version=1`, kind)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() != 1 {
		return false, errors.New("missing or unsupported readiness marker")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func lockImport(ctx context.Context, conn *pgx.Conn) error {
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended(current_schema(),$1))`, importerLock).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("another importer owns this destination; wait for it to finish before retrying")
	}
	return nil
}
