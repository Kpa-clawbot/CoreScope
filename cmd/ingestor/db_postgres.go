package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// postgresMaintenanceNote is logged once when a Postgres store opens. The
// SQLite maintenance calls (Checkpoint, CheckAutoVacuum, RunIncrementalVacuum,
// RefreshPlannerStats, EnsurePlannerStats) are no-ops there; retention prunes
// run exactly as on SQLite.
const postgresMaintenanceNote = "[db] maintenance handled by PostgreSQL autovacuum (WAL checkpoint, incremental vacuum and planner statistics are SQLite-only and skipped)"

// OpenPostgresStore opens the measurement database on PostgreSQL, creating or
// upgrading its schema (pgschema.Apply), refusing to start when another
// CoreScope install shares the database (pgschema.CheckSingleInstall), and
// granting the server's control role its privileges on the control tables
// (pgschema.EnsureControlGrants).
// queuePath is the configured dbPath: on Postgres it is only the place the
// empty-target guard looks for a SQLite database that has not been copied
// over yet. Nothing is exchanged with the server through files: geo-prune
// requests and multi-byte snapshots go through corescope_control, the stats
// through ingestor_stats.
//
// No error it returns contains the DSN or its password.
func OpenPostgresStore(dsn, queuePath string, sampleIntervalSec int) (*Store, error) {
	if err := checkPostgresDSN(dsn); err != nil {
		return nil, err
	}
	db, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		return nil, redactDSNError(fmt.Errorf("opening postgres: %w", err), dsn)
	}
	s, err := openPostgresStoreDB(db, queuePath, sampleIntervalSec)
	if err != nil {
		db.Close()
		// The guard's refusal is built here, not by the driver, and holds
		// no DSN; redacting it could only mangle its copy command.
		var refusal *emptyPostgresTargetError
		if errors.As(err, &refusal) {
			return nil, err
		}
		return nil, redactDSNError(err, dsn)
	}
	return s, nil
}

// allowSharedDatabaseForTests skips the single-install check
// (pgschema.CheckSingleInstall). Only the test binary sets it: the tests run
// many throwaway CoreScope schemas side by side in one database, which a
// production ingestor must refuse — the control tables live in one fixed
// schema, so two installs would consume each other's prune requests and
// snapshots.
var allowSharedDatabaseForTests bool

// openPostgresStoreDB is OpenPostgresStore on an already opened handle (tests
// pass one scoped to a throwaway schema). It owns db from here on, but leaves
// closing it on error to the caller.
func openPostgresStoreDB(db *sql.DB, queuePath string, sampleIntervalSec int) (*Store, error) {
	// One connection, exactly as on SQLite. Besides keeping a single writer,
	// it is what keeps the server's tail correct: the server polls
	// transmissions and observations for "id > last seen id", which misses
	// nothing only if ids commit in increasing order. With two connections a
	// transaction holding id 101 can commit after one holding 102, and a poll
	// in between moves the cursor past 101 for good.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := pingPostgres(db); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	if err := pgschema.Apply(context.Background(), db, log.Printf); err != nil {
		return nil, fmt.Errorf("applying postgres schema: %w", err)
	}
	if !allowSharedDatabaseForTests {
		if err := pgschema.CheckSingleInstall(context.Background(), db); err != nil {
			return nil, fmt.Errorf("refusing to start: %w", err)
		}
	}
	if err := ensureControlGrants(db); err != nil {
		return nil, err
	}
	for _, p := range sqliteGuardPaths(queuePath) {
		if err := refuseEmptyPostgresOverSQLite(db, p); err != nil {
			return nil, err
		}
	}

	s, err := newStore(db, queuePath, sampleIntervalSec)
	if err != nil {
		return nil, err
	}
	s.loadAdvertEvidenceStatus()
	log.Print(postgresMaintenanceNote)
	return s, nil
}

// controlRoleMissingNote is logged when the server's control role is absent
// (or not set up as scripts/postgres-roles.sh sets it up). Ingest is
// unaffected; only the server's writes to corescope_control are.
const controlRoleMissingNote = "[db] role corescope_control_client is missing or not set up: geo-prune requests and multi-byte capability snapshots from the server need it; run scripts/postgres-roles.sh"

// ensureControlGrants grants corescope_control_client exactly its privileges
// on the control tables, which the ingestor owns. The role itself is created
// by scripts/postgres-roles.sh (it needs CREATEROLE, which the writer should
// not hold); without it the grants are skipped and one line says so.
func ensureControlGrants(db *sql.DB) error {
	ctx := context.Background()
	if err := pgschema.EnsureControlGrants(ctx, db); err != nil {
		return fmt.Errorf("granting control-table privileges: %w", err)
	}
	ok, err := pgschema.ControlGrantsOK(ctx, db)
	if err != nil {
		log.Printf("[db] checking role corescope_control_client: %v", err)
		return nil
	}
	if !ok {
		log.Print(controlRoleMissingNote)
	}
	return nil
}

