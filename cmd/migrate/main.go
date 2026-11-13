// Command migrate runs all dbschema migrations against a SQLite
// CoreScope database and exits. Used by CI / one-shot tooling to bring
// an unmigrated fixture (or a fresh DB) up to the schema shape the
// read-only server (cmd/server) requires via dbschema.AssertReady.
//
// In production the ingestor (cmd/ingestor) runs dbschema.Apply at
// startup before subscribing to MQTT — this binary exists so CI's E2E
// job can migrate the e2e-fixture.db without booting the full ingestor
// (which needs MQTT brokers).
//
// It also moves an installation between backends: copy streams every row of
// a fully migrated source into an empty target (SQLite or Postgres, either
// direction) and verifies the result; verify compares two databases.
//
// Usage:
//
//	migrate -db path/to/file.db
//	migrate copy   -from <db> -to <db> [-batch 5000]
//	migrate verify -a <db> -b <db>
//
// where <db> is sqlite:<path> or a postgres:// / postgresql:// URL.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "copy" || os.Args[1] == "verify") {
		os.Exit(runSubcommand(os.Args[1], os.Args[2:], os.Stderr))
	}
	legacyMain()
}

// runSubcommand runs copy or verify, writing all output to w through the
// password scrubber, and returns the process exit code.
func runSubcommand(cmd string, args []string, w io.Writer) int {
	sw := newScrubWriter(w)
	lg := log.New(sw, "[migrate] ", log.LstdFlags|log.Lmsgprefix)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	if cmd == "copy" {
		err = runCopy(ctx, args, lg, sw)
	} else {
		err = runVerify(ctx, args, lg, sw)
	}
	switch {
	case errors.Is(err, errUsage):
		return 2
	case err != nil:
		lg.Printf("FAILED: %v", err)
		return 1
	}
	return 0
}

// legacyMain is `migrate -db <path>`: run dbschema.Apply on a SQLite file.
func legacyMain() {
	dbPath := flag.String("db", "", "path to SQLite database to migrate (required)")
	flag.Parse()

	if *dbPath == "" {
		log.Fatalf("[migrate] -db is required")
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[migrate] ")

	// Same DSN as the ingestor. A bare path here would mean synchronous=NORMAL
	// under mattn, quietly weakening durability for a process that writes — and
	// this one runs dbschema.Apply, which now also deletes duplicate rows.
	db, err := sql.Open("sqlite3", dbschema.WriterDSN(*dbPath))
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("ping %s: %v", *dbPath, err)
	}

	if err := dbschema.Apply(db, log.Printf); err != nil {
		log.Fatalf("dbschema.Apply: %v", err)
	}

	if err := dbschema.AssertReady(db); err != nil {
		log.Fatalf("dbschema.AssertReady after Apply: %v (this is a bug — Apply did not produce a ready schema)", err)
	}

	log.Printf("OK: %s is migrated and ready", *dbPath)
}
