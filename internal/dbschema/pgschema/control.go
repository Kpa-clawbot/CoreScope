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

// controlTablePrivs is every privilege controlRole holds on a control table:
// privs on the whole table, INSERT only on insertCols. created_at is never
// among them, so its DEFAULT (migration 0003, the database clock) always
// applies to the server's rows. Anything else it held there is revoked by
// EnsureControlGrants.
var controlTablePrivs = []struct {
	table      string
	privs      []string
	insertCols []string
}{
	{"prune_requests", []string{"SELECT"}, []string{"id", "payload"}},
	{"prune_results", []string{"SELECT"}, nil},
	{"mbcap_snapshots", []string{"SELECT"}, []string{"id", "payload"}},
}

// grantList is the privilege list of a GRANT on the table, e.g.
// "SELECT, INSERT (id, payload)".
func grantList(privs, insertCols []string) string {
	l := append([]string(nil), privs...)
	if len(insertCols) > 0 {
		l = append(l, "INSERT ("+strings.Join(insertCols, ", ")+")")
	}
	return strings.Join(l, ", ")
}

// ControlGrantState is what CheckControlGrants found.
type ControlGrantState int

const (
	ControlAbsent     ControlGrantState = iota // role missing, or the tables don't exist yet
	ControlIncomplete                          // role exists but lacks some required grant
	ControlExact                               // exactly the required grants and nothing else
	ControlExcess                              // holds anything beyond them (other privileges, ownership, CREATE, memberships, attributes, default privileges on the control schema)
)

func (s ControlGrantState) String() string {
	switch s {
	case ControlAbsent:
		return "absent"
	case ControlIncomplete:
		return "incomplete"
	case ControlExact:
		return "exact"
	case ControlExcess:
		return "excess"
	}
	return fmt.Sprintf("ControlGrantState(%d)", int(s))
}

// EnsureControlGrants gives the role corescope_control_client exactly USAGE on
// schema corescope_control, SELECT and INSERT (id, payload) on prune_requests
// and mbcap_snapshots and SELECT on prune_results, revoking anything else that
// role or PUBLIC holds on them. It refuses (error, granting nothing) while any
// control table carries a user trigger: one would run as the table owner on
// the server's inserts. It returns nil, doing nothing more, when the role
// does not exist (scripts/postgres-roles.sh creates it, and grants the same
// privileges itself when the tables already exist, so the two may run in
// either order). The ingestor runs it after Apply at every start, as the
// writer, which owns the control tables. Idempotent.
func EnsureControlGrants(ctx context.Context, db *sql.DB) error {
	return ensureControlGrants(ctx, db, controlRole)
}

// CheckControlGrants reports how the role corescope_control_client's
// privileges compare with what EnsureControlGrants grants. ControlExact means
// in this database no other privilege (on any schema, table, column,
// sequence, function, type, language, foreign server or wrapper, large
// object, the database, or cluster-wide on a tablespace or parameter), no
// default privilege, no object ownership or policy, no role membership of its
// own, no LOGIN and no SUPERUSER, CREATEROLE, CREATEDB, REPLICATION or
// BYPASSRLS attribute. Excess wins over missing grants. The detail names what
// is missing or extra, for the server's startup diagnostics; it is empty for
// ControlExact.
func CheckControlGrants(ctx context.Context, db *sql.DB) (ControlGrantState, string, error) {
	return checkControlGrants(ctx, db, controlRole)
}

// ControlGrantsOK reports whether CheckControlGrants finds ControlExact.
func ControlGrantsOK(ctx context.Context, db *sql.DB) (bool, error) {
	state, _, err := CheckControlGrants(ctx, db)
	return state == ControlExact, err
}