// refuseEmptyPostgresOverSQLite stops an ingestor that was switched to
// Postgres before the operator's SQLite data was copied there: it would start
// on an empty database, and the server would show an empty mesh while the
// history sits unused in the old file. It refuses only when Postgres has no
// transmissions and sqlitePath is a SQLite database that has some. A
// populated Postgres starts regardless of what is left at sqlitePath.
func refuseEmptyPostgresOverSQLite(db *sql.DB, sqlitePath string) error {
	var populated bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM transmissions)`).Scan(&populated); err != nil {
		return fmt.Errorf("checking postgres for existing transmissions: %w", err)
	}
	if populated {
		return nil
	}
	n, err := sqliteTransmissionCount(sqlitePath)
	if err != nil {
		return &emptyPostgresTargetError{fmt.Sprintf("refusing to start on an empty PostgreSQL database: cannot tell whether %s still holds data (%v); move %s aside to start with an empty database", sqlitePath, err, sqlitePath)}
	}
	if n == 0 {
		return nil
	}
	return &emptyPostgresTargetError{fmt.Sprintf(`refusing to start on an empty PostgreSQL database while %s still holds %d transmissions: copy it first with "corescope-migrate copy -from sqlite:%s -to <postgres url>", or move %s aside to start with an empty database`,
		sqlitePath, n, sqlitePath, sqlitePath)}
}

// sqliteGuardEnv names an extra SQLite file the empty-target guard checks.
// The Docker image sets it on Postgres: there the ingestor's DB_PATH points
// into /app/data/shared (where the file queues lived before they moved into
// the database), while an operator's existing SQLite data is still at
// /app/data/meshcore.db.
const sqliteGuardEnv = "CORESCOPE_SQLITE_GUARD_PATH"

// sqliteGuardPaths returns the SQLite files the guard must check. When
// $CORESCOPE_SQLITE_GUARD_PATH is set it names the operator's real SQLite
// file and is the only one checked (in the image queuePath sits in what was
// the directory shared with the API server). Otherwise it is queuePath, the
// configured dbPath.
func sqliteGuardPaths(queuePath string) []string {
	if extra := strings.TrimSpace(os.Getenv(sqliteGuardEnv)); extra != "" {
		return []string{extra}
	}
	return []string{queuePath}
}

// emptyPostgresTargetError is the empty-target guard's refusal.
type emptyPostgresTargetError struct{ msg string }

func (e *emptyPostgresTargetError) Error() string { return e.msg }

// sqliteMagic is the first 16 bytes of every SQLite 3 database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// sqliteTransmissionCount returns how many transmissions the SQLite database
// at path holds: 0 when there is no file there, when it is not a SQLite
// database, or when it has no transmissions table. It opens the file
// read-only and never creates it.
func sqliteTransmissionCount(path string) (int64, error) {
	if path == "" {
		return 0, nil
	}
	// Lstat before opening: a FIFO or device at the path must never be
	// opened (it could block the ingestor's startup, or be read from), and
	// SQLite is only ever handed a regular file.
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if st.IsDir() {
		return 0, nil
	}
	if !st.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file (%s)", path, st.Mode().Type())
	}
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	head := make([]byte, len(sqliteMagic))
	_, err = io.ReadFull(f, head)
	f.Close()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, nil // shorter than a header: an empty or foreign file
	}
	if err != nil {
		// A directory, or unreadable.
		if st, serr := os.Stat(path); serr == nil && st.IsDir() {
			return 0, nil
		}
		return 0, err
	}
	if !bytes.Equal(head, sqliteMagic) {
		return 0, nil
	}

	// The same read-only open the users.db readers use: mode=ro never
	// creates or writes the database file.
	dsn, err := usersDBReadOnlyDSN(path)
	if err != nil {
		return 0, err
	}
	lite, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return 0, err
	}
	defer lite.Close()
	ok, err := dbschema.TableExists(lite, "transmissions")
	if err != nil || !ok {
		return 0, err
	}
	var n int64
	if err := lite.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// checkPostgresDSN rejects a URL-form DSN that does not parse. The driver's
// error for one quotes the raw string, password and all, and an unparseable
// URL is also one redactDSNError cannot reliably take the password out of.
func checkPostgresDSN(dsn string) error {
	if isPostgresURL(dsn) {
		if _, err := url.Parse(dsn); err != nil {
			return errors.New("opening postgres: db.dsn is not a valid postgres:// URL")
		}
	}
	return nil
}

func isPostgresURL(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// keywordPassword finds password=... in a keyword/value DSN, quoted or not.
var keywordPassword = regexp.MustCompile(`password\s*=\s*('(?:[^'\\]|\\.)*'|[^\s]+)`)

// redactDSNError returns err with the DSN and its password replaced by
// "xxxxx" wherever they occur in its message, or err itself when neither does.
func redactDSNError(err error, dsn string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	red := msg
	for _, secret := range dsnSecrets(dsn) {
		if secret != "" {
			red = strings.ReplaceAll(red, secret, "xxxxx")
		}
	}
	if red == msg {
		return err
	}
	return errors.New(red)
}

// dsnSecrets lists the strings that must never appear in an error: the whole
// DSN and the password, raw and escaped. The whole DSN comes first, so it is
// replaced as one before the password inside it.
func dsnSecrets(dsn string) []string {
	out := []string{dsn}
	if isPostgresURL(dsn) {
		if u, err := url.Parse(dsn); err == nil && u.User != nil {
			if pw, ok := u.User.Password(); ok {
				out = append(out, u.User.String(), pw, url.QueryEscape(pw), url.PathEscape(pw))
			}
			if pw := u.Query().Get("password"); pw != "" {
				out = append(out, pw, url.QueryEscape(pw))
			}
		}
		return out
	}
	for _, m := range keywordPassword.FindAllStringSubmatch(dsn, -1) {
		v := m[1]
		out = append(out, v)
		if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
			out = append(out, v[1:len(v)-1])
		}
	}
	return out
}

// pgConnectWait bounds how long a first connect rides out a refused or
// unresolvable endpoint (a new pod's NetworkPolicy not admitting it yet).
const pgConnectWait = 30 * time.Second

func pingPostgres(db *sql.DB) error {
	err := db.Ping()
	if err == nil || !dbx.IsTransientConnError(err) {
		return err
	}
	log.Printf("postgres not reachable yet, retrying for up to %s", pgConnectWait)
	return dbx.PingWithRetry(context.Background(), db, pgConnectWait)
}
