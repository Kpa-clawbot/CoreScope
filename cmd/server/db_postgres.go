package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// Run-time parameters the server forces on every Postgres connection, sent at
// connection startup so they apply to each pooled connection.
const (
	pgReadOnlyParam    = "default_transaction_read_only"
	pgStmtTimeoutParam = "statement_timeout"
	pgIdleTxParam      = "idle_in_transaction_session_timeout"
)

// pgIdleInTxTimeout ends a session left idle inside a transaction, so a
// stuck read snapshot cannot pin old row versions (and block vacuum) for long.
const pgIdleInTxTimeout = 60 * time.Second

// pgOpenTimeout bounds each startup check in OpenPostgresDB, so a stalled
// server fails startup instead of hanging it.
const pgOpenTimeout = 30 * time.Second

// pgParam is one forced run-time parameter and its value.
type pgParam struct{ name, value string }

// pgServerParams returns the parameters the server forces: read-only
// transactions, statement_timeout (0 disables it) and the idle-in-transaction
// timeout. Times are in milliseconds, the unit pg_settings reports them in.
func pgServerParams(statementTimeoutSec int) []pgParam {
	return []pgParam{
		{pgReadOnlyParam, "on"},
		{pgStmtTimeoutParam, strconv.Itoa(statementTimeoutSec * 1000)},
		{pgIdleTxParam, strconv.FormatInt(pgIdleInTxTimeout.Milliseconds(), 10)},
	}
}

// OpenPostgresDB opens the analyzer database on Postgres, read-only, with the
// default statement timeout (dbconfig.DefaultStatementTimeoutSec).
func OpenPostgresDB(dsn string) (*DB, error) {
	return OpenPostgresDBWithStatementTimeout(dsn, dbconfig.DefaultStatementTimeoutSec)
}

