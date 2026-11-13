package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// pgxStdlibDriver is pgx's own database/sql driver (registered by its stdlib
// package). The Postgres write session uses it instead of dbx's wrapper
// because COPY needs the *stdlib.Conn that sql.Conn.Raw hands out, which the
// wrapper hides. SQL sent on it uses $n placeholders (dbx.Rebind).
const pgxStdlibDriver = "pgx"

// progressEvery is how often a long table logs an interim row count.
const progressEvery = 30 * time.Second

type copyJob struct {
	lg    *log.Logger
	src   side
	batch int
	rows  int64 // total rows written
}

// runCopy implements `corescope-migrate copy -from <db> -to <db> [-batch N]`.
func runCopy(ctx context.Context, args []string, lg *log.Logger, sw *scrubWriter) error {
	fs := flag.NewFlagSet("copy", flag.ContinueOnError)
	fs.SetOutput(lg.Writer())
	fromArg := fs.String("from", "", "source database (sqlite:<path> or postgres://…); must be fully migrated")
	toArg := fs.String("to", "", "target database (sqlite:<path> or postgres://…); must be empty")
	batch := fs.Int("batch", 5000, "rows per COPY call (Postgres target) or per transaction (SQLite target)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *fromArg == "" || *toArg == "" || *batch < 1 || fs.NArg() > 0 {
		fs.Usage()
		return errUsage
	}
	from, err := parseDBRef(*fromArg)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	to, err := parseDBRef(*toArg)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}
	sw.add(from, to)

	lg.Printf("copy %s -> %s (batch %d)", from.display, to.display, *batch)
	lg.Printf("WARNING: stop every writer (the ingestor) on the source first: the copy reads one snapshot, and rows written after it are not copied")

	src, closeSrc, err := openSource(ctx, from)
	if err != nil {
		return err
	}
	defer closeSrc()

	job := &copyJob{lg: lg, src: src, batch: *batch}
	start := time.Now()
	if to.kind == postgresBackend {
		err = job.intoPostgres(ctx, to)
	} else {
		err = job.intoSQLite(ctx, to)
	}
	if err != nil {
		return err
	}
	lg.Printf("OK: copied %d rows into %s in %s; the copy is verified", job.rows, to.display, time.Since(start).Round(time.Millisecond))
	return nil
}

// plan returns the target tables to copy in foreign-key order (parents
// first), after checking the source can supply every target column. Tables
// and columns only the source has are reported, not copied.
func (j *copyJob) plan(ctx context.Context, tgt *schema) ([]*table, error) {
	var names []string
	for _, name := range tgt.sortedNames() {
		if skipReason(name) != "" {
			continue
		}
		tt := tgt.tables[name]
		st := j.src.schema.tables[name]
		if st == nil {
			return nil, fmt.Errorf("source has no table %s that the target schema has; is the source fully migrated?", name)
		}
		if len(tt.pk) == 0 {
			return nil, fmt.Errorf("target table %s has no primary key", name)
		}
		for _, c := range tt.cols {
			if st.colIndex(c.name) < 0 {
				return nil, fmt.Errorf("source table %s lacks column %s that the target has; is the source fully migrated?", name, c.name)
			}
		}
		for _, c := range st.cols {
			if tt.colIndex(c.name) < 0 {
				j.lg.Printf("WARNING: source column %s.%s has no counterpart in the target schema; it is NOT copied", name, c.name)
			}
		}
		names = append(names, name)
	}

	all := map[string]bool{}
	for n := range tgt.tables {
		all[n] = true
	}
	for n := range j.src.schema.tables {
		all[n] = true
	}
	var seen []string
	for n := range all {
		seen = append(seen, n)
	}
	sort.Strings(seen)
	for _, n := range seen {
		if r := skipReason(n); r != "" {
			j.lg.Printf("skipping %s: %s", n, r)
		} else if tgt.tables[n] == nil {
			var cnt int64
			if err := j.src.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(n)).Scan(&cnt); err != nil {
				return nil, fmt.Errorf("count source table %s: %w", n, err)
			}
			j.lg.Printf("WARNING: source table %s (%d rows) has no counterpart in the target schema; it is NOT copied", n, cnt)
		}
	}

	order, err := fkOrder(tgt, names)
	if err != nil {
		return nil, err
	}
	out := make([]*table, len(order))
	for i, n := range order {
		out[i] = tgt.tables[n]
	}
	return out, nil
}

