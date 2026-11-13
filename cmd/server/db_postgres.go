package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// pgReadOnlyParam is the Postgres run-time parameter OpenPostgresDB sends at
// connection startup, so every pooled connection starts read-only.
const pgReadOnlyParam = "default_transaction_read_only"

// pgOpenTimeout bounds each startup check in OpenPostgresDB, so a stalled
// server fails startup instead of hanging it.
const pgOpenTimeout = 30 * time.Second

// OpenPostgresDB opens the analyzer database on Postgres, read-only.
//
// The read-only invariant (#1283) is held twice over. Every connection starts
// with default_transaction_read_only=on, so a write the server's code sends
// by mistake fails with "cannot execute ... in a read-only transaction". That
// is only a session default — a session may override it — so the role must
// also hold no INSERT/UPDATE/DELETE/TRUNCATE privilege on any table in its
// schema; startup is refused if it does. Errors never carry the DSN or the
// password: driver errors are scrubbed of the password before they surface.
//
// After those checks it follows OpenDB: schema detection on one pinned
// connection, then the prepared statements.
func OpenPostgresDB(dsn string) (*DB, error) {
	roDSN, password, err := pgDSNWithReadOnly(dsn)
	if err != nil {
		return nil, err
	}
	scrub := func(err error) error { return scrubPGSecret(err, password) }

	conn, err := sql.Open(dbx.PostgresDriverName, roDSN)
	if err != nil {
		return nil, scrub(err)
	}
	conn.SetMaxOpenConns(8)
	conn.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), pgOpenTimeout)
	defer cancel()
	fail := func(format string, err error) (*DB, error) {
		conn.Close()
		return nil, fmt.Errorf(format, scrub(err))
	}

	if err := conn.PingContext(ctx); err != nil {
		return fail("ping failed: %w", err)
	}
	if err := pgschema.AssertReady(ctx, conn); err != nil {
		return fail("%w", err)
	}
	if err := verifyPostgresReadOnly(ctx, conn); err != nil {
		return fail("%w", err)
	}

	d := &DB{conn: conn, pg: true, path: ""}
	// Same order and reasons as OpenDB (#1901): detect on one pinned
	// connection and fail loudly, then prepare against the detected schema.
	sc, err := conn.Conn(ctx)
	if err != nil {
		return fail("schema detection: acquire connection: %w", err)
	}
	derr := d.detectSchema(ctx, sc)
	_ = sc.Close()
	if derr != nil {
		return fail("schema detection failed: %w", derr)
	}
	if err := d.prepareStatements(); err != nil {
		return fail("prepare statements: %w", err)
	}
	return d, nil
}

