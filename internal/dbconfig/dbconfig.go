// Package dbconfig provides the shared DBConfig struct used by both the server
// and ingestor binaries for SQLite vacuum and maintenance settings (#919, #921).
package dbconfig

import (
	"fmt"
	"strings"
)

// Supported database backends.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// DBConfig controls SQLite vacuum and maintenance behavior (#919).
type DBConfig struct {
	VacuumOnStartup        bool `json:"vacuumOnStartup"`        // one-time full VACUUM on startup if auto_vacuum is not INCREMENTAL
	IncrementalVacuumPages int  `json:"incrementalVacuumPages"` // pages returned to OS per reaper cycle (default 1024)
	AnalysisLimit          int  `json:"analysisLimit"`          // index rows ANALYZE visits per index (default 10000); negative disables the planner stats refresh (#2058)

	Driver string `json:"driver,omitempty"` // "sqlite" (default) or "postgres"
	DSN    string `json:"dsn,omitempty"`    // Postgres connection string; unused for sqlite

	// StatementTimeoutSec caps each query the API server runs on Postgres
	// (statement_timeout). Unset → DefaultStatementTimeoutSec; 0 disables;
	// negative is a config error. Server only; unused for sqlite.
	StatementTimeoutSec *int `json:"statementTimeoutSec,omitempty"`

	// Load controls chunked startup loading (#1009).
	Load *LoadConfig `json:"load,omitempty"`
}

// LoadConfig controls the chunked startup-load behavior (#1009).
type LoadConfig struct {
	// ChunkSize is the number of transmission rows fetched per chunk
	// during PacketStore.LoadChunked. 0/unset → 10000.
	ChunkSize int `json:"chunkSize"`
}

// GetIncrementalVacuumPages returns the configured pages or 1024 default.
func (c *DBConfig) GetIncrementalVacuumPages() int {
	if c != nil && c.IncrementalVacuumPages > 0 {
		return c.IncrementalVacuumPages
	}
	return 1024
}

// GetLoadChunkSize returns the configured chunk size or 10000 default (#1009).
func (c *DBConfig) GetLoadChunkSize() int {
	if c != nil && c.Load != nil && c.Load.ChunkSize > 0 {
		return c.Load.ChunkSize
	}
	return 10000
}

// DefaultStatementTimeoutSec is the server's Postgres statement_timeout when
// db.statementTimeoutSec is unset. The public API is unauthenticated, so a
// query must not be able to run unbounded.
const DefaultStatementTimeoutSec = 120

// GetStatementTimeoutSec returns db.statementTimeoutSec, or
// DefaultStatementTimeoutSec when unset. 0 means no timeout. A negative value
// is returned as-is; Validate rejects it. Nil-safe.
func (c *DBConfig) GetStatementTimeoutSec() int {
	if c == nil || c.StatementTimeoutSec == nil {
		return DefaultStatementTimeoutSec
	}
	return *c.StatementTimeoutSec
}

// GetDriver normalises Driver: "", "sqlite", "sqlite3" give DriverSQLite;
// "postgres", "postgresql", "pg" give DriverPostgres (case- and
// space-insensitive). Anything else is an error. Nil-safe.
func (c *DBConfig) GetDriver() (string, error) {
	if c == nil {
		return DriverSQLite, nil
	}
	switch strings.ToLower(strings.TrimSpace(c.Driver)) {
	case "", "sqlite", "sqlite3":
		return DriverSQLite, nil
	case "postgres", "postgresql", "pg":
		return DriverPostgres, nil
	}
	return "", fmt.Errorf("db.driver %q is not recognised; use \"sqlite\" (or sqlite3) or \"postgres\" (or postgresql, pg)", c.Driver)
}

// Validate returns an error when the driver is invalid, or when it is
// postgres and DSN is empty, or when statementTimeoutSec is negative. The DSN
// is never included in the message. Nil-safe.
func (c *DBConfig) Validate() error {
	d, err := c.GetDriver()
	if err != nil {
		return err
	}
	if n := c.GetStatementTimeoutSec(); n < 0 {
		return fmt.Errorf("db.statementTimeoutSec is %d; use 0 to disable the timeout or a positive number of seconds", n)
	}
	if d == DriverPostgres && strings.TrimSpace(c.DSN) == "" {
		return fmt.Errorf("db.driver=postgres requires db.dsn (or DB_DSN)")
	}
	return nil
}