// OpenPostgresDBWithStatementTimeout opens the analyzer database on Postgres,
// read-only, capping each statement at statementTimeoutSec (0: no cap).
//
// The read-only invariant (#1283) is held twice over. Every connection starts
// with default_transaction_read_only=on, so a write the server's code sends
// by mistake fails with "cannot execute ... in a read-only transaction". That
// is only a session default — a session may override it — so the role, and
// every role it can reach through membership, must also hold no write
// privilege on any relation in its schema; startup is refused otherwise.
// Errors never carry the DSN or the password: driver errors are scrubbed of
// the password before they surface.
//
// statement_timeout bounds what one request to the unauthenticated public API
// can cost the database.
//
// After those checks it follows OpenDB: schema detection on one pinned
// connection, then the prepared statements.
func OpenPostgresDBWithStatementTimeout(dsn string, statementTimeoutSec int) (*DB, error) {
	if statementTimeoutSec < 0 {
		return nil, fmt.Errorf("statement timeout is %ds; use 0 to disable it or a positive number of seconds", statementTimeoutSec)
	}
	params := pgServerParams(statementTimeoutSec)
	roDSN, password, err := pgServerDSN(dsn, params)
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
	if err := verifyPostgresSessionParams(ctx, conn, params); err != nil {
		return fail("%w", err)
	}
	if err := verifyPostgresNoWritePrivilege(ctx, conn); err != nil {
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

// verifyPostgresSessionParams checks that each forced parameter took effect.
// pgServerDSN already refuses a DSN that overrides one, but a role or database
// setting cannot, and PGOPTIONS in the environment is read by the driver
// outside the DSN; this is the backstop for all of them.
func verifyPostgresSessionParams(ctx context.Context, conn *sql.DB, params []pgParam) error {
	for _, p := range params {
		var got string
		if err := conn.QueryRowContext(ctx, `SELECT setting FROM pg_settings WHERE name = ?`, p.name).Scan(&got); err != nil {
			return fmt.Errorf("read %s: %w", p.name, err)
		}
		if got != p.value {
			return fmt.Errorf("%s is %q after connecting, want %q; remove any override of it (PGOPTIONS, or ALTER ROLE/DATABASE ... SET)", p.name, got, p.value)
		}
	}
	return nil
}

// pgWritePrivileges is the has_table_privilege list that counts as write
// power for a server major version (server_version_num). MAINTAIN (VACUUM,
// REINDEX, REFRESH MATERIALIZED VIEW, LOCK ...) exists from Postgres 17.
func pgWritePrivileges(serverVersionNum int) string {
	privs := "INSERT, UPDATE, DELETE, TRUNCATE"
	if serverVersionNum >= 170000 {
		privs += ", MAINTAIN"
	}
	return privs
}

// verifyPostgresNoWritePrivilege refuses a role that could write any relation
// in its current schema, directly or through a role it belongs to.
//
// Membership is followed through pg_auth_members to every role reachable,
// ignoring the INHERIT flags: a role that does not inherit a membership can
// still SET ROLE to it and write. Each reachable role is then checked with
// has_table_privilege / has_any_column_privilege evaluated AS that role, which
// accounts for its own inherited grants, ownership, superuser and the
// pg_write_all_data / pg_maintain predefined roles. This was chosen over
// refusing every non-pg_* membership: it is exact, so a deployment that
// grants SELECT through a group role (a common setup) still starts, and it
// needs no allowlist of predefined roles to keep current across releases.
//
// Relations are tables, partitioned tables, views (packets_v is
// auto-updatable, so INSERT on it writes transmissions), foreign tables and
// materialized views; column-level INSERT/UPDATE grants count too.
func verifyPostgresNoWritePrivilege(ctx context.Context, conn *sql.DB) error {
	var role, schema string
	var versionNum int
	if err := conn.QueryRowContext(ctx, `SELECT current_user, COALESCE(current_schema(), ''), current_setting('server_version_num')::int`).Scan(&role, &schema, &versionNum); err != nil {
		return fmt.Errorf("read current role and schema: %w", err)
	}
	if schema == "" {
		return fmt.Errorf("postgres role %q has no current schema; set search_path in db.dsn to the CoreScope schema", role)
	}

	rows, err := conn.QueryContext(ctx, `
		WITH RECURSIVE reach(oid) AS (
			SELECT oid FROM pg_roles WHERE rolname = current_user
			UNION
			SELECT m.roleid FROM pg_auth_members m JOIN reach r ON m.member = r.oid
		)
		SELECT c.relname, pg_get_userbyid(r.oid)
		FROM reach r
		CROSS JOIN pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p', 'v', 'f', 'm')
		  AND (has_table_privilege(r.oid, c.oid, ?)
		       OR has_any_column_privilege(r.oid, c.oid, 'INSERT, UPDATE'))
		ORDER BY 1, 2`, pgWritePrivileges(versionNum))
	if err != nil {
		return fmt.Errorf("check table privileges: %w", err)
	}
	defer rows.Close()
	var writable []string
	for rows.Next() {
		var rel, via string
		if err := rows.Scan(&rel, &via); err != nil {
			return fmt.Errorf("check table privileges: %w", err)
		}
		if via != role {
			rel += fmt.Sprintf(" (via role %q)", via)
		}
		writable = append(writable, rel)
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
	return fmt.Errorf("postgres role %q can write to relations in schema %q (%s); the server must connect as a read-only role "+
		"that is not a member of any role with write privileges. Create one and put it in db.dsn:\n"+
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

// pgServerDSN returns dsn with params set (replacing any value already there,
// matching keys case-insensitively as Postgres matches parameter names), plus
// the DSN's password so callers can scrub it from driver errors. It accepts
// both DSN forms libpq and pgx do: a postgres:// or postgresql:// URL, and a
// keyword=value string. An options parameter that sets one of params is an
// error rather than something to drop silently: it shows a configuration that
// expects a different value. Errors never quote the DSN.
func pgServerDSN(dsn string, params []pgParam) (string, string, error) {
	forced := make(map[string]bool, len(params))
	for _, p := range params {
		forced[p.name] = true
	}
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
		for key, vals := range q {
			lk := strings.ToLower(key)
			if forced[lk] {
				delete(q, key)
				continue
			}
			if lk == "options" {
				for _, v := range vals {
					if err := checkPGOptions(v, params); err != nil {
						return "", "", err
					}
				}
			}
		}
		for _, p := range params {
			q.Set(p.name, p.value)
		}
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
		lk := strings.ToLower(kv[0])
		if forced[lk] {
			continue
		}
		switch lk {
		case "password":
			password = kv[1]
		case "options":
			if err := checkPGOptions(kv[1], params); err != nil {
				return "", "", err
			}
		}
		writePGKeywordValue(&b, kv[0], kv[1])
	}
	for _, p := range params {
		writePGKeywordValue(&b, p.name, p.value)
	}
	return b.String(), password, nil
}

// checkPGOptions refuses an options value (command-line switches sent to the
// backend: -c name=value or --name=value) that mentions a forced parameter.
// Postgres folds dashes in --name to underscores and names are case-insensitive,
// so both are normalised before matching. The value is not quoted in the error.
func checkPGOptions(options string, params []pgParam) error {
	norm := strings.ReplaceAll(strings.ToLower(options), "-", "_")
	for _, p := range params {
		if strings.Contains(norm, p.name) {
			return fmt.Errorf("db.dsn: the options parameter sets %s, which the server sets itself; remove it from options", p.name)
		}
	}
	return nil
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

const (
	// pgStatsTTL is how long /api/perf serves Postgres database stats
	// before querying the catalog again.
	pgStatsTTL = 30 * time.Second
	// pgStatsFailTTL is how long a failed refresh is remembered: during an
	// outage /api/perf serves the last value instead of querying per request.
	pgStatsFailTTL = 5 * time.Second
	// pgStatsQueryTimeout bounds one refresh.
	pgStatsQueryTimeout = 5 * time.Second
)

// pgStatsCache holds the last good postgresDBStats result. One slot, keyed by
// the *DB it was computed for: a process opens one analyzer database, so the
// slot never thrashes in production, and a different *DB (tests) starts over
// (gen discards a refresh that finishes for the previous one).
//
// mu guards the fields only and is never held across a query: one caller
// refreshes (refreshing) while the others get the last value, flagged stale.
var pgStatsCache struct {
	mu         sync.Mutex
	db         *DB
	gen        uint64
	have       bool
	stats      SqliteStats
	at         time.Time
	failedAt   time.Time
	refreshing bool
}

// pgStatsQueryHook, when set (tests only), runs at the start of each refresh.
var pgStatsQueryHook func()

// postgresDBStats returns the /api/perf database block on Postgres: the
// database size and each main table's estimated live row count from
// pg_stat_user_tables (never COUNT(*): the tables hold tens of millions of
// rows). Postgres has no SQLite WAL file or freelist, so those stay zero.
//
// Fresh values are cached for pgStatsTTL. While a refresh is running, or for
// pgStatsFailTTL after one failed, callers get the last good value with
// Stale set (or an empty block with Stale set if there is none yet).
func (db *DB) postgresDBStats() SqliteStats {
	c := &pgStatsCache
	c.mu.Lock()
	if c.db != db {
		c.db, c.have, c.refreshing = db, false, false
		c.at, c.failedAt = time.Time{}, time.Time{}
		c.gen++
	}
	now := time.Now()
	if c.have && now.Sub(c.at) < pgStatsTTL {
		s := c.stats
		c.mu.Unlock()
		return s
	}
	if c.refreshing || (!c.failedAt.IsZero() && now.Sub(c.failedAt) < pgStatsFailTTL) {
		s := pgStatsStaleLocked()
		c.mu.Unlock()
		return s
	}
	c.refreshing = true
	gen := c.gen
	c.mu.Unlock()

	s, err := db.queryPostgresDBStats()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.refreshing = false
		if err == nil {
			c.have, c.stats, c.at, c.failedAt = true, s, time.Now(), time.Time{}
		} else {
			c.failedAt = time.Now()
		}
	}
	if err != nil {
		log.Printf("[perf] postgres database stats: %v", err)
		return pgStatsStaleLocked()
	}
	return s
}

// pgStatsStaleLocked returns the last good stats flagged stale. c.mu held.
func pgStatsStaleLocked() SqliteStats {
	if pgStatsCache.have {
		s := pgStatsCache.stats
		s.Stale = true
		return s
	}
	return SqliteStats{Engine: "postgres", WalPages: &WalPages{}, Rows: &SqliteRowCounts{}, Stale: true}
}

// queryPostgresDBStats reads the stats from the catalog. Any failure fails
// the whole refresh, so a partial result is never cached.
func (db *DB) queryPostgresDBStats() (SqliteStats, error) {
	if pgStatsQueryHook != nil {
		pgStatsQueryHook()
	}
	result := SqliteStats{Engine: "postgres", WalPages: &WalPages{}, Rows: &SqliteRowCounts{}}
	ctx, cancel := context.WithTimeout(context.Background(), pgStatsQueryTimeout)
	defer cancel()

	var size int64
	if err := db.conn.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		return result, fmt.Errorf("database size: %w", err)
	}
	result.DbSizeMB = math.Round(float64(size)/1048576*10) / 10

	rows, err := db.conn.QueryContext(ctx, `
		SELECT relname, n_live_tup
		FROM pg_stat_user_tables
		WHERE schemaname = current_schema()
		  AND relname IN ('transmissions', 'observations', 'nodes', 'observers')`)
	if err != nil {
		return result, fmt.Errorf("row estimates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return result, fmt.Errorf("row estimates: %w", err)
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
		return result, fmt.Errorf("row estimates: %w", err)
	}
	return result, nil
}
