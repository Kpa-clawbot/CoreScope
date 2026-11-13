package pgschema

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// controlRole is the NOLOGIN group role scripts/postgres-roles.sh grants to
// the reader: membership is the server's only write access, and only to the
// control tables (migration 0002).
const controlRole = "corescope_control_client"

// controlSchema holds the server -> ingestor request tables.
const controlSchema = "corescope_control"

// controlTablePrivs is every privilege controlRole holds on a control table.
// Anything else it held there is revoked by EnsureControlGrants.
var controlTablePrivs = []struct {
	table string
	privs []string
}{
	{"prune_requests", []string{"INSERT", "SELECT"}},
	{"prune_results", []string{"SELECT"}},
	{"mbcap_snapshots", []string{"INSERT", "SELECT"}},
}

// EnsureControlGrants gives the role corescope_control_client exactly USAGE on
// schema corescope_control, SELECT, INSERT on prune_requests and
// mbcap_snapshots and SELECT on prune_results, revoking anything else that
// role or PUBLIC holds on them. It returns nil, doing nothing, when the role
// does not exist (scripts/postgres-roles.sh creates it, and grants the same
// privileges itself when the tables already exist, so the two may run in
// either order). The ingestor runs it after Apply at every start, as the
// writer, which owns the control tables. Idempotent.
func EnsureControlGrants(ctx context.Context, db *sql.DB) error {
	return ensureControlGrants(ctx, db, controlRole)
}

// ControlGrantsOK reports whether the role corescope_control_client exists and
// holds exactly what EnsureControlGrants grants: in this database no other
// privilege (on any schema, table, sequence, column or the database itself),
// no object ownership, no role membership of its own, no LOGIN and no
// SUPERUSER, CREATEROLE, CREATEDB, REPLICATION or BYPASSRLS attribute. The
// server uses it for its startup diagnostics; false with a nil error means
// "run the ingestor or scripts/postgres-roles.sh".
func ControlGrantsOK(ctx context.Context, db *sql.DB) (bool, error) {
	return controlGrantsOK(ctx, db, controlRole)
}

