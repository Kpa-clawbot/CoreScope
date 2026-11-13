package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"slices"
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
// The one exception is the control schema (control_pg.go): the column-level
// INSERT the group role corescope_control_client holds there is allowed, and
// when the role inherits that group with exactly the control grants a second,
// small pool with read-only transactions forced off is opened for the control
// tables alone. Without it (role absent, not inherited, or missing a grant)
// the server still starts, with geo-prune and multibyte capability publishing
// disabled; a control role holding more than its exact grants refuses startup.
//
// After those checks it follows OpenDB: schema detection on one pinned
// connection, then the prepared statements, then the control pool.
func OpenPostgresDBWithStatementTimeout(dsn string, statementTimeoutSec int) (*DB, error) {
	if statementTimeoutSec < 0 {
		return nil, fmt.Errorf("statement timeout is %ds; use 0 to disable it or a positive number of seconds", statementTimeoutSec)
	}
	params := pgServerParams(statementTimeoutSec)
	roDSN, secrets, err := pgServerDSN(dsn, params)
	if err != nil {
		return nil, err
	}
	scrub := func(err error) error { return scrubPGSecret(err, secrets...) }

	conn, err := sql.Open(dbx.PostgresDriverName, roDSN)
	if err != nil {
		return nil, scrub(err)
	}
	conn.SetMaxOpenConns(8)
	conn.SetMaxIdleConns(4)

	fail := func(format string, err error) (*DB, error) {
		conn.Close()
		return nil, fmt.Errorf(format, scrub(err))
	}

	// The first connect gets its own budget so waiting for the network does
	// not eat into the checks below.
	pingCtx, pingCancel := context.WithTimeout(context.Background(), pgConnectWait+pgOpenTimeout)
	err = pingPostgres(pingCtx, conn)
	pingCancel()
	if err != nil {
		return fail("ping failed: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pgOpenTimeout)
	defer cancel()
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
	if err := d.openPostgresControl(ctx, dsn, statementTimeoutSec); err != nil {
		return fail("%w", err)
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
// power for a server major version (server_version_num). TRIGGER lets a role
// attach a trigger that runs as whoever writes the table next (the ingestor);
// MAINTAIN (VACUUM, REINDEX, REFRESH MATERIALIZED VIEW, LOCK ...) exists from
// Postgres 17.
func pgWritePrivileges(serverVersionNum int) string {
	privs := "INSERT, UPDATE, DELETE, TRUNCATE, TRIGGER"
	if serverVersionNum >= 170000 {
		privs += ", MAINTAIN"
	}
	return privs
}

// pgReachableRoles is a CTE naming every role the session can act as: the
// session user and the current user (which differ when the role parameter
// was set by options or ALTER ROLE ... SET role), plus every role either is a
// member of, transitively. INHERIT flags are ignored on purpose: a role that
// does not inherit a membership can still SET ROLE to it.
const pgReachableRoles = `
	WITH RECURSIVE reach(oid) AS (
		SELECT oid FROM pg_roles WHERE rolname IN (current_user, session_user)
		UNION
		SELECT m.roleid FROM pg_auth_members m JOIN reach r ON m.member = r.oid
	)`

// pgControlInsertTables and pgControlInsertColumns are the SQL lists of the
// one write the control role may hold in the control schema: column-level
// INSERT on these columns of these tables (pgschema.EnsureControlGrants).
const (
	pgControlInsertTables  = `'prune_requests', 'mbcap_snapshots'`
	pgControlInsertColumns = `'id', 'payload'`
)

// pgWritePowerChecks are the ways a reachable role could write, each a query
// on pgReachableRoles returning one finding per row. Placeholders: the write
// privilege list (pgWritePrivileges).
var pgWritePowerChecks = []struct {
	what  string
	query string
	args  bool // takes the privilege list as its one argument
}{
	{
		// Role attributes are not inherited, but each is usable after
		// SET ROLE: SUPERUSER bypasses every check, CREATEROLE can create
		// and grant roles, BYPASSRLS ignores row-level security.
		what: "role attributes",
		query: pgReachableRoles + `
		SELECT format('role %I has %s', p.rolname, concat_ws(', ',
			CASE WHEN p.rolsuper THEN 'SUPERUSER' END,
			CASE WHEN p.rolcreaterole THEN 'CREATEROLE' END,
			CASE WHEN p.rolbypassrls THEN 'BYPASSRLS' END))
		FROM reach r JOIN pg_roles p ON p.oid = r.oid
		WHERE p.rolsuper OR p.rolcreaterole OR p.rolbypassrls
		ORDER BY 1`,
	},
	{
		// Owning, or creating objects in, a schema on the search path lets a
		// role plant a function or operator that shadows one the ingestor
		// calls, which then runs as the ingestor. pg_catalog is searched
		// first and only superusers can create in it; the session's own temp
		// schema holds nothing another session resolves.
		what: "schema privileges",
		query: pgReachableRoles + `
		SELECT format('role %I %s schema %I', pg_get_userbyid(r.oid),
			CASE WHEN n.nspowner = r.oid THEN 'owns' ELSE 'can CREATE in' END, n.nspname)
		FROM reach r
		CROSS JOIN pg_namespace n
		WHERE n.nspname = ANY (current_schemas(true))
		  AND n.nspname <> 'pg_catalog'
		  AND n.oid <> pg_my_temp_schema()
		  AND (n.nspowner = r.oid OR has_schema_privilege(r.oid, n.oid, 'CREATE'))
		ORDER BY 1`,
	},
	{
		// Each reachable role is checked AS that role, which accounts for its
		// inherited grants, ownership and the pg_write_all_data / pg_maintain
		// predefined roles. Relations are tables, partitioned tables, views
		// (packets_v is auto-updatable: INSERT on it writes transmissions),
		// foreign tables and materialized views; column-level INSERT/UPDATE
		// grants count too.
		what: "table privileges",
		query: pgReachableRoles + `
		SELECT CASE WHEN pg_get_userbyid(r.oid) = current_user THEN c.relname
			ELSE format('%s (via role %I)', c.relname, pg_get_userbyid(r.oid)) END
		FROM reach r
		CROSS JOIN pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p', 'v', 'f', 'm')
		  AND (has_table_privilege(r.oid, c.oid, ?)
		       OR has_any_column_privilege(r.oid, c.oid, 'INSERT, UPDATE'))
		ORDER BY 1`,
		args: true,
	},
	{
		// A sequence is writable state: UPDATE allows setval, USAGE allows
		// nextval (an identity or serial column's counter, which the ingestor
		// owns). SELECT alone only reads the counter.
		what: "sequence privileges",
		query: pgReachableRoles + `
		SELECT CASE WHEN pg_get_userbyid(r.oid) = current_user THEN c.relname
			ELSE format('%s (via role %I)', c.relname, pg_get_userbyid(r.oid)) END
		FROM reach r
		CROSS JOIN pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind = 'S'
		  AND has_sequence_privilege(r.oid, c.oid, 'UPDATE, USAGE')
		ORDER BY 1`,
	},
	{
		// The control schema is written through membership in pgControlRole
		// only (control_pg.go), and only as far as the exact grants
		// pgschema.EnsureControlGrants gives it: column-level INSERT on
		// (id, payload) of the two request tables (pgControlInsertColumns).
		// Nothing else counts as allowed, for any reachable role, the
		// control role included, or PUBLIC: a table or column grant,
		// USAGE on a sequence, ownership of a table or of the schema,
		// CREATE on it refuses startup like a write grant on the
		// measurement schema would.
		what: "control schema privileges",
		query: pgReachableRoles + `,
		holders(oid) AS (
			SELECT oid FROM reach
			UNION SELECT 0::oid
		),
		privs(p) AS (SELECT unnest(string_to_array(replace(?, ' ', ''), ',') || ARRAY['USAGE'])),
		ctlrole(oid) AS (SELECT oid FROM pg_roles WHERE rolname = '` + pgControlRole + `')
		SELECT format('%s %s on %I.%I', CASE WHEN a.grantee = 0 THEN 'PUBLIC has' ELSE format('role %I has', pg_get_userbyid(a.grantee)) END,
			a.privilege_type, n.nspname, c.relname)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL aclexplode(c.relacl) a
		WHERE n.nspname = '` + pgControlSchema + `'
		  AND a.grantee IN (SELECT oid FROM holders)
		  AND a.privilege_type IN (SELECT p FROM privs)
		UNION ALL
		SELECT format('%s %s on column %I.%I.%I', CASE WHEN a.grantee = 0 THEN 'PUBLIC has' ELSE format('role %I has', pg_get_userbyid(a.grantee)) END,
			a.privilege_type, n.nspname, c.relname, t.attname)
		FROM pg_attribute t
		JOIN pg_class c ON c.oid = t.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL aclexplode(t.attacl) a
		WHERE n.nspname = '` + pgControlSchema + `'
		  AND a.grantee IN (SELECT oid FROM holders)
		  AND a.privilege_type IN ('INSERT', 'UPDATE')
		  AND NOT (a.privilege_type = 'INSERT'
		       AND a.grantee IN (SELECT oid FROM ctlrole)
		       AND c.relname IN (` + pgControlInsertTables + `)
		       AND t.attname IN (` + pgControlInsertColumns + `))
		UNION ALL
		SELECT format('role %I owns %I.%I', pg_get_userbyid(c.relowner), n.nspname, c.relname)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = '` + pgControlSchema + `'
		  AND c.relowner IN (SELECT oid FROM holders)
		UNION ALL
		SELECT format('role %I owns schema %I', pg_get_userbyid(n.nspowner), n.nspname)
		FROM pg_namespace n
		WHERE n.nspname = '` + pgControlSchema + `'
		  AND n.nspowner IN (SELECT oid FROM holders)
		UNION ALL
		SELECT format('%s CREATE on schema %I', CASE WHEN a.grantee = 0 THEN 'PUBLIC has' ELSE format('role %I has', pg_get_userbyid(a.grantee)) END, n.nspname)
		FROM pg_namespace n
		CROSS JOIN LATERAL aclexplode(n.nspacl) a
		WHERE n.nspname = '` + pgControlSchema + `'
		  AND a.grantee IN (SELECT oid FROM holders)
		  AND a.privilege_type = 'CREATE'
		ORDER BY 1`,
		args: true,
	},
}

// verifyPostgresNoWritePrivilege refuses a session that could gain write
// power over the CoreScope database, as any role it can act as (see
// pgReachableRoles): a write privilege on a relation in its schema, a
// dangerous role attribute, ownership of / CREATE on a schema it searches, or
// write power on the control schema beyond the control role's exact column
// INSERT, or UPDATE/USAGE on a sequence.
//
// Reachable roles are each checked exactly, rather than refusing every
// membership outside an allowlist of predefined roles: a deployment that
// grants SELECT through a group role (a common setup) still starts, and no
// allowlist has to track new predefined roles across releases.
func verifyPostgresNoWritePrivilege(ctx context.Context, conn *sql.DB) error {
	var role, sessionRole, schema string
	var versionNum int
	if err := conn.QueryRowContext(ctx, `SELECT current_user, session_user, COALESCE(current_schema(), ''), current_setting('server_version_num')::int`).Scan(&role, &sessionRole, &schema, &versionNum); err != nil {
		return fmt.Errorf("read current role and schema: %w", err)
	}
	if schema == "" {
		return fmt.Errorf("postgres role %q has no current schema; set search_path in db.dsn to the CoreScope schema", role)
	}

	var findings []string
	for _, chk := range pgWritePowerChecks {
		var args []any
		if chk.args {
			args = append(args, pgWritePrivileges(versionNum))
		}
		got, err := pgQueryStrings(ctx, conn, chk.query, args...)
		if err != nil {
			return fmt.Errorf("check %s: %w", chk.what, err)
		}
		if len(got) > 0 {
			switch chk.what {
			case "table privileges":
				got = []string{"can write " + strings.Join(pgFirstN(got, 5), ", ")}
			case "sequence privileges":
				got = []string{"can advance or set sequence " + strings.Join(pgFirstN(got, 5), ", ")}
			}
		}
		findings = append(findings, got...)
	}
	if len(findings) == 0 {
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
	who := fmt.Sprintf("postgres role %q", role)
	if sessionRole != role {
		who = fmt.Sprintf("postgres session user %q (current role %q)", sessionRole, role)
	}
	s := pgQuoteIdent(schema)
	return fmt.Errorf("%s could gain write access to schema %q: %s. The server must connect as a read-only role "+
		"with no SUPERUSER/CREATEROLE/BYPASSRLS, no ownership of or CREATE on any schema in its search_path, and no "+
		"membership in a role with write privileges. Create one and put it in db.dsn:\n"+
		"  CREATE ROLE corescope_reader LOGIN PASSWORD '...';\n"+
		"  GRANT USAGE ON SCHEMA %s TO corescope_reader;\n"+
		"  GRANT SELECT ON ALL TABLES IN SCHEMA %s TO corescope_reader;\n"+
		"  ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON TABLES TO corescope_reader;\n"+
		"and if PUBLIC can create in a schema on the path (the public schema before Postgres 15): REVOKE CREATE ON SCHEMA public FROM PUBLIC;",
		who, schema, strings.Join(pgFirstN(findings, 8), "; "), s, s, owner, s)
}

// pgQueryStrings runs a query returning one text column and collects it.
func pgQueryStrings(ctx context.Context, conn *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// pgFirstN returns at most n items, summarising the rest as "and K more".
func pgFirstN(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(append([]string{}, items[:n]...), fmt.Sprintf("and %d more", len(items)-n))
}

// pgQuoteIdent quotes a Postgres identifier for display in a SQL hint.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgServerDSN returns dsn with params set (replacing any value already there,
// matching keys case-insensitively as Postgres matches parameter names), plus
// every password the DSN carries (URL userinfo, a password= query parameter,
// or a password keyword), raw and percent-encoded, so callers can scrub them
// from driver errors. It accepts
// both DSN forms libpq and pgx do: a postgres:// or postgresql:// URL, and a
// keyword=value string. An options parameter that sets one of params is an
// error rather than something to drop silently: it shows a configuration that
// expects a different value. Errors never quote the DSN.
func pgServerDSN(dsn string, params []pgParam) (string, []string, error) {
	forced := make(map[string]bool, len(params))
	for _, p := range params {
		forced[p.name] = true
	}
	trimmed := strings.TrimSpace(dsn)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			// Not wrapped: url.Error echoes the DSN, password included.
			return "", nil, errors.New("db.dsn: unparseable postgres URL")
		}
		var secrets []string
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				secrets = pgSecretForms(secrets, pw)
			}
		}
		q := u.Query()
		for key, vals := range q {
			lk := strings.ToLower(key)
			if forced[lk] {
				delete(q, key)
				continue
			}
			switch lk {
			case "password":
				for _, v := range vals {
					secrets = pgSecretForms(secrets, v)
				}
			case "options":
				for _, v := range vals {
					if err := checkPGOptions(v, params); err != nil {
						return "", nil, err
					}
				}
			}
		}
		for _, p := range params {
			q.Set(p.name, p.value)
		}
		u.RawQuery = q.Encode()
		return u.String(), secrets, nil
	}

	pairs, err := parsePGKeywordValue(trimmed)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	var secrets []string
	for _, kv := range pairs {
		lk := strings.ToLower(kv[0])
		if forced[lk] {
			continue
		}
		switch lk {
		case "password":
			secrets = pgSecretForms(secrets, kv[1])
		case "options":
			if err := checkPGOptions(kv[1], params); err != nil {
				return "", nil, err
			}
		}
		writePGKeywordValue(&b, kv[0], kv[1])
	}
	for _, p := range params {
		writePGKeywordValue(&b, p.name, p.value)
	}
	return b.String(), secrets, nil
}

// pgSecretForms appends pw and its percent-encoded spellings (what a URL DSN
// echoed verbatim would contain) to secrets, skipping empty and repeated ones.
func pgSecretForms(secrets []string, pw string) []string {
	for _, f := range []string{pw, url.QueryEscape(pw), url.PathEscape(pw), url.UserPassword("", pw).String()[1:]} {
		if f == "" || slices.Contains(secrets, f) {
			continue
		}
		secrets = append(secrets, f)
	}
	return secrets
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

// scrubPGSecret returns err with every occurrence of each secret in its text
// replaced. pgx's own errors omit the password, but this keeps that true
// whatever a future driver version or a wrapped error decides to print.
// Longer secrets are replaced first so one that contains another is not left
// half-redacted.
func scrubPGSecret(err error, secrets ...string) error {
	if err == nil {
		return err
	}
	sorted := slices.Clone(secrets)
	slices.SortFunc(sorted, func(a, b string) int { return len(b) - len(a) })
	msg := err.Error()
	for _, s := range sorted {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "********")
		}
	}
	if msg == err.Error() {
		return err
	}
	return &pgSecretError{msg: msg, err: err}
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

// pgConnectWait bounds how long a first connect rides out a refused or
// unresolvable endpoint (a new pod's NetworkPolicy not admitting it yet).
const pgConnectWait = 30 * time.Second

func pingPostgres(ctx context.Context, conn *sql.DB) error {
	err := conn.PingContext(ctx)
	if err == nil || !dbx.IsTransientConnError(err) {
		return err
	}
	log.Printf("[db] postgres not reachable yet, retrying for up to %s", pgConnectWait)
	return dbx.PingWithRetry(ctx, conn, pgConnectWait)
}