// verifyPostgresReadOnly checks that the session defaults to read-only
// transactions and that the connected role cannot write any table in its
// current schema.
func verifyPostgresReadOnly(ctx context.Context, conn *sql.DB) error {
	var ro string
	if err := conn.QueryRowContext(ctx, `SHOW default_transaction_read_only`).Scan(&ro); err != nil {
		return fmt.Errorf("read default_transaction_read_only: %w", err)
	}
	if ro != "on" {
		return fmt.Errorf("default_transaction_read_only is %q after connecting, want \"on\"; remove any override of it from db.dsn and from the role or database settings", ro)
	}

	var role, schema string
	if err := conn.QueryRowContext(ctx, `SELECT current_user, COALESCE(current_schema(), '')`).Scan(&role, &schema); err != nil {
		return fmt.Errorf("read current role and schema: %w", err)
	}
	if schema == "" {
		return fmt.Errorf("postgres role %q has no current schema; set search_path in db.dsn to the CoreScope schema", role)
	}

	// has_table_privilege with a comma-separated list is true when any one is
	// held, and it accounts for role membership, ownership and superuser —
	// which information_schema grants alone would miss.
	rows, err := conn.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p')
		  AND has_table_privilege(c.oid, 'INSERT, UPDATE, DELETE, TRUNCATE')
		ORDER BY c.relname`)
	if err != nil {
		return fmt.Errorf("check table privileges: %w", err)
	}
	defer rows.Close()
	var writable []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("check table privileges: %w", err)
		}
		writable = append(writable, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check table privileges: %w", err)
	}
	if len(writable) == 0 {
		return nil
	}

	// The tables' owner is the ingestor's role in a standard deployment; it
	// is what ALTER DEFAULT PRIVILEGES must name so new tables stay readable.
	owner := "<ingestor_role>"
	var o string
	if err := conn.QueryRowContext(ctx, `
		SELECT pg_get_userbyid(c.relowner)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = 'schema_version'`).Scan(&o); err == nil {
		owner = pgQuoteIdent(o)
	}
	shown := writable
	if len(shown) > 5 {
		shown = append(append([]string{}, shown[:5]...), fmt.Sprintf("and %d more", len(writable)-5))
	}
	s := pgQuoteIdent(schema)
	return fmt.Errorf("postgres role %q can write to tables in schema %q (%s); the server must connect as a read-only role. "+
		"Create one and put it in db.dsn:\n"+
		"  CREATE ROLE corescope_reader LOGIN PASSWORD '...';\n"+
		"  GRANT USAGE ON SCHEMA %s TO corescope_reader;\n"+
		"  GRANT SELECT ON ALL TABLES IN SCHEMA %s TO corescope_reader;\n"+
		"  ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON TABLES TO corescope_reader;",
		role, schema, strings.Join(shown, ", "), s, s, owner, s)
}

// pgQuoteIdent quotes a Postgres identifier for display in a SQL hint.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgDSNWithReadOnly returns dsn with default_transaction_read_only=on set
// (replacing any value already there), plus the DSN's password so callers can
// scrub it from driver errors. It accepts both DSN forms libpq and pgx do: a
// postgres:// or postgresql:// URL, and a keyword=value string. Errors never
// quote the DSN.
func pgDSNWithReadOnly(dsn string) (string, string, error) {
	trimmed := strings.TrimSpace(dsn)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			// Not wrapped: url.Error echoes the DSN, password included.
			return "", "", errors.New("db.dsn: unparseable postgres URL")
		}
		password := ""
		if u.User != nil {
			password, _ = u.User.Password()
		}
		q := u.Query()
		q.Set(pgReadOnlyParam, "on")
		u.RawQuery = q.Encode()
		return u.String(), password, nil
	}

	pairs, err := parsePGKeywordValue(trimmed)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	password := ""
	for _, kv := range pairs {
		switch kv[0] {
		case pgReadOnlyParam:
			continue
		case "password":
			password = kv[1]
		}
		writePGKeywordValue(&b, kv[0], kv[1])
	}
	writePGKeywordValue(&b, pgReadOnlyParam, "on")
	return b.String(), password, nil
}

// writePGKeywordValue appends key='value', single-quoted and escaped the way
// libpq reads it, so values with spaces or quotes survive the round trip.
func writePGKeywordValue(b *strings.Builder, key, value string) {
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(key)
	b.WriteString("='")
	b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value))
	b.WriteByte('\'')
}

// parsePGKeywordValue splits a libpq keyword=value connection string into
// ordered pairs: whitespace separates pairs, whitespace may surround '=', and
// a value is either bare or single-quoted, with backslash escaping the next
// character in both. Errors name the problem, never the input.
func parsePGKeywordValue(s string) ([][2]string, error) {
	var out [][2]string
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' }
	i := 0
	for {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			return out, nil
		}
		start := i
		for i < len(s) && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		key := s[start:i]
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if key == "" || i >= len(s) || s[i] != '=' {
			return nil, errors.New("db.dsn: malformed keyword=value connection string (expected keyword=value)")
		}
		i++
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		var val strings.Builder
		if i < len(s) && s[i] == '\'' {
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					val.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '\'' {
					closed = true
					i++
					break
				}
				val.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("db.dsn: unterminated quoted value in connection string")
			}
		} else {
			for i < len(s) && !isSpace(s[i]) {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				val.WriteByte(s[i])
				i++
			}
		}
		out = append(out, [2]string{key, val.String()})
	}
}

// pgSecretError carries a driver error whose text had the password removed.
// It still unwraps to the original, so errors.As (e.g. *pgconn.PgError) works.
type pgSecretError struct {
	msg string
	err error
}

func (e *pgSecretError) Error() string { return e.msg }
func (e *pgSecretError) Unwrap() error { return e.err }

// scrubPGSecret returns err with every occurrence of password in its text
// replaced. pgx's own errors omit the password, but this keeps that true
// whatever a future driver version or a wrapped error decides to print.
func scrubPGSecret(err error, password string) error {
	if err == nil || password == "" || !strings.Contains(err.Error(), password) {
		return err
	}
	return &pgSecretError{msg: strings.ReplaceAll(err.Error(), password, "********"), err: err}
}

// pgStatsTTL is how long /api/perf serves Postgres database stats before
// querying the catalog again.
const pgStatsTTL = 30 * time.Second

// pgStatsCache holds the last postgresDBStats result. One slot, keyed by the
// *DB it was computed for: a process opens one analyzer database, so the slot
// never thrashes in production, and a different *DB (tests) just recomputes.
var pgStatsCache struct {
	mu    sync.Mutex
	db    *DB
	at    time.Time
	stats SqliteStats
}

// postgresDBStats returns the /api/perf database block on Postgres: the
// database size and each main table's estimated live row count from
// pg_stat_user_tables (never COUNT(*): the tables hold tens of millions of
// rows). Postgres has no SQLite WAL file or freelist, so those stay zero.
// Cached for pgStatsTTL; a failed query is logged and not cached.
func (db *DB) postgresDBStats() SqliteStats {
	pgStatsCache.mu.Lock()
	defer pgStatsCache.mu.Unlock()
	if pgStatsCache.db == db && time.Since(pgStatsCache.at) < pgStatsTTL {
		return pgStatsCache.stats
	}

	result := SqliteStats{Engine: "postgres", WalPages: &WalPages{}, Rows: &SqliteRowCounts{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failed := false

	var size int64
	if err := db.conn.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		log.Printf("[perf] postgres database size: %v", err)
		failed = true
	} else {
		result.DbSizeMB = math.Round(float64(size)/1048576*10) / 10
	}

	rows, err := db.conn.QueryContext(ctx, `
		SELECT relname, n_live_tup
		FROM pg_stat_user_tables
		WHERE schemaname = current_schema()
		  AND relname IN ('transmissions', 'observations', 'nodes', 'observers')`)
	if err != nil {
		log.Printf("[perf] postgres row estimates: %v", err)
		failed = true
	} else {
		for rows.Next() {
			var name string
			var n int64
			if err := rows.Scan(&name, &n); err != nil {
				failed = true
				break
			}
			switch name {
			case "transmissions":
				result.Rows.Transmissions = int(n)
			case "observations":
				result.Rows.Observations = int(n)
			case "nodes":
				result.Rows.Nodes = int(n)
			case "observers":
				result.Rows.Observers = int(n)
			}
		}
		if err := rows.Err(); err != nil {
			log.Printf("[perf] postgres row estimates: %v", err)
			failed = true
		}
		rows.Close()
	}

	if !failed {
		pgStatsCache.db = db
		pgStatsCache.at = time.Now()
		pgStatsCache.stats = result
	}
	return result
}