// CheckSingleInstall returns an error naming every other schema in the
// current database holding a CoreScope schema (a table named schema_version),
// since corescope_control is shared by all of them. It reads only pg_class and
// pg_namespace, which every role sees whatever its table privileges.
func CheckSingleInstall(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'schema_version' AND c.relkind IN ('r', 'p')
		  AND n.nspname IS DISTINCT FROM current_schema() AND n.nspname <> ?
		ORDER BY n.nspname`, controlSchema)
	if err != nil {
		return fmt.Errorf("pgschema: single-install check: %w", err)
	}
	defer rows.Close()
	var others []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return fmt.Errorf("pgschema: single-install check: %w", err)
		}
		others = append(others, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pgschema: single-install check: %w", err)
	}
	if len(others) > 0 {
		return fmt.Errorf("pgschema: schema %s in this database also holds a CoreScope schema (schema_version): one CoreScope install per database; the control schema %s is shared, so a second install could act on the first's requests",
			strings.Join(others, ", "), controlSchema)
	}
	return nil
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
	// A trigger on a control table runs as whoever defined it on every row
	// the server inserts: none belongs there, so refuse rather than hand out
	// grants that would feed it.
	trigs, err := controlTriggers(ctx, tx)
	if err != nil {
		return err
	}
	if len(trigs) > 0 {
		return fmt.Errorf("pgschema: control grants: refusing while the control tables carry triggers (%s); drop them", strings.Join(trigs, ", "))
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
		stmts = append(stmts, `GRANT `+grantList(tp.privs, tp.insertCols)+` ON TABLE `+tables[i]+` TO `+r)
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

// controlTriggers lists the user (non-internal) triggers on the control
// tables as "table.trigger".
func controlTriggers(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.relname || '.' || t.tgname
		FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ? AND NOT t.tgisinternal ORDER BY 1`, controlSchema)
	if err != nil {
		return nil, fmt.Errorf("pgschema: control grants: list triggers: %w", err)
	}
	defer rows.Close()
	var trigs []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("pgschema: control grants: list triggers: %w", err)
		}
		trigs = append(trigs, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgschema: control grants: list triggers: %w", err)
	}
	return trigs, nil
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

// controlRoleACLQuery lists everything the role named by the argument holds
// in the current database (and cluster-wide on shared objects), one
// "<object> <privilege>" per row (" WITH GRANT OPTION" appended when
// grantable): every privilege granted to it directly, from every catalog
// with an ACL, every default privilege naming it, and every object it owns
// or policy naming it (pg_shdepend, which covers every object type). Its
// role memberships and attributes are checked separately.
const controlRoleACLQuery = `
WITH r AS (SELECT oid FROM pg_roles WHERE rolname = ?),
acl(obj, a) AS (
	SELECT 'schema ' || n.nspname, aclexplode(n.nspacl) FROM pg_namespace n
	UNION ALL
	SELECT 'table ' || n.nspname || '.' || c.relname, aclexplode(c.relacl)
	  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	UNION ALL
	SELECT 'column ' || n.nspname || '.' || c.relname || '.' || t.attname, aclexplode(t.attacl)
	  FROM pg_attribute t JOIN pg_class c ON c.oid = t.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	UNION ALL
	SELECT 'database ' || datname, aclexplode(datacl) FROM pg_database WHERE datname = current_database()
	UNION ALL
	SELECT 'function ' || p.oid::regprocedure::text, aclexplode(p.proacl) FROM pg_proc p
	UNION ALL
	SELECT 'type ' || t.oid::regtype::text, aclexplode(t.typacl) FROM pg_type t
	UNION ALL
	SELECT 'language ' || lanname, aclexplode(lanacl) FROM pg_language
	UNION ALL
	SELECT 'foreign data wrapper ' || fdwname, aclexplode(fdwacl) FROM pg_foreign_data_wrapper
	UNION ALL
	SELECT 'foreign server ' || srvname, aclexplode(srvacl) FROM pg_foreign_server
	UNION ALL
	SELECT 'large object ' || oid::text, aclexplode(lomacl) FROM pg_largeobject_metadata
	UNION ALL
	SELECT 'tablespace ' || spcname, aclexplode(spcacl) FROM pg_tablespace
	UNION ALL
	SELECT 'parameter ' || parname, aclexplode(paracl) FROM pg_parameter_acl
	UNION ALL
	SELECT 'default privileges for ' || pg_get_userbyid(d.defaclrole)
	       || coalesce(' in schema ' || n.nspname, '') || ' on ' || d.defaclobjtype::text,
	       aclexplode(d.defaclacl)
	  FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
)
SELECT obj || ' ' || (a).privilege_type || CASE WHEN (a).is_grantable THEN ' WITH GRANT OPTION' ELSE '' END
  FROM acl, r WHERE (a).grantee = r.oid
UNION ALL
SELECT CASE s.deptype WHEN 'o' THEN 'owns ' WHEN 'r' THEN 'policy on ' ELSE 'depends (' || s.deptype::text || ') on ' END
       || pg_describe_object(s.classid, s.objid, s.objsubid)
  FROM pg_shdepend s, r
 WHERE s.refclassid = 'pg_authid'::regclass AND s.refobjid = r.oid AND s.deptype <> 'a'
   AND s.dbid IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
UNION ALL
SELECT 'member of ' || pg_get_userbyid(m.roleid) FROM pg_auth_members m, r WHERE m.member = r.oid`

