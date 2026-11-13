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
// upgrading its schema (pgschema.Apply). queuePath is the configured dbPath:
// on Postgres it is only the anchor for the file queues the server shares
// (prunequeue, mbcapqueue), and the place the empty-target guard looks for a
// SQLite database that has not been copied over yet.
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

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	if err := pgschema.Apply(context.Background(), db, log.Printf); err != nil {
		return nil, fmt.Errorf("applying postgres schema: %w", err)
	}
	if err := refuseEmptyPostgresOverSQLite(db, queuePath); err != nil {
		return nil, err
	}

	s, err := newStore(db, queuePath, sampleIntervalSec)
	if err != nil {
		return nil, err
	}
	s.loadAdvertEvidenceStatus()
	log.Print(postgresMaintenanceNote)
	return s, nil
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
	f, err := os.Open(path)
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
	lite, err := sql.Open("sqlite3", usersDBReadOnlyDSN(path))
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
