package main

// Accounts in PostgreSQL (userManagement.dsn / DB_DSN_ACCOUNTS): the account
// store gets its own credential, separate from the measurement reader's. That
// credential can write (it owns schema corescope_users), so "the server never
// writes measurement data" (#1283) only stays true if it can write nothing
// else: verifyAccountsPGRole refuses startup otherwise.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// accountsPGCheckTimeout bounds the startup privilege check.
const accountsPGCheckTimeout = 30 * time.Second

// accountsPGRole is the role a users.OpenPostgres store will log in as: the
// DSN's user, or what pgx falls back to (PGUSER, then the OS user), as the
// store resolves it. Errors never quote the DSN.
func accountsPGRole(dsn string) (string, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", errors.New("userManagement.dsn: unparseable Postgres DSN")
	}
	if cfg.User == "" {
		return "", errors.New("userManagement.dsn: no user to connect as")
	}
	return cfg.User, nil
}

// pgReachableFrom is pgReachableRoles for a named role instead of the
// session's: the role and every role it is a member of, transitively,
// INHERIT ignored (SET ROLE works without it). Its one placeholder is the
// role name. A role the measurement cluster does not know reaches nothing.
const pgReachableFrom = `
	WITH RECURSIVE reach(oid) AS (
		SELECT oid FROM pg_roles WHERE rolname = ?
		UNION
		SELECT m.roleid FROM pg_auth_members m JOIN reach r ON m.member = r.oid
	)`

// accountsWritePowerChecks are the ways a role reachable from the accounts
// role could write the measurement schema (the measurement pool's
// current_schema()) or the control schema, or create a schema in the
// measurement database. Placeholders: the role name, then
// the write privilege list when args is set.
var accountsWritePowerChecks = []struct {
	what  string
	query string
	args  bool
}{
	{
		what: "role attributes",
		query: pgReachableFrom + `
		SELECT format('role %I has %s', p.rolname, concat_ws(', ',
			CASE WHEN p.rolsuper THEN 'SUPERUSER' END,
			CASE WHEN p.rolcreaterole THEN 'CREATEROLE' END))
		FROM reach r JOIN pg_roles p ON p.oid = r.oid
		WHERE p.rolsuper OR p.rolcreaterole
		ORDER BY 1`,
	},
	{
		// Owning the database, or CREATE on it, lets the role create a
		// schema named after the ingestor's writer role, first in the
		// writer's default search_path ("$user"), and plant tables or
		// triggers there that the writer writes through.
		// has_database_privilege counts PUBLIC's grants. users.OpenPostgres
		// refuses the same in the database it connects to.
		what: "database privileges",
		query: pgReachableFrom + `
		SELECT format('role %I %s database %I', pg_get_userbyid(r.oid),
			CASE WHEN d.datdba = r.oid THEN 'owns' ELSE 'can CREATE in' END, d.datname)
		FROM reach r
		JOIN pg_database d ON d.datname = current_database()
		WHERE d.datdba = r.oid OR has_database_privilege(r.oid, d.oid, 'CREATE')
		ORDER BY 1`,
	},
	{
		// Owning, or creating in, either schema would let the role plant a
		// table or function there that the ingestor or the server uses.
		what: "schema privileges",
		query: pgReachableFrom + `
		SELECT format('role %I %s schema %I', pg_get_userbyid(r.oid),
			CASE WHEN n.nspowner = r.oid THEN 'owns' ELSE 'can CREATE in' END, n.nspname)
		FROM reach r
		CROSS JOIN pg_namespace n
		WHERE n.nspname IN (current_schema(), '` + pgControlSchema + `')
		  AND (n.nspowner = r.oid OR has_schema_privilege(r.oid, n.oid, 'CREATE'))
		ORDER BY 1`,
	},
	{
		// has_table_privilege counts ownership, inherited and PUBLIC grants
		// and the predefined roles; column grants are checked on their own.
		what: "table privileges",
		query: pgReachableFrom + `
		SELECT format('can write %I.%I', n.nspname, c.relname) ||
			CASE WHEN pg_get_userbyid(r.oid) = ? THEN '' ELSE format(' (via role %I)', pg_get_userbyid(r.oid)) END
		FROM reach r
		CROSS JOIN pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN (current_schema(), '` + pgControlSchema + `')
		  AND c.relkind IN ('r', 'p', 'v', 'f', 'm')
		  AND (has_table_privilege(r.oid, c.oid, ?)
		       OR has_any_column_privilege(r.oid, c.oid, 'INSERT, UPDATE'))
		ORDER BY 1`,
		args: true,
	},
	{
		what: "sequence privileges",
		query: pgReachableFrom + `
		SELECT format('can advance or set sequence %I.%I', n.nspname, c.relname)
		FROM reach r
		CROSS JOIN pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN (current_schema(), '` + pgControlSchema + `')
		  AND c.relkind = 'S'
		  AND has_sequence_privilege(r.oid, c.oid, 'UPDATE, USAGE')
		ORDER BY 1`,
	},
}

// verifyAccountsPGRole refuses an accounts role that is the measurement
// reader role, or that could write the measurement schema or the control
// schema as itself or any role it can reach. It runs on the measurement
// pool (read-only; catalog reads only), which sees those schemas whichever
// database the accounts DSN names; roles are cluster-wide, so a role of
// that name on another cluster is judged by its namesake here.
func verifyAccountsPGRole(ctx context.Context, conn *sql.DB, accountsRole string) error {
	var role, sessionRole, schema string
	var versionNum int
	if err := conn.QueryRowContext(ctx, `SELECT current_user, session_user, COALESCE(current_schema(), ''), current_setting('server_version_num')::int`).
		Scan(&role, &sessionRole, &schema, &versionNum); err != nil {
		return fmt.Errorf("accounts role check: read the measurement role: %w", err)
	}
	if accountsRole == role || accountsRole == sessionRole {
		return fmt.Errorf("userManagement.dsn connects as %q, the measurement reader role: give the account store its own role "+
			"(one that owns schema corescope_users and can write nothing else), so the server's measurement connection stays read-only", accountsRole)
	}
	var findings []string
	for _, chk := range accountsWritePowerChecks {
		args := []any{accountsRole}
		if chk.args {
			args = append(args, accountsRole, pgWritePrivileges(versionNum))
		}
		got, err := pgQueryStrings(ctx, conn, chk.query, args...)
		if err != nil {
			return fmt.Errorf("accounts role check: %s: %w", chk.what, err)
		}
		findings = append(findings, got...)
	}
	if len(findings) == 0 {
		return nil
	}
	return fmt.Errorf("userManagement.dsn connects as postgres role %q, which could write outside the account store: %s. "+
		"The accounts role may own schema corescope_users and nothing else: revoke these (and any membership granting them), "+
		"so the server cannot write measurement data (schema %q) or the control tables (schema %q)",
		accountsRole, strings.Join(pgFirstN(findings, 8), "; "), schema, pgControlSchema)
}

// checkAccountsPG runs verifyAccountsPGRole when the measurement database is
// on Postgres. On SQLite there is nothing in Postgres the accounts role
// could write but its own schema, and the file is opened mode=ro.
func (s *Server) checkAccountsPG(dsn string) error {
	if s.db == nil || !s.db.isPostgres() {
		return nil
	}
	role, err := accountsPGRole(dsn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountsPGCheckTimeout)
	defer cancel()
	return verifyAccountsPGRole(ctx, s.db.conn, role)
}