// checkControlGrants is CheckControlGrants for an explicit role.
func checkControlGrants(ctx context.Context, db *sql.DB, role string) (ControlGrantState, string, error) {
	var super, login, createRole, createDB, replication, bypassRLS bool
	err := db.QueryRowContext(ctx, `SELECT rolsuper, rolcanlogin, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = ?`, role).Scan(&super, &login, &createRole, &createDB, &replication, &bypassRLS)
	if err == sql.ErrNoRows {
		return ControlAbsent, "role " + role + " does not exist", nil
	}
	if err != nil {
		return ControlAbsent, "", fmt.Errorf("pgschema: control grants check: read role: %w", err)
	}
	var noTables []string
	for _, tp := range controlTablePrivs {
		var ok bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(?) IS NOT NULL`, controlSchema+"."+tp.table).Scan(&ok); err != nil {
			return ControlAbsent, "", fmt.Errorf("pgschema: control grants check: probe %s: %w", tp.table, err)
		}
		if !ok {
			noTables = append(noTables, controlSchema+"."+tp.table)
		}
	}
	if len(noTables) > 0 {
		return ControlAbsent, strings.Join(noTables, ", ") + " missing: the ingestor has not created the control tables yet", nil
	}

	var extra []string
	for _, a := range []struct {
		on   bool
		name string
	}{{super, "SUPERUSER"}, {login, "LOGIN"}, {createRole, "CREATEROLE"}, {createDB, "CREATEDB"}, {replication, "REPLICATION"}, {bypassRLS, "BYPASSRLS"}} {
		if a.on {
			extra = append(extra, "attribute "+a.name)
		}
	}

	rows, err := db.QueryContext(ctx, controlRoleACLQuery, role)
	if err != nil {
		return ControlAbsent, "", fmt.Errorf("pgschema: control grants check: %w", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return ControlAbsent, "", fmt.Errorf("pgschema: control grants check: %w", err)
		}
		got[s] = true
	}
	if err := rows.Err(); err != nil {
		return ControlAbsent, "", fmt.Errorf("pgschema: control grants check: %w", err)
	}
	var missing []string
	for _, w := range expectedControlACL() {
		if got[w] {
			delete(got, w)
		} else {
			missing = append(missing, w)
		}
	}
	for s := range got {
		extra = append(extra, s)
	}
	sort.Strings(extra)

	switch {
	case len(extra) > 0:
		detail := "role " + role + " holds more than the control grants: " + strings.Join(extra, "; ")
		if len(missing) > 0 {
			detail += "; and lacks: " + strings.Join(missing, "; ")
		}
		return ControlExcess, detail, nil
	case len(missing) > 0:
		return ControlIncomplete, "role " + role + " lacks: " + strings.Join(missing, "; "), nil
	}
	return ControlExact, "", nil
}

// expectedControlACL is what controlRoleACLQuery returns, sorted, once
// EnsureControlGrants has run.
func expectedControlACL() []string {
	want := []string{"schema " + controlSchema + " USAGE"}
	for _, tp := range controlTablePrivs {
		for _, p := range tp.privs {
			want = append(want, "table "+controlSchema+"."+tp.table+" "+p)
		}
		for _, c := range tp.insertCols {
			want = append(want, "column "+controlSchema+"."+tp.table+"."+c+" INSERT")
		}
	}
	sort.Strings(want)
	return want
}

// quoteIdent quotes a Postgres identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
