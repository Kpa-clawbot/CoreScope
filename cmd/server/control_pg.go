package main

// The control pool: the server's only writable Postgres connections, and the
// only file that may open or use them (readonly_invariant_test.go pins this).
//
// On Postgres the server and the ingestor share no filesystem, so the
// geo-prune requests and the multibyte capability snapshot travel through
// control tables in the corescope_control schema (pgschema migration 0002)
// instead of the file queues SQLite deployments use. Inserting there needs a
// transaction that is not read-only, which the main pool forbids by forcing
// default_transaction_read_only=on. So the server opens a second, small pool
// from the same DSN with it forced off, and hands it only to the
// prunequeue/mbcapqueue Postgres implementations.
//
// The pool runs as the same role as the main pool, so it can do no more than
// that role may: verifyPostgresNoWritePrivilege refuses any write privilege on
// the measurement schema, and on the control schema everything beyond the
// column INSERT the group role corescope_control_client holds. The pool is opened only when
// the role inherits that group and the group holds exactly the control grants
// (pgschema.CheckControlGrants): column INSERT (id, payload) on prune_requests
// and mbcap_snapshots, SELECT on those and prune_results. If the role is not
// in the group or the group lacks a grant, the server starts without it, logs
// one warning, and the control features are off: the prune endpoints answer
// 503 and the multibyte snapshot is not published. A group holding MORE than
// those grants refuses startup.

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/mbcapqueue"
	"github.com/meshcore-analyzer/prunequeue"
)

// pgControlRole is the NOLOGIN group role whose membership is the server's
// only write access: scripts/postgres-roles.sh creates it and grants it to the
// reader, pgschema.EnsureControlGrants (run by the ingestor) keeps its grants.
const pgControlRole = "corescope_control_client"

// pgControlSchema is the schema holding the control tables.
const pgControlSchema = prunequeue.DefaultPGSchema

// msgControlUnavailable is the 503 body of the prune endpoints when the
// control pool is off. It names the fix, never the role's privileges.
const msgControlUnavailable = "geo-prune is unavailable: the server's Postgres role is not a member of " +
	pgControlRole + " with the control grants; see the server log at startup"

// pgControlParams are pgServerParams with read-only transactions forced off:
// the control pool's sessions must be able to INSERT into the control tables.
// Every other parameter (the timeouts) is the main pool's.
func pgControlParams(statementTimeoutSec int) []pgParam {
	params := pgServerParams(statementTimeoutSec)
	for i := range params {
		if params[i].name == pgReadOnlyParam {
			params[i] = pgParam{pgReadOnlyParam, "off"}
		}
	}
	return params
}

// pgControlAvailability reports whether the session's role can use the
// control tables: it inherits pgControlRole and that role holds exactly the
// control grants. reason says why not, for the startup warning. A control
// role holding more than those grants is an error (pgschema.ControlExcess):
// the server must not run with a group role that can do more than the
// control writes, whether or not this session's role is in it yet.
func pgControlAvailability(ctx context.Context, conn *sql.DB) (ok bool, reason string, err error) {
	var exists, member bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ?)`, pgControlRole).Scan(&exists); err != nil {
		return false, "", fmt.Errorf("probe role %s: %w", pgControlRole, err)
	}
	if !exists {
		return false, "role " + pgControlRole + " does not exist (run scripts/postgres-roles.sh)", nil
	}
	// USAGE: the role's privileges are inherited, so the pool can use them
	// without SET ROLE.
	if err := conn.QueryRowContext(ctx, `SELECT pg_has_role(current_user, ?, 'USAGE')`, pgControlRole).Scan(&member); err != nil {
		return false, "", fmt.Errorf("probe membership in %s: %w", pgControlRole, err)
	}
	if !member {
		return false, "the role does not inherit " + pgControlRole + " (GRANT " + pgControlRole + " TO <reader>, or run scripts/postgres-roles.sh)", nil
	}
	state, detail, err := pgschema.CheckControlGrants(ctx, conn)
	if err != nil {
		return false, "", err
	}
	switch state {
	case pgschema.ControlExact:
		return true, "", nil
	case pgschema.ControlExcess:
		var role string
		_ = conn.QueryRowContext(ctx, `SELECT current_user`).Scan(&role)
		return false, "", fmt.Errorf("postgres role %q is a member of %s, which holds more than the control grants: %s (revoke them, or start the ingestor, which repairs the grants)", role, pgControlRole, detail)
	default: // ControlAbsent, ControlIncomplete
		return false, pgControlRole + " does not hold all the control grants: " + detail + " (start the ingestor, which repairs them, or run scripts/postgres-roles.sh)", nil
	}
}

// openPostgresControl opens the control pool and the Postgres queues on it
// when the role may use the control tables (see the file comment). When it
// may not, it logs one warning and leaves them nil; that is not an error.
// An error means the checks or the pool themselves failed, or the control
// role holds excess grants: startup stops.
// Called once by OpenPostgresDBWithStatementTimeout, after the main pool's
// privilege checks passed.
func (d *DB) openPostgresControl(ctx context.Context, dsn string, statementTimeoutSec int) error {
	ok, reason, err := pgControlAvailability(ctx, d.conn)
	if err != nil {
		return fmt.Errorf("control tables: %w", err)
	}
	if !ok {
		log.Printf("[db] WARNING: geo-prune and multibyte capability publishing are disabled: %s", reason)
		return nil
	}

	ctlParams := pgControlParams(statementTimeoutSec)
	ctlDSN, secrets, err := pgServerDSN(dsn, ctlParams)
	if err != nil {
		return err
	}
	scrub := func(err error) error { return scrubPGSecret(err, secrets...) }
	ctl, err := sql.Open(dbx.PostgresDriverName, ctlDSN)
	if err != nil {
		return fmt.Errorf("control pool: %w", scrub(err))
	}
	// Two writers at most: the geo-prune handler and the analytics cycle.
	ctl.SetMaxOpenConns(2)
	ctl.SetMaxIdleConns(1)
	fail := func(format string, err error) error {
		ctl.Close()
		return fmt.Errorf(format, scrub(err))
	}
	if err := pingPostgres(ctx, ctl); err != nil {
		return fail("control pool: ping failed: %w", err)
	}
	if err := verifyPostgresSessionParams(ctx, ctl, ctlParams); err != nil {
		return fail("control pool: %w", err)
	}
	// Same DSN, so the same role: the privilege checks run on the main pool
	// cover this one only while that holds.
	var mainRole, ctlRole string
	if err := d.conn.QueryRowContext(ctx, `SELECT current_user`).Scan(&mainRole); err != nil {
		return fail("control pool: read role: %w", err)
	}
	if err := ctl.QueryRowContext(ctx, `SELECT current_user`).Scan(&ctlRole); err != nil {
		return fail("control pool: read role: %w", err)
	}
	if ctlRole != mainRole {
		return fail("%w", fmt.Errorf("control pool runs as role %q, the main pool as %q", ctlRole, mainRole))
	}

	d.ctl = ctl
	d.pruneQ = prunequeue.NewPG(ctl, "")
	d.mbcap = mbcapqueue.NewPG(ctl, "")
	return nil
}

// closeControl closes the control pool, if open. Called by Close.
func (d *DB) closeControl() {
	if d.ctl != nil {
		d.ctl.Close()
	}
}
