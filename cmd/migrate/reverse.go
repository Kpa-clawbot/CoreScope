package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

type reverseOptions struct {
	DatabaseURL, Destination, StateDir, Kind string
	BatchSize                                int
	Resume                                   bool
	keepPending                              bool
	afterBatch                               func() error
	source                                   *postgresFence
}

type identityState struct {
	Table       string `json:"table"`
	Column      string `json:"column"`
	LastValue   int64  `json:"last_value"`
	IsCalled    bool   `json:"is_called"`
	SQLiteFloor int64  `json:"sqlite_floor"`
}

type reverseManifest struct {
	Version               int             `json:"version"`
	RunID                 string          `json:"run_id"`
	Kind                  string          `json:"kind"`
	Destination           string          `json:"destination"`
	SourceIdentity        string          `json:"source_identity"`
	RecoverySHA256        string          `json:"recovery_sha256"`
	Source                []tableReport   `json:"source"`
	Identities            []identityState `json:"identities"`
	AddedSQLiteMigrations []string        `json:"added_sqlite_migrations"`
}

type postgresFence struct {
	db       *sql.DB
	tx       *sql.Tx
	kind     string
	tables   []importTable
	identity string
}

func (f *postgresFence) Close() {
	if f.tx != nil {
		f.tx.Rollback()
	}
	if f.db != nil {
		f.db.Close()
	}
}

// Local service leases are only one fence. Native table locks and the ingestor's
// commit-order lock also exclude real database writers during the offline copy.
func fencePostgres(ctx context.Context, dsn, kind string) (_ *postgresFence, err error) {
	f := &postgresFence{kind: kind}
	if f.tables, err = layouts(kind); err != nil {
		return nil, err
	}
	if f.db, err = pgutil.Open(dsn, false); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	if kind == "telemetry" {
		err = dbschema.AssertPostgresReady(f.db)
	} else {
		err = users.AssertPostgresReady(f.db)
	}
	if err != nil {
		return nil, err
	}
	f.tx, err = f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = f.tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(current_schema(),$1))`, dbschema.WriterLockKey).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("PostgreSQL source writer is active; stop all writers before conversion")
	}
	var names []string
	for _, table := range f.tables {
		names = append(names, quote(table.Name))
	}
	if _, err = f.tx.ExecContext(ctx, `LOCK TABLE `+strings.Join(names, ",")+` IN SHARE MODE NOWAIT`); err != nil {
		return nil, errors.New("PostgreSQL source is in use; stop all writers before conversion")
	}
	if err = validatePostgresLayout(f.db, kind, f.tables); err != nil {
		return nil, err
	}
	config, e := pgutil.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	var database, schema string
	var dbOID, schemaOID int64
	if err = f.tx.QueryRowContext(ctx, `SELECT current_database(),current_schema(),(SELECT oid::bigint FROM pg_database WHERE datname=current_database()),(SELECT oid::bigint FROM pg_namespace WHERE nspname=current_schema())`).Scan(&database, &schema, &dbOID, &schemaOID); err != nil {
		return nil, err
	}
	data, _ := json.Marshal([]any{database, schema, dbOID, schemaOID, config.Host, config.Port})
	sum := sha256.Sum256(data)
	f.identity = hex.EncodeToString(sum[:])
	return f, nil
}

func identityReferences(kind string) map[string][][2]string {
	if kind == "telemetry" {
		return map[string][][2]string{
			"observers":     {{"observations", "observer_idx"}},
			"transmissions": {{"observations", "transmission_id"}, {"advert_route_evidence", "tx_id"}, {"advert_evidence_backfill", "tx_cursor"}},
			"observations":  {{"advert_evidence_backfill", "obs_cursor"}},
		}
	}
	return map[string][][2]string{
		"users":    {{"audit_log", "actor_user_id"}, {"audit_log", "target_user_id"}, {"users", "activated_by"}, {"sessions", "user_id"}, {"tokens", "user_id"}, {"mail_log", "user_id"}, {"proposals", "proposer_id"}, {"proposals", "reviewer_id"}, {"user_settings", "user_id"}, {"notification_prefs", "user_id"}, {"notification_watches", "user_id"}, {"notification_state", "user_id"}},
		"mail_log": {{"mail_events", "mail_id"}},
	}
}

func postgresIdentities(ctx context.Context, f *postgresFence) ([]identityState, error) {
	var out []identityState
	refs := identityReferences(f.kind)
	for _, table := range f.tables {
		for _, col := range table.Columns {
			if !col.Identity {
				continue
			}
			var sequence string
			if err := f.db.QueryRowContext(ctx, `SELECT pg_get_serial_sequence($1,$2)`, table.Name, col.Name).Scan(&sequence); err != nil {
				return nil, err
			}
			var increment, min, max int64
			var cycle bool
			if err := f.db.QueryRowContext(ctx, `SELECT seqincrement,seqmin,seqmax,seqcycle FROM pg_sequence WHERE seqrelid=$1::regclass`, sequence).Scan(&increment, &min, &max, &cycle); err != nil {
				return nil, err
			}
			if increment != 1 || min != 1 || max != math.MaxInt64 || cycle {
				return nil, errors.New("source identity sequence has unsupported custom allocation settings")
			}
			state := identityState{Table: table.Name, Column: col.Name}
			if err := f.db.QueryRowContext(ctx, `SELECT last_value,is_called FROM `+sequence).Scan(&state.LastValue, &state.IsCalled); err != nil {
				return nil, err
			}
			state.SQLiteFloor = state.LastValue
			if !state.IsCalled {
				state.SQLiteFloor--
			}
			if state.SQLiteFloor < 0 {
				return nil, errors.New("invalid source identity high water")
			}
			for _, ref := range append([][2]string{{table.Name, col.Name}}, refs[table.Name]...) {
				var high int64
				if err := f.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(`+quote(ref[1])+`),0) FROM `+quote(ref[0])).Scan(&high); err != nil {
					return nil, err
				}
				if high > state.SQLiteFloor {
					state.SQLiteFloor = high
				}
			}
			out = append(out, state)
		}
	}
	return out, nil
}