// ensureControlGrants is EnsureControlGrants for an explicit role, so tests
// need not create the cluster-wide role name.
func ensureControlGrants(ctx context.Context, db *sql.DB, role string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pgschema: control grants: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgschema: control grants: begin: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	// The control tables are shared by every process on the database, and
	// two concurrent GRANTs on one object fail ("tuple concurrently
	// updated"): serialise with Apply's lock, held to the end of this tx.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, advisoryLockKey); err != nil {
		return fmt.Errorf("pgschema: control grants: acquire lock: %w", err)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ?)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("pgschema: control grants: probe role: %w", err)
	}
	if !exists {
		return nil
	}

	// GRANT by a role that does not own the object only warns, so check
	// ownership first rather than report grants that were never made.
	var missing, foreign []string
	for _, obj := range controlObjects() {
		var owned sql.NullBool
		if err := tx.QueryRowContext(ctx, `SELECT pg_has_role(current_user, `+obj.ownerExpr+`, 'USAGE')`+obj.from, obj.args...).Scan(&owned); err != nil {
			if err == sql.ErrNoRows {
				missing = append(missing, obj.name)
				continue
			}
			return fmt.Errorf("pgschema: control grants: probe %s: %w", obj.name, err)
		}
		if !owned.Bool {
			foreign = append(foreign, obj.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("pgschema: control grants: %s missing; run Apply first", strings.Join(missing, ", "))
	}
	if len(foreign) > 0 {
		return fmt.Errorf("pgschema: control grants: %s not owned by the current role, which therefore cannot grant on them", strings.Join(foreign, ", "))
	}

	r := quoteIdent(role)
	tables := make([]string, len(controlTablePrivs))
	for i, tp := range controlTablePrivs {
		tables[i] = controlSchema + "." + tp.table
	}
	stmts := []string{
		`REVOKE ALL ON SCHEMA ` + controlSchema + ` FROM PUBLIC, ` + r,
		`GRANT USAGE ON SCHEMA ` + controlSchema + ` TO ` + r,
		// ALL covers every privilege the server version knows (MAINTAIN
		// on 17+) and the column privileges on these tables.
		`REVOKE ALL ON TABLE ` + strings.Join(tables, ", ") + ` FROM PUBLIC, ` + r,
	}
	for i, tp := range controlTablePrivs {
		stmts = append(stmts, `GRANT `+strings.Join(tp.privs, ", ")+` ON TABLE `+tables[i]+` TO `+r)
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("pgschema: control grants: %s: %w", s, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgschema: control grants: commit: %w", err)
	}
	return nil
}

// controlObject is one object EnsureControlGrants grants on, as a query
// fragment yielding its owner's oid.
type controlObject struct {
	name      string
	ownerExpr string
	from      string
	args      []any
}

func controlObjects() []controlObject {
	objs := []controlObject{{
		name:      "schema " + controlSchema,
		ownerExpr: "nspowner",
		from:      ` FROM pg_namespace WHERE nspname = ?`,
		args:      []any{controlSchema},
	}}
	for _, tp := range controlTablePrivs {
		objs = append(objs, controlObject{
			name:      "table " + controlSchema + "." + tp.table,
			ownerExpr: "c.relowner",
			from:      ` FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = ? AND c.relname = ? AND c.relkind = 'r'`,
			args:      []any{controlSchema, tp.table},
		})
	}
	return objs
}

// controlRoleACLQuery lists every privilege granted directly to the role named
// by the argument, in the current database, one "<object> <privilege>"
// per row (" WITH GRANT OPTION" appended when grantable), plus one row per
// object it owns.
const controlRoleACLQuery = `
WITH r AS (SELECT oid FROM pg_roles WHERE rolname = ?)
SELECT 'schema ' || n.nspname || ' ' || a.privilege_type || CASE WHEN a.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END
  FROM pg_namespace n CROSS JOIN LATERAL aclexplode(n.nspacl) a, r
 WHERE a.grantee = r.oid
UNION ALL
SELECT 'table ' || n.nspname || '.' || c.relname || ' ' || a.privilege_type || CASE WHEN a.is_grantable THEN ' WITH GRANT OPTION' ELSE '' END
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) a, r
 WHERE a.grantee = r.oid
UNION ALL
SELECT 'column ' || n.nspname || '.' || c.relname || '.' || t.attname || ' ' || a.privilege_type
  FROM pg_attribute t JOIN pg_class c ON c.oid = t.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
       CROSS JOIN LATERAL aclexplode(t.attacl) a, r
 WHERE a.grantee = r.oid
UNION ALL
SELECT 'database ' || d.datname || ' ' || a.privilege_type
  FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) a, r
 WHERE d.datname = current_database() AND a.grantee = r.oid
UNION ALL
SELECT 'owns schema ' || nspname FROM pg_namespace, r WHERE nspowner = r.oid
UNION ALL
SELECT 'owns relation ' || relname FROM pg_class, r WHERE relowner = r.oid
UNION ALL
SELECT 'owns database ' || datname FROM pg_database, r WHERE datdba = r.oid
UNION ALL
SELECT 'member of ' || pg_get_userbyid(m.roleid) FROM pg_auth_members m, r WHERE m.member = r.oid`

// controlGrantsOK is ControlGrantsOK for an explicit role.
func controlGrantsOK(ctx context.Context, db *sql.DB, role string) (bool, error) {
	var super, login, createRole, createDB, replication, bypassRLS bool
	err := db.QueryRowContext(ctx, `SELECT rolsuper, rolcanlogin, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = ?`, role).Scan(&super, &login, &createRole, &createDB, &replication, &bypassRLS)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("pgschema: control grants check: read role: %w", err)
	}
	if super || login || createRole || createDB || replication || bypassRLS {
		return false, nil
	}

	rows, err := db.QueryContext(ctx, controlRoleACLQuery, role)
	if err != nil {
		return false, fmt.Errorf("pgschema: control grants check: %w", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return false, fmt.Errorf("pgschema: control grants check: %w", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("pgschema: control grants check: %w", err)
	}
	sort.Strings(got)
	want := expectedControlACL()
	return strings.Join(got, "\n") == strings.Join(want, "\n"), nil
}

// expectedControlACL is controlRoleACLQuery's result, sorted, once
// EnsureControlGrants has run.
func expectedControlACL() []string {
	want := []string{"schema " + controlSchema + " USAGE"}
	for _, tp := range controlTablePrivs {
		for _, p := range tp.privs {
			want = append(want, "table "+controlSchema+"."+tp.table+" "+p)
		}
	}
	sort.Strings(want)
	return want
}

// quoteIdent quotes a Postgres identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