func skipReason(name string) string {
	if r, ok := skipTables[name]; ok {
		return r
	}
	if strings.HasPrefix(name, "sqlite_") {
		return "SQLite-internal"
	}
	return ""
}

// checkEmpty refuses a target that already holds rows in any data table.
// The bookkeeping tables are exempt: schema creation seeds them.
func checkEmpty(ctx context.Context, q queryer, ref dbRef, plan []*table) error {
	for _, t := range plan {
		if bookkeepingTables[t.name] {
			continue
		}
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM `+quoteIdent(t.name)+` LIMIT 1`).Scan(&one)
		if err == nil {
			return fmt.Errorf("target is not empty (table %s has rows); refusing to overwrite. Copy into a fresh target: a SQLite path that does not exist yet, or an empty Postgres database", t.name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check %s on %s: %w", t.name, ref.display, err)
		}
	}
	return nil
}

// normalizeRow normalises raw (in t.cols order) into dst, failing with the
// table, primary key and column named.
func normalizeRow(t *table, raw, dst []any, strict bool) error {
	isPK := func(i int) bool {
		for _, p := range t.pk {
			if p == i {
				return true
			}
		}
		return false
	}
	for i, c := range t.cols {
		v, err := normalize(c.class, raw[i], strict)
		if err == nil && v == nil && isPK(i) {
			err = errors.New("NULL in a primary-key column")
		}
		if err != nil {
			return fmt.Errorf("table %s, row %s, column %s: %v", t.name, describePK(t, raw), c.name, err)
		}
		dst[i] = v
	}
	return nil
}

// progress logs one line per table, plus an interim line on long tables.
type progress struct {
	lg    *log.Logger
	table string
	start time.Time
	last  time.Time
	n     int64
}

func newProgress(lg *log.Logger, table string) *progress {
	now := time.Now()
	return &progress{lg: lg, table: table, start: now, last: now}
}

func (p *progress) add(k int64) {
	p.n += k
	if now := time.Now(); now.Sub(p.last) >= progressEvery {
		p.last = now
		p.lg.Printf("  … %s: %d rows so far", p.table, p.n)
	}
}

func (p *progress) done() {
	el := time.Since(p.start)
	rate := float64(p.n) / el.Seconds()
	p.lg.Printf("copied %-28s %10d rows in %8s (%.0f rows/s)", p.table, p.n, el.Round(time.Millisecond), rate)
}

// upsertStmt writes a bookkeeping row over the seeded one with the same key.
func upsertStmt(t *table) string {
	cols := t.colNames()
	q := make([]string, len(cols))
	for i := range q {
		q[i] = "?"
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	var pk, set []string
	for _, idx := range t.pk {
		pk = append(pk, quoteIdent(cols[idx]))
	}
	for i, c := range cols {
		if !containsInt(t.pk, i) {
			set = append(set, quoteIdent(c)+" = excluded."+quoteIdent(c))
		}
	}
	action := "DO NOTHING"
	if len(set) > 0 {
		action = "DO UPDATE SET " + strings.Join(set, ", ")
	}
	return "INSERT INTO " + quoteIdent(t.name) + " (" + strings.Join(quoted, ", ") + ") VALUES (" +
		strings.Join(q, ", ") + ") ON CONFLICT (" + strings.Join(pk, ", ") + ") " + action
}

func insertStmt(t *table) string {
	cols := t.colNames()
	quoted := make([]string, len(cols))
	q := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
		q[i] = "?"
	}
	return "INSERT INTO " + quoteIdent(t.name) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(q, ", ") + ")"
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// upsertTable copies a bookkeeping table row by row (they hold tens of rows).
func (j *copyJob) upsertTable(ctx context.Context, ex execer, kind backend, t *table) error {
	p := newProgress(j.lg, t.name)
	q := upsertStmt(t)
	if kind == postgresBackend {
		q = dbx.Rebind(q)
	}
	s, err := openStream(ctx, j.src.q, j.src.kind, t, t.colNames(), true)
	if err != nil {
		return err
	}
	defer s.close()
	row := make([]any, len(t.cols))
	for {
		ok, err := s.next()
		if err != nil {
			return fmt.Errorf("read %s: %w", t.name, err)
		}
		if !ok {
			break
		}
		if err := normalizeRow(t, s.vals, row, kind == postgresBackend); err != nil {
			return err
		}
		if _, err := ex.ExecContext(ctx, q, row...); err != nil {
			return fmt.Errorf("write %s row %s: %w", t.name, describePK(t, s.vals), err)
		}
		p.add(1)
	}
	j.rows += p.n
	p.done()
	return nil
}

// ── Postgres target ──────────────────────────────────────────────────────

// intoPostgres copies into a Postgres target inside ONE transaction. A
// failure at any point (including verify, which runs before COMMIT) rolls
// everything back, so the target is left exactly as it was: schema present,
// data tables empty, which the empty-target rule accepts on a rerun. Postgres
// rolls back by discarding the transaction id, so even a 20M-row abort is
// instant, and an aborted COPY leaves only dead tuples for autovacuum.
// Per-table transactions would instead leave a half-loaded target that the
// operator has to drop by hand.
func (j *copyJob) intoPostgres(ctx context.Context, to dbRef) (err error) {
	admin, err := sql.Open(dbx.PostgresDriverName, to.url)
	if err != nil {
		return fmt.Errorf("open %s: %w", to.display, err)
	}
	defer admin.Close()
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to %s: %w", to.display, err)
	}
	if err := pgschema.Apply(ctx, admin, j.lg.Printf); err != nil {
		return fmt.Errorf("create schema on %s: %w", to.display, err)
	}
	tgt, err := loadSchema(ctx, admin, postgresBackend)
	if err != nil {
		return fmt.Errorf("read schema of %s: %w", to.display, err)
	}
	plan, err := j.plan(ctx, tgt)
	if err != nil {
		return err
	}
	if err := checkEmpty(ctx, admin, to, plan); err != nil {
		return err
	}

	wdb, err := sql.Open(pgxStdlibDriver, to.url)
	if err != nil {
		return fmt.Errorf("open %s: %w", to.display, err)
	}
	defer wdb.Close()
	conn, err := wdb.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", to.display, err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin on %s: %w", to.display, err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
			j.lg.Printf("the target transaction was rolled back: %s has the CoreScope schema but none of the copied rows, so it is still an empty target; fix the cause above and rerun the same command", to.display)
		}
	}()

	for _, t := range plan {
		if bookkeepingTables[t.name] {
			err = j.upsertTable(ctx, tx, postgresBackend, t)
		} else {
			err = j.copyToPostgres(ctx, conn, t)
		}
		if err != nil {
			return err
		}
	}
	if err := resetIdentities(ctx, tx, plan); err != nil {
		return err
	}
	results, err := compareTables(ctx, j.src, side{q: tx, kind: postgresBackend, schema: tgt})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if err := reportResults(j.lg, results); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit on %s: %w", to.display, err)
	}
	committed = true
	return nil
}

// copySource feeds at most limit normalised rows to one CopyFrom call.
type copySource struct {
	s     *rowStream
	t     *table
	row   []any
	limit int
	n     int
	done  bool
	err   error
}

func (c *copySource) Next() bool {
	if c.n >= c.limit || c.done || c.err != nil {
		return false
	}
	ok, err := c.s.next()
	if err != nil {
		c.err = fmt.Errorf("read source %s: %w", c.t.name, err)
		return false
	}
	if !ok {
		c.done = true
		return false
	}
	if err := normalizeRow(c.t, c.s.vals, c.row, true); err != nil {
		c.err = err
		return false
	}
	c.n++
	return true
}

// Values may return the same slice every row: pgx encodes it before calling
// Next again.
func (c *copySource) Values() ([]any, error) { return c.row, nil }
func (c *copySource) Err() error             { return c.err }

// copyToPostgres streams one table into the open transaction on conn with
// COPY, batch rows per CopyFrom call.
func (j *copyJob) copyToPostgres(ctx context.Context, conn *sql.Conn, t *table) error {
	p := newProgress(j.lg, t.name)
	// SQLite reads in rowid/primary-key order for free; Postgres heap order
	// is already insertion order, and ORDER BY there would cost a sort.
	s, err := openStream(ctx, j.src.q, j.src.kind, t, t.colNames(), j.src.kind == sqliteBackend)
	if err != nil {
		return err
	}
	defer s.close()
	cs := &copySource{s: s, t: t, row: make([]any, len(t.cols)), limit: j.batch}
	ident := pgx.Identifier{t.name}
	cols := t.colNames()
	err = conn.Raw(func(dc any) error {
		sc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected Postgres driver connection %T", dc)
		}
		pc := sc.Conn()
		for !cs.done {
			cs.n = 0
			n, err := pc.CopyFrom(ctx, ident, cols, cs)
			if err != nil {
				if cs.err != nil {
					return cs.err
				}
				return fmt.Errorf("COPY into %s (rows %d to %d): %w", t.name, p.n+1, p.n+int64(j.batch), err)
			}
			p.add(n)
		}
		return nil
	})
	if err != nil {
		return err
	}
	j.rows += p.n
	p.done()
	return nil
}

// resetIdentities moves every identity sequence past the copied ids, so the
// next default id is MAX(id)+1 (1 on an empty table). setval is not
// transactional; if the copy later rolls back the sequence stays moved, which
// is harmless, and a rerun sets it again.
func resetIdentities(ctx context.Context, tx *sql.Tx, plan []*table) error {
	for _, t := range plan {
		if t.serial == "" {
			continue
		}
		q := `SELECT setval(pg_get_serial_sequence($1, $2), COALESCE((SELECT MAX(` + quoteIdent(t.serial) +
			`) FROM ` + quoteIdent(t.name) + `), 0) + 1, false)`
		var v sql.NullInt64
		if err := tx.QueryRowContext(ctx, q, quoteIdent(t.name), t.serial).Scan(&v); err != nil {
			return fmt.Errorf("reset identity %s.%s: %w", t.name, t.serial, err)
		}
		if !v.Valid {
			return fmt.Errorf("reset identity %s.%s: no sequence found", t.name, t.serial)
		}
	}
	return nil
}

// ── SQLite target ────────────────────────────────────────────────────────

// intoSQLite builds the copy in <path>.partial and renames it to <path> only
// after it is verified, so <path> exists only as a complete, verified copy.
// A failure leaves the .partial file, which is never mistaken for a
// database: the next run refuses until it is deleted, and says how.
func (j *copyJob) intoSQLite(ctx context.Context, to dbRef) (err error) {
	path := to.path
	if st, err := os.Stat(path); err == nil {
		if st.IsDir() || st.Size() > 0 {
			return fmt.Errorf("target SQLite file %s already exists; refusing to overwrite. Give a path that does not exist yet", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	for _, sfx := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + sfx); err == nil {
			return fmt.Errorf("%s%s exists (left over from an earlier database at this path); refusing to create a new database next to it. Remove it or choose another path", path, sfx)
		}
	}
	partial := path + ".partial"
	cleanup := fmt.Sprintf("rm -f '%s' '%s-wal' '%s-shm' '%s-journal'", partial, partial, partial, partial)
	for _, f := range []string{partial, partial + "-wal", partial + "-shm", partial + "-journal"} {
		if _, err := os.Stat(f); err == nil {
			return fmt.Errorf("%s exists: an earlier copy into %s did not finish. It is not a usable database; delete it and rerun: %s", f, path, cleanup)
		}
	}

	db, err := sql.Open("sqlite3", dbschema.WriterDSN(partial))
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	db.SetMaxOpenConns(1)
	closed := false
	defer func() {
		if !closed {
			db.Close()
		}
		if err != nil {
			j.lg.Printf("the copy did not finish; %s was NOT created. The incomplete output is %s, which is not a usable database. Delete it before rerunning: %s", path, partial, cleanup)
		}
	}()
	if _, err := db.ExecContext(ctx, dbschema.SQLiteEndState); err != nil {
		return fmt.Errorf("create schema in %s: %w", partial, err)
	}
	tgt, err := loadSchema(ctx, db, sqliteBackend)
	if err != nil {
		return fmt.Errorf("read schema of %s: %w", partial, err)
	}
	plan, err := j.plan(ctx, tgt)
	if err != nil {
		return err
	}
	if err := checkEmpty(ctx, db, to, plan); err != nil {
		return err
	}

	for _, t := range plan {
		if bookkeepingTables[t.name] {
			err = j.inSQLiteTx(ctx, db, func(tx *sql.Tx) error { return j.upsertTable(ctx, tx, sqliteBackend, t) })
		} else {
			err = j.insertIntoSQLite(ctx, db, t)
		}
		if err != nil {
			return err
		}
	}
	if err := fixSQLiteSequence(ctx, db, plan); err != nil {
		return err
	}
	results, err := compareTables(ctx, j.src, side{q: db, kind: sqliteBackend, schema: tgt})
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if err := reportResults(j.lg, results); err != nil {
		return err
	}

	// Closing the last connection checkpoints the WAL into the file and
	// deletes it; a -wal still present would be lost by the rename.
	closed = true
	if err := db.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partial, err)
	}
	if _, err := os.Stat(partial + "-wal"); err == nil {
		return fmt.Errorf("%s-wal still exists after close; not renaming", partial)
	}
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return fmt.Errorf("target SQLite file %s appeared during the copy; refusing to overwrite it", path)
	}
	if err := os.Rename(partial, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", partial, path, err)
	}
	os.Remove(partial + "-shm") // only if a crashed reader left one
	return nil
}

func (j *copyJob) inSQLiteTx(ctx context.Context, db *sql.DB, f func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// insertIntoSQLite streams one table into the target with a prepared INSERT,
// committing every batch rows.
func (j *copyJob) insertIntoSQLite(ctx context.Context, db *sql.DB, t *table) error {
	p := newProgress(j.lg, t.name)
	s, err := openStream(ctx, j.src.q, j.src.kind, t, t.colNames(), j.src.kind == sqliteBackend)
	if err != nil {
		return err
	}
	defer s.close()
	stmt, err := db.PrepareContext(ctx, insertStmt(t))
	if err != nil {
		return fmt.Errorf("prepare insert into %s: %w", t.name, err)
	}
	defer stmt.Close()
	row := make([]any, len(t.cols))
	for done := false; !done; {
		err := j.inSQLiteTx(ctx, db, func(tx *sql.Tx) error {
			ts := tx.StmtContext(ctx, stmt)
			defer ts.Close()
			n := 0
			for ; n < j.batch; n++ {
				ok, err := s.next()
				if err != nil {
					return fmt.Errorf("read source %s: %w", t.name, err)
				}
				if !ok {
					done = true
					break
				}
				if err := normalizeRow(t, s.vals, row, false); err != nil {
					return err
				}
				if _, err := ts.ExecContext(ctx, row...); err != nil {
					return fmt.Errorf("insert into %s row %s: %w", t.name, describePK(t, s.vals), err)
				}
			}
			p.add(int64(n))
			return nil
		})
		if err != nil {
			return err
		}
	}
	j.rows += p.n
	p.done()
	return nil
}

// fixSQLiteSequence makes sqlite_sequence hold MAX(id) for every
// AUTOINCREMENT table. Inserting explicit ids already advances it; this
// states the invariant instead of relying on that.
func fixSQLiteSequence(ctx context.Context, db *sql.DB, plan []*table) error {
	for _, t := range plan {
		if t.serial == "" {
			continue
		}
		maxQ := `(SELECT MAX(` + quoteIdent(t.serial) + `) FROM ` + quoteIdent(t.name) + `)`
		res, err := db.ExecContext(ctx, `UPDATE sqlite_sequence SET seq = COALESCE(`+maxQ+`, 0) WHERE name = ?`, t.name)
		if err != nil {
			return fmt.Errorf("sqlite_sequence for %s: %w", t.name, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := db.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq) SELECT ?, `+maxQ+` WHERE `+maxQ+` IS NOT NULL`, t.name); err != nil {
				return fmt.Errorf("sqlite_sequence for %s: %w", t.name, err)
			}
		}
	}
	return nil
}
