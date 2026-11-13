package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/meshcore-analyzer/users"
)

// usersPGSchema is the Postgres schema the account store lives in
// (users.OpenPostgres creates it; it is not exported).
const usersPGSchema = "corescope_users"

// usersPGURL is raw (a postgres:// URL) with search_path set to the accounts
// schema, so the generic copy/verify machinery, which reads current_schema(),
// works on it.
func usersPGURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// Not wrapped: url.Error quotes the whole URL, password included.
		return "", errors.New("unparseable postgres URL")
	}
	q := u.Query()
	q.Set("search_path", usersPGSchema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// usersRef returns ref aimed at the accounts schema when it is Postgres.
func usersRef(ref dbRef) (dbRef, error) {
	if ref.kind != postgresBackend {
		return ref, nil
	}
	u, err := usersPGURL(ref.url)
	if err != nil {
		return dbRef{}, err
	}
	ref.url = u
	return ref, nil
}

// fixupUsersSchema papers over the one table the two account schemas model
// differently. SQLite's mail_events has no primary key and keeps insertion
// order in its implicit rowid; Postgres has an explicit identity column of
// that name. Both sides get the same view: primary key (mail_id, event, at)
// (the UNIQUE constraint) and a rowid column that is copied and compared, so
// the order eventsFor reads (at, rowid) survives the move.
func fixupUsersSchema(s *schema, kind backend) {
	t := s.tables["mail_events"]
	if t == nil {
		return
	}
	if kind == sqliteBackend && t.colIndex("rowid") < 0 {
		t.cols = append(t.cols, column{name: "rowid", class: clsInt, notNull: true})
	}
	if len(t.pk) == 0 {
		for _, n := range []string{"mail_id", "event", "at"} {
			if i := t.colIndex(n); i >= 0 {
				t.pk = append(t.pk, i)
			}
		}
	}
}

// usersBinaryVersion is the account schema version this binary's users
// package creates, read from a throwaway store.
func usersBinaryVersion() (int, error) {
	dir, err := os.MkdirTemp("", "corescope-migrate-users-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	st, err := users.Open(filepath.Join(dir, "users.db"))
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.SchemaVersion()
}

// openUsersSource opens a users.db read-only and refuses one whose
// schema_version differs from this binary's.
func openUsersSource(ctx context.Context, ref dbRef) (side, func(), error) {
	want, err := usersBinaryVersion()
	if err != nil {
		return side{}, nil, fmt.Errorf("determine the account schema version of this binary: %w", err)
	}
	db, err := openReadOnly(ctx, ref)
	if err != nil {
		return side{}, nil, err
	}
	s, closeFn, err := snapshotSide(ctx, ref, db, func(tx *sql.Tx) error {
		var have int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&have); err != nil {
			return fmt.Errorf("source %s has no readable schema_version (%v): it is not a CoreScope users.db", ref.display, err)
		}
		if have != want {
			return fmt.Errorf("source %s is at account schema version %d, this binary expects %d; start the CoreScope server once (with userManagement enabled) on a copy of its version so it migrates users.db, then rerun",
				ref.display, have, want)
		}
		return nil
	})
	if err != nil {
		return side{}, nil, err
	}
	fixupUsersSchema(s.schema, s.kind)
	return s, closeFn, nil
}

// runCopyUsers implements `corescope-migrate copy-users -from sqlite:<users.db> -to <postgres URL>`.
func runCopyUsers(ctx context.Context, args []string, lg *log.Logger, sw *scrubWriter) error {
	fs := flag.NewFlagSet("copy-users", flag.ContinueOnError)
	fs.SetOutput(lg.Writer())
	fromArg := fs.String("from", "", "source users.db (sqlite:<path>)")
	toArg := fs.String("to", "", "target Postgres database (postgres://user@host/db); schema corescope_users is created and must be empty")
	batch := fs.Int("batch", 5000, "rows per COPY call")
	fs.Usage = usageFor(fs, "copy-users -from sqlite:<users.db> -to postgres://<accounts role>@host/db [-batch 5000]",
		"corescope-migrate copy-users -from sqlite:/var/lib/corescope/users.db -to postgres://corescope_users@db.example/corescope")
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
	if from.kind != sqliteBackend {
		return errors.New("-from: copy-users reads a SQLite users.db (sqlite:<path>)")
	}
	if to.kind != postgresBackend {
		return errors.New("-to: copy-users writes the Postgres accounts schema (postgres://...)")
	}

	lg.Printf("copy-users %s -> %s schema %s (batch %d)", from.display, to.display, usersPGSchema, *batch)
	lg.Printf("WARNING: stop the CoreScope server on the source first: the copy reads one snapshot, and accounts changed after it are not copied")

	src, closeSrc, err := openUsersSource(ctx, from)
	if err != nil {
		return err
	}
	defer closeSrc()

	job := &copyJob{lg: lg, src: src, batch: *batch, users: true,
		absent: map[string]bool{}, absentCols: map[string]map[string]bool{}}
	start := time.Now()
	if err := job.intoPostgres(ctx, to); err != nil {
		return err
	}
	lg.Printf("OK: copied %d rows into %s schema %s in %s; the copy is verified", job.rows, to.display, usersPGSchema, time.Since(start).Round(time.Millisecond))
	return nil
}

// runVerifyUsers implements `corescope-migrate verify-users -a <db> -b <db>`:
// verify for account stores. Each side is a SQLite users.db or the Postgres
// accounts schema.
func runVerifyUsers(ctx context.Context, args []string, lg *log.Logger, sw *scrubWriter) error {
	fs := flag.NewFlagSet("verify-users", flag.ContinueOnError)
	fs.SetOutput(lg.Writer())
	aArg := fs.String("a", "", "first account store (sqlite:<users.db> or postgres://user@host/db)")
	bArg := fs.String("b", "", "second account store; its schema is the reference (normally the copy)")
	fs.Usage = usageFor(fs, "verify-users -a <db> -b <db>",
		"corescope-migrate verify-users -a sqlite:/var/lib/corescope/users.db -b postgres://corescope_users@db.example/corescope")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *aArg == "" || *bArg == "" || fs.NArg() > 0 {
		fs.Usage()
		return errUsage
	}
	a, err := parseDBRef(*aArg)
	if err != nil {
		return fmt.Errorf("-a: %w", err)
	}
	b, err := parseDBRef(*bArg)
	if err != nil {
		return fmt.Errorf("-b: %w", err)
	}
	sw.add(a, b)
	if a, err = usersRef(a); err != nil {
		return fmt.Errorf("-a: %w", err)
	}
	if b, err = usersRef(b); err != nil {
		return fmt.Errorf("-b: %w", err)
	}

	sa, closeA, err := openSnapshot(ctx, a)
	if err != nil {
		return err
	}
	defer closeA()
	sb, closeB, err := openSnapshot(ctx, b)
	if err != nil {
		return err
	}
	defer closeB()
	fixupUsersSchema(sa.schema, sa.kind)
	fixupUsersSchema(sb.schema, sb.kind)
	sa.redact, sb.redact = true, true
	lg.Printf("verifying %s (a) against %s (b)", a.display, b.display)
	results, err := compareTables(ctx, sa, sb)
	if err != nil {
		return err
	}
	return reportResults(lg, results)
}