func digestPostgresSource(ctx context.Context, f *postgresFence) ([]tableReport, error) {
	var reports []tableReport
	for _, table := range f.tables {
		// Bound decompressed text before a large value reaches the Go driver.
		sizes := []string{"0"}
		for _, col := range table.Columns {
			if col.Type == "TEXT" {
				sizes = append(sizes, `COALESCE(octet_length(`+quote(col.Name)+`),0)::bigint`)
			} else {
				sizes = append(sizes, "8")
			}
		}
		var oversized bool
		if err := f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+quote(table.Name)+` WHERE `+strings.Join(sizes, "+")+`>$1)`, maxImportRowBytes).Scan(&oversized); err != nil {
			return nil, err
		}
		if oversized {
			return nil, errors.New("a source row exceeds the 64 MiB migration safety limit")
		}
		report, err := digestTarget(ctx, f.db, table)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func readPrivateJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	d := json.NewDecoder(io.LimitReader(file, 1024*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid private conversion manifest")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing conversion manifest")
	}
	return nil
}
func fileDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, contextReader{ctx, file}, make([]byte, 128*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func prepareReverse(ctx context.Context, o reverseOptions, f *postgresFence) (reverseManifest, error) {
	var manifest reverseManifest
	dest, err := filepath.Abs(o.Destination)
	if err != nil {
		return manifest, err
	}
	if o.Destination == "" || o.StateDir == "" {
		return manifest, errors.New("reverse conversion requires a fresh SQLite target and private recovery directory")
	}
	dir := filepath.Join(o.StateDir, o.Kind)
	path := filepath.Join(dir, "reverse.json")
	backup := filepath.Join(dir, "recovery.pg_dump")
	source, err := digestPostgresSource(ctx, f)
	if err != nil {
		return manifest, err
	}
	identities, err := postgresIdentities(ctx, f)
	if err != nil {
		return manifest, err
	}
	err = readPrivateJSON(path, &manifest)
	if err == nil {
		if !o.Resume || manifest.Version != 1 || manifest.Kind != o.Kind || manifest.Destination != dest || manifest.SourceIdentity != f.identity || !slices.Equal(source, manifest.Source) || !slices.Equal(identities, manifest.Identities) {
			return manifest, errors.New("reverse resume source, sequence state or destination differs from the immutable manifest")
		}
		digest, err := fileDigest(ctx, backup)
		if err != nil {
			return manifest, err
		}
		if digest != manifest.RecoverySHA256 {
			return manifest, errors.New("native PostgreSQL recovery archive changed; resume refused")
		}
		return manifest, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	if o.Resume {
		return manifest, errors.New("reverse preparation has no complete manifest; preserve recovery files and abort before choosing a new job")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return manifest, errors.New("reverse destination already exists or cannot be inspected; existing files were not changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return manifest, err
	}
	if len(entries) != 0 {
		return manifest, errors.New("reverse recovery directory is not empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return manifest, err
	}
	// Recovery is native and contains the whole selected database. The source
	// table fences remain held; removing search_path changes no database target.
	u, err := url.Parse(o.DatabaseURL)
	if err != nil {
		return manifest, errors.New("invalid source database URL")
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	if err := pgutil.Dump(ctx, u.String(), backup); err != nil {
		return manifest, err
	}
	digest, err := fileDigest(ctx, backup)
	if err != nil {
		return manifest, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return manifest, err
	}
	manifest = reverseManifest{Version: 1, RunID: hex.EncodeToString(random[:]), Kind: o.Kind, Destination: dest, SourceIdentity: f.identity, RecoverySHA256: digest, Source: source, Identities: identities}
	if o.Kind == "telemetry" {
		template, err := sql.Open(importSQLiteDriver, ":memory:")
		if err != nil {
			return manifest, err
		}
		defer template.Close()
		template.SetMaxOpenConns(1)
		if err := dbschema.ApplySQLite(template, func(string, ...interface{}) {}); err != nil {
			return manifest, err
		}
		rows, err := template.Query(`SELECT name FROM _migrations ORDER BY name`)
		if err != nil {
			return manifest, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return manifest, err
			}
			var n int
			if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM _migrations WHERE name=$1`, name).Scan(&n); err != nil {
				rows.Close()
				return manifest, err
			}
			if n == 0 {
				manifest.AddedSQLiteMigrations = append(manifest.AddedSQLiteMigrations, name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return manifest, err
		}
	}
	after, err := postgresIdentities(ctx, f)
	if err != nil {
		return manifest, err
	}
	if !slices.Equal(identities, after) {
		return manifest, errors.New("source sequence allocation changed during backup; stop every writer")
	}
	return manifest, writePrivateJSON(path, manifest)
}

func reversePostgres(ctx context.Context, o reverseOptions) (importReport, error) {
	report := importReport{Kind: o.Kind}
	if o.BatchSize == 0 {
		o.BatchSize = 1000
	}
	if o.BatchSize < 1 || o.BatchSize > 10000 {
		return report, errors.New("copy batch size must be between 1 and 10000")
	}
	f := o.source
	if f == nil {
		var err error
		f, err = fencePostgres(ctx, o.DatabaseURL, o.Kind)
		if err != nil {
			return report, err
		}
		defer f.Close()
	}
	manifest, err := prepareReverse(ctx, o, f)
	if err != nil {
		return report, err
	}
	destination, verifyOnly, err := openReverseTarget(ctx, o, manifest)
	if err != nil {
		return report, err
	}
	defer destination.Close()
	for i, table := range f.tables {
		got, err := copyReverseTable(ctx, f.db, destination, table, o, manifest, verifyOnly)
		if err != nil {
			return report, fmt.Errorf("reverse %s: %w", table.Name, err)
		}
		if got != manifest.Source[i] {
			return report, errors.New("reverse source count or logical digest changed")
		}
		report.Tables = append(report.Tables, got)
	}
	for _, identity := range manifest.Identities {
		var count int
		var high int64
		if err := destination.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name=?`, identity.Table).Scan(&count, &high); err != nil {
			return report, err
		}
		if verifyOnly {
			if count != 1 || high != identity.SQLiteFloor {
				return report, errors.New("completed SQLite identity allocation changed; resume refused")
			}
			continue
		}
		if _, err := destination.ExecContext(ctx, `DELETE FROM sqlite_sequence WHERE name=?`, identity.Table); err != nil {
			return report, err
		}
		if _, err := destination.ExecContext(ctx, `INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)`, identity.Table, identity.SQLiteFloor); err != nil {
			return report, err
		}
	}
	if !verifyOnly {
		for _, name := range manifest.AddedSQLiteMigrations {
			if _, err := destination.ExecContext(ctx, `INSERT OR IGNORE INTO _migrations(name) VALUES(?)`, name); err != nil {
				return report, err
			}
		}
	}
	for _, name := range manifest.AddedSQLiteMigrations {
		var n int
		if err := destination.QueryRowContext(ctx, `SELECT count(*) FROM _migrations WHERE name=?`, name).Scan(&n); err != nil || n != 1 {
			return report, errors.New("SQLite physical migration marker is missing")
		}
	}
	if o.Kind == "telemetry" {
		// A copied ledger can already mark this async task done. Build the real
		// index offline; never rewrite its retained status to hide absent work.
		if err := dbschema.EnsureSQLiteObserverTimeIndex(ctx, destination); err != nil {
			return report, err
		}
	}
	if err := assertSQLiteIntegrity(ctx, destination); err != nil {
		return report, err
	}
	sourceAfter, err := digestPostgresSource(ctx, f)
	if err != nil {
		return report, err
	}
	identitiesAfter, err := postgresIdentities(ctx, f)
	if err != nil {
		return report, err
	}
	if !slices.Equal(sourceAfter, manifest.Source) || !slices.Equal(identitiesAfter, manifest.Identities) {
		return report, errors.New("PostgreSQL source or sequence changed during conversion; target remains unselected")
	}
	if err := validatePostgresLayout(f.db, o.Kind, f.tables); err != nil {
		return report, err
	}
	if !o.keepPending {
		if !verifyOnly {
			if _, err := destination.ExecContext(ctx, `DROP TABLE corescope_reverse_progress`); err != nil {
				return report, err
			}
		}
		if err := assertSQLiteData(ctx, destination, o.Kind); err != nil {
			return report, err
		}
	}
	report.Verified = true
	return report, nil
}

func createReverseTarget(ctx context.Context, destination, kind, runID string) (err error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".corescope-reverse-*.sqlite")
	if err != nil {
		return err
	}
	temp := file.Name()
	if err := file.Close(); err != nil {
		return err
	}
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(temp + suffix)
		}
	}()
	uri, err := legacy.WriterDSN(temp)
	if err != nil {
		return err
	}
	db, err := sql.Open(importSQLiteDriver, uri)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if kind == "telemetry" {
		err = dbschema.ApplySQLite(db, func(string, ...interface{}) {})
	} else {
		err = users.ApplySQLite(db)
	}
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tables, err := layouts(kind)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+quote(tables[i].Name)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE corescope_reverse_progress(table_name TEXT PRIMARY KEY,rows_copied INTEGER NOT NULL,sha256 TEXT NOT NULL,complete INTEGER NOT NULL); INSERT INTO corescope_reverse_progress VALUES('',0,?,0)`, runID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var busy, log, checkpointed int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("private SQLite staging checkpoint is busy")
	}
	if err := db.Close(); err != nil {
		return err
	}
	// Publish the complete native pending marker with the file. Link never
	// overwrites an existing path, unlike rename on Unix. Both paths share a
	// directory/filesystem and the closed database has no outstanding WAL.
	return os.Link(temp, destination)
}

func openReverseTarget(ctx context.Context, o reverseOptions, m reverseManifest) (*sql.DB, bool, error) {
	if _, err := os.Lstat(m.Destination); errors.Is(err, os.ErrNotExist) {
		if err := createReverseTarget(ctx, m.Destination, o.Kind, m.RunID); err != nil {
			return nil, false, err
		}
	} else if err != nil {
		return nil, false, err
	} else if !o.Resume {
		return nil, false, errors.New("SQLite destination exists; use the exact verified job to resume")
	}
	uri, err := legacy.WriterDSN(m.Destination)
	if err != nil {
		return nil, false, err
	}
	db, err := sql.Open(importSQLiteDriver, uri)
	if err != nil {
		return nil, false, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, bool, error) { db.Close(); return nil, false, err }
	var progress int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='corescope_reverse_progress' AND type='table'`).Scan(&progress); err != nil {
		return fail(err)
	}
	if progress == 0 {
		return db, true, nil
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT sha256 FROM corescope_reverse_progress WHERE table_name=''`).Scan(&id); err != nil || id != m.RunID {
		return fail(errors.New("SQLite target belongs to another or incomplete preparation job"))
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fail(err)
	}
	return db, false, nil
}
func digestSQLiteTable(ctx context.Context, source, target *sql.DB, table importTable, m reverseManifest) (tableReport, error) {
	report := tableReport{Table: table.Name}
	_, names := columnNames(table)
	for i := range names {
		names[i] = "+" + names[i]
	}
	order, err := orderBy(source, table, false, false)
	if err != nil {
		return report, err
	}
	where := ""
	var args []any
	if table.Name == "_migrations" && len(m.AddedSQLiteMigrations) > 0 {
		var params []string
		for _, name := range m.AddedSQLiteMigrations {
			params = append(params, "?")
			args = append(args, name)
		}
		where = ` WHERE name NOT IN (` + strings.Join(params, ",") + `)`
	}
	rows, err := target.QueryContext(ctx, `SELECT `+strings.Join(names, ",")+` FROM `+quote(table.Name)+where+` ORDER BY `+order, args...)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		values, err := scanCanonicalRow(rows, table)
		if err != nil {
			return report, err
		}
		hashRow(h, values)
		report.Rows++
	}
	report.SHA256 = hex.EncodeToString(h.Sum(nil))
	return report, rows.Err()
}

func scanCanonicalRow(rows *sql.Rows, table importTable) ([]any, error) {
	values := make([]any, len(table.Columns))
	dest := make([]any, len(values))
	for i := range dest {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	for i, col := range table.Columns {
		value, err := canonical(values[i], col.Type)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", col.Name, err)
		}
		if f, ok := value.(float64); ok && math.IsNaN(f) {
			return nil, errors.New("SQLite cannot preserve a PostgreSQL NaN; source was not changed")
		}
		values[i] = value
	}
	return values, nil
}

func copyReverseTable(ctx context.Context, source, target *sql.DB, table importTable, o reverseOptions, m reverseManifest, verifyOnly bool) (tableReport, error) {
	report := tableReport{Table: table.Name}
	existing, err := digestSQLiteTable(ctx, source, target, table, m)
	if err != nil {
		return report, err
	}
	var copied int64
	var stored string
	var complete bool
	if verifyOnly {
		copied, stored, complete = existing.Rows, existing.SHA256, true
	} else {
		err := target.QueryRowContext(ctx, `SELECT rows_copied,sha256,complete FROM corescope_reverse_progress WHERE table_name=?`, table.Name).Scan(&copied, &stored, &complete)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return report, err
		}
		if stored == "" {
			sum := sha256.Sum256(nil)
			stored = hex.EncodeToString(sum[:])
		}
		if existing.Rows != copied || existing.SHA256 != stored {
			return report, errors.New("SQLite destination differs from committed copy progress")
		}
	}
	_, names := columnNames(table)
	order, err := orderBy(source, table, true, false)
	if err != nil {
		return report, err
	}
	rows, err := source.QueryContext(ctx, `SELECT `+strings.Join(names, ",")+` FROM `+quote(table.Name)+` ORDER BY `+order)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	placeholders := make([]string, len(names))
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("?%d", i+1)
	}
	insert := `INSERT INTO ` + quote(table.Name) + ` (` + strings.Join(names, ",") + `) VALUES(` + strings.Join(placeholders, ",") + `)`
	h := sha256.New()
	var batch [][]any
	bytes := 0
	flush := func(done bool) error {
		if verifyOnly {
			return nil
		}
		if len(batch) == 0 && !done {
			return nil
		}
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.PrepareContext(ctx, insert)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, values := range batch {
			if _, err := stmt.ExecContext(ctx, values...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO corescope_reverse_progress VALUES(?,?,?,?) ON CONFLICT(table_name) DO UPDATE SET rows_copied=excluded.rows_copied,sha256=excluded.sha256,complete=excluded.complete`, table.Name, report.Rows, hex.EncodeToString(h.Sum(nil)), done); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		wrote := len(batch) > 0
		batch = nil
		bytes = 0
		if wrote && o.afterBatch != nil {
			return o.afterBatch()
		}
		return nil
	}
	for rows.Next() {
		values, err := scanCanonicalRow(rows, table)
		if err != nil {
			return report, err
		}
		hashRow(h, values)
		report.Rows++
		if report.Rows <= copied {
			if report.Rows == copied && hex.EncodeToString(h.Sum(nil)) != stored {
				return report, errors.New("source prefix differs from reverse copy progress")
			}
			continue
		}
		if complete {
			return report, errors.New("completed SQLite table has missing source rows")
		}
		batch = append(batch, values)
		for _, value := range values {
			if s, ok := value.(string); ok {
				bytes += len(s)
			} else {
				bytes += 8
			}
		}
		if len(batch) >= o.BatchSize || bytes >= 8*1024*1024 {
			if err := flush(false); err != nil {
				return report, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	if report.Rows < copied {
		return report, errors.New("SQLite target contains extra rows")
	}
	if err := flush(true); err != nil {
		return report, err
	}
	report.SHA256 = hex.EncodeToString(h.Sum(nil))
	got, err := digestSQLiteTable(ctx, source, target, table, m)
	if err != nil {
		return report, err
	}
	if got != report {
		return report, errors.New("SQLite count or logical digest verification failed")
	}
	return report, nil
}

func assertSQLiteData(ctx context.Context, db *sql.DB, kind string) error {
	if err := assertSQLiteIntegrity(ctx, db); err != nil {
		return err
	}
	if kind == "telemetry" {
		return dbschema.AssertSQLiteReady(db)
	}
	return users.AssertSQLiteReady(db)
}

func assertSQLiteIntegrity(ctx context.Context, db *sql.DB) error {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("SQLite target failed integrity check")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return rowErr
	}
	if invalid {
		return errors.New("SQLite target relationships failed verification")
	}
	return nil
}
