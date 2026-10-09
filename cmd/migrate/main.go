// Command migrate bootstraps PostgreSQL or imports immutable offline SQLite
// recovery snapshots. Runtime services never apply DDL themselves.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runCommand(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "[migrate]", err)
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databaseURL := flags.String("database-url", "", "PostgreSQL telemetry owner URL (prefer CORESCOPE_DATABASE_URL)")
	usersURL := flags.String("users-database-url", "", "PostgreSQL accounts owner URL (prefer CORESCOPE_USERS_DATABASE_URL)")
	source := flags.String("from-sqlite", "", "offline telemetry SQLite source")
	accountSource := flags.String("users-from-sqlite", "", "offline account SQLite source")
	stateDir := flags.String("state-dir", "state/migration", "private migration snapshots and resume manifests")
	offline := flags.Bool("offline", false, "confirm telemetry and account writers are stopped")
	resume := flags.Bool("resume", false, "resume the same source/state/destination import")
	checkReady := flags.Bool("check-ready", false, "check readiness without changing any schema or data")
	checkImportKind := flags.String("check-import-kind", "", "read-only completed-import guard: telemetry or accounts, with its SQLite source path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(out)
			flags.PrintDefaults()
			return nil
		}
		return errors.New("invalid migration arguments; use -help")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional migration argument; use -help")
	}
	if *databaseURL == "" {
		*databaseURL = os.Getenv("CORESCOPE_DATABASE_URL")
	}
	if *usersURL == "" {
		*usersURL = os.Getenv("CORESCOPE_USERS_DATABASE_URL")
	}
	if *databaseURL == "" && *usersURL == "" {
		return errors.New("provide CORESCOPE_DATABASE_URL and/or CORESCOPE_USERS_DATABASE_URL")
	}
	if *databaseURL != "" && *usersURL != "" {
		same, err := pgutil.SameDatabase(ctx, *databaseURL, *usersURL)
		if err != nil {
			return err
		}
		if same {
			return errors.New("telemetry and accounts require separate PostgreSQL databases")
		}
	}
	type store struct{ kind, dsn, source string }
	stores := []store{{"telemetry", *databaseURL, *source}, {"accounts", *usersURL, *accountSource}}
	if *checkImportKind != "" {
		if *checkReady || *resume {
			return errors.New("-check-import-kind cannot be combined with -check-ready or -resume")
		}
		for _, s := range stores {
			if s.kind == *checkImportKind {
				if s.dsn == "" || s.source == "" {
					return errors.New("completed-import check requires the matching PostgreSQL URL and SQLite source path")
				}
				if err := checkImportedSource(ctx, s.dsn, s.kind, s.source); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s verified import matches the retained SQLite source\n", s.kind)
				return nil
			}
		}
		return errors.New("-check-import-kind must be telemetry or accounts")
	}
	importing := *source != "" || *accountSource != ""
	if importing && !*offline {
		return errors.New("SQLite import requires -offline after stopping all telemetry and account writers")
	}
	if *checkReady && (importing || *resume) {
		return errors.New("-check-ready cannot be combined with import or resume")
	}
	if *resume && !importing {
		return errors.New("-resume requires a SQLite source")
	}
	var imported []store
	for _, s := range stores {
		if s.dsn == "" {
			if s.source != "" {
				return fmt.Errorf("%s source requires its PostgreSQL owner URL", s.kind)
			}
			continue
		}
		if *checkReady {
			db, err := pgutil.Open(s.dsn, false)
			if err != nil {
				return err
			}
			if s.kind == "telemetry" {
				err = dbschema.AssertReady(db)
			} else {
				err = users.AssertReady(db)
			}
			db.Close()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s PostgreSQL schema is ready\n", s.kind)
			continue
		}
		if s.source != "" {
			report, err := importSQLite(ctx, importOptions{Source: s.source, DatabaseURL: s.dsn, StateDir: *stateDir, Kind: s.kind, Resume: *resume})
			if err != nil {
				return err
			}
			if !report.Verified {
				return errors.New("import returned without complete verification")
			}
			if err := json.NewEncoder(out).Encode(report); err != nil {
				return err
			}
			imported = append(imported, s)
		}
	}
	if *checkReady {
		return nil
	}
	// Empty/absent optional stores are initialized only after every requested
	// SQLite import has verified, so an account failure cannot cut over telemetry.
	for _, s := range stores {
		if s.dsn == "" || s.source != "" {
			continue
		}
		db, err := pgutil.Open(s.dsn, false)
		if err != nil {
			return err
		}
		if s.kind == "telemetry" {
			err = dbschema.Apply(db, nil)
		} else {
			err = users.Apply(db)
		}
		db.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s PostgreSQL schema initialized\n", s.kind)
	}
	for i, s := range imported {
		if err := finalizeImport(ctx, s.dsn, s.kind); err != nil {
			// There is no cross-database transaction. Writers stay offline throughout
			// cutover; close any earlier readiness gate if a later finalization fails.
			for _, prior := range imported[:i] {
				db, openErr := pgutil.Open(prior.dsn, false)
				if openErr == nil {
					_, resetErr := db.Exec(`UPDATE corescope_schema SET ready=false WHERE kind=$1`, prior.kind)
					db.Close()
					if resetErr != nil {
						return errors.Join(err, errors.New("an earlier readiness gate could not be reset; keep services stopped"))
					}
				} else {
					return errors.Join(err, errors.New("an earlier readiness gate could not be reset; keep services stopped"))
				}
			}
			return err
		}
	}
	for _, s := range imported {
		fmt.Fprintf(out, "%s import verified and finalized; keep the recovery snapshot for rollback\n", s.kind)
	}
	return nil
}

func checkImportedSource(ctx context.Context, databaseURL, kind, source string) error {
	db, err := pgutil.Open(databaseURL, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if kind == "telemetry" {
		err = dbschema.AssertReady(db)
	} else {
		err = users.AssertReady(db)
	}
	if err != nil {
		return err
	}
	var runID, recordedKind, digest, normalized, reportText string
	var verified bool
	if err := db.QueryRowContext(ctx, `SELECT run_id,kind,source_digest,normalized_digest,verified,report FROM corescope_import WHERE id=1`).Scan(&runID, &recordedKind, &digest, &normalized, &verified, &reportText); err != nil {
		return errors.New("PostgreSQL has no completed SQLite import; run the explicit offline upgrade before starting services")
	}
	var report importReport
	if !verified || recordedKind != kind || runID == "" || normalized == "" || json.Unmarshal([]byte(reportText), &report) != nil || !report.Verified || report.Kind != kind {
		return errors.New("PostgreSQL import verification marker is incomplete or belongs to another store")
	}
	tables, err := layouts(kind)
	if err != nil {
		return err
	}
	if len(report.Tables) != len(tables) {
		return errors.New("PostgreSQL import report is incomplete")
	}
	for i, table := range tables {
		if report.Tables[i].Table != table.Name || report.Tables[i].Rows < 0 || len(report.Tables[i].SHA256) != 64 {
			return errors.New("PostgreSQL import report is invalid")
		}
	}
	actual, err := fingerprint(ctx, source)
	if err != nil {
		return err
	}
	if actual != digest {
		return errors.New("retained SQLite source does not match this PostgreSQL import; keep services stopped")
	}
	return nil
}
