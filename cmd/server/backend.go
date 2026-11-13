package main

import (
	"github.com/meshcore-analyzer/mbcapqueue"
	"github.com/meshcore-analyzer/prunequeue"
)

// The server reads either a SQLite file or a Postgres database. Almost all of
// its SQL is written to run unchanged on both (the Postgres driver in
// internal/dbx rewrites ? placeholders); the statements that cannot be shared
// pick their text with sqlFor, so both forms sit side by side where they are
// used and a reviewer sees them together.

// isPostgres reports whether db reads a Postgres database. Nil-safe.
func (db *DB) isPostgres() bool { return db != nil && db.pg }

// sqlFor returns sqlite on SQLite and postgres on Postgres. Use it only where
// no single statement works on both; prefer portable SQL everywhere else.
func (db *DB) sqlFor(sqlite, postgres string) string {
	if db.isPostgres() {
		return postgres
	}
	return sqlite
}

// queueAnchor is the path files kept beside the database are placed by: the
// SQLite database file, or on Postgres the configured dbPath (default
// data/meshcore.db). On SQLite the file queues shared with the ingestor
// (prunequeue, mbcapqueue) live there; on Postgres they are control tables
// (see pruneQueue), and the anchor only places local files such as the
// geo-filter backup.
func (db *DB) queueAnchor() string {
	if db == nil {
		return ""
	}
	if db.queuePath != "" {
		return db.queuePath
	}
	return db.path
}

// pruneQueue returns the geo-prune queue shared with the ingestor: the marker
// files beside the SQLite database, or on Postgres the control tables. It is
// nil on Postgres when the control tables are unavailable to the server's
// role (control_pg.go); callers answer 503 then.
func (db *DB) pruneQueue() prunequeue.Queue {
	if db.isPostgres() {
		return db.pruneQ
	}
	return prunequeue.NewFS(db.queueAnchor())
}

// mbcapStore returns where the multibyte capability snapshot is published
// for the ingestor: the snapshot file beside the SQLite database, or on
// Postgres the control tables. Nil when there is nowhere to publish (no
// database path, or on Postgres the control tables are unavailable).
func (db *DB) mbcapStore() mbcapqueue.Store {
	if db.isPostgres() {
		return db.mbcap
	}
	if db.queueAnchor() == "" {
		return nil
	}
	return mbcapqueue.NewFS(db.queueAnchor())
}
