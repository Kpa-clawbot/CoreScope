// Package dbconfig provides shared database settings used by both the server
// and ingestor binaries.
package dbconfig

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/stdlib"
)

const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"

	postgresQMarkDriver = "corescope-postgres-qmark"
)

// DBConfig controls database driver selection, connection pool sizing, and
// SQLite-only vacuum maintenance behavior (#919).
type DBConfig struct {
	VacuumOnStartup        bool   `json:"vacuumOnStartup"`        // one-time full VACUUM on startup if auto_vacuum is not INCREMENTAL
	IncrementalVacuumPages int    `json:"incrementalVacuumPages"` // pages returned to OS per reaper cycle (default 1024)
	Driver                 string `json:"driver,omitempty"`       // sqlite|postgres
	URL                    string `json:"url,omitempty"`          // Postgres connection URL; env DATABASE_URL overrides this
	Path                   string `json:"path,omitempty"`         // SQLite path; legacy top-level dbPath is still supported
	MaxOpenConns           int    `json:"maxOpenConns,omitempty"`
	MaxIdleConns           int    `json:"maxIdleConns,omitempty"`
}

// GetIncrementalVacuumPages returns the configured pages or 1024 default.
func (c *DBConfig) GetIncrementalVacuumPages() int {
	if c != nil && c.IncrementalVacuumPages > 0 {
		return c.IncrementalVacuumPages
	}
	return 1024
}

// Settings is the resolved database configuration after environment and legacy
// top-level dbPath fallbacks have been applied.
type Settings struct {
	Driver       string
	URL          string
	Path         string
	MaxOpenConns int
	MaxIdleConns int
}

// Resolve returns final DB settings. DATABASE_URL selects Postgres unless
// DB_DRIVER is explicitly set. DB_PATH preserves the legacy SQLite override.
func Resolve(cfg *DBConfig, legacyPath string) Settings {
	s := Settings{Driver: DriverSQLite, Path: legacyPath}
	if cfg != nil {
		if cfg.Driver != "" {
			s.Driver = strings.ToLower(strings.TrimSpace(cfg.Driver))
		}
		if cfg.URL != "" {
			s.URL = strings.TrimSpace(cfg.URL)
		}
		if cfg.Path != "" {
			s.Path = strings.TrimSpace(cfg.Path)
		}
		s.MaxOpenConns = cfg.MaxOpenConns
		s.MaxIdleConns = cfg.MaxIdleConns
	}
	if v := os.Getenv("DATABASE_URL"); strings.TrimSpace(v) != "" {
		s.URL = strings.TrimSpace(v)
		if s.Driver == "" || s.Driver == DriverSQLite {
			s.Driver = DriverPostgres
		}
	}
	if v := os.Getenv("DB_DRIVER"); strings.TrimSpace(v) != "" {
		s.Driver = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("DB_PATH"); strings.TrimSpace(v) != "" {
		s.Path = strings.TrimSpace(v)
	}
	if s.Driver == "" {
		s.Driver = DriverSQLite
	}
	if s.Driver == DriverPostgres && s.URL == "" {
		s.URL = os.Getenv("PGDATABASE")
	}
	if s.Path == "" {
		s.Path = "data/meshcore.db"
	}
	return s
}

func (s Settings) IsPostgres() bool { return strings.EqualFold(s.Driver, DriverPostgres) }
func (s Settings) IsSQLite() bool   { return !s.IsPostgres() }

func (s Settings) Label() string {
	if s.IsPostgres() {
		return DriverPostgres
	}
	return DriverSQLite
}

func (s Settings) DataSource() string {
	if s.IsPostgres() {
		return s.URL
	}
	return s.Path
}

func (s Settings) SQLDriverName() string {
	if s.IsPostgres() {
		RegisterPostgresQMark()
		return postgresQMarkDriver
	}
	return DriverSQLite
}

var registerPostgresOnce sync.Once

// RegisterPostgresQMark registers a pgx-backed database/sql driver that accepts
// the existing CoreScope qmark placeholders and rewrites them for PostgreSQL.
func RegisterPostgresQMark() {
	registerPostgresOnce.Do(func() {
		sql.Register(postgresQMarkDriver, &qmarkDriver{inner: stdlib.GetDefaultDriver()})
	})
}

type qmarkDriver struct {
	inner driver.Driver
}

func (d *qmarkDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &qmarkConn{Conn: c}, nil
}

type qmarkConn struct {
	driver.Conn
}

func (c *qmarkConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(rebindPostgresPlaceholders(query))
}

func (c *qmarkConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, rebindPostgresPlaceholders(query))
	}
	return c.Prepare(query)
}

func (c *qmarkConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	if ex, ok := c.Conn.(driver.Execer); ok {
		return ex.Exec(rebindPostgresPlaceholders(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ex, ok := c.Conn.(driver.ExecerContext); ok {
		return ex.ExecContext(ctx, rebindPostgresPlaceholders(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	if q, ok := c.Conn.(driver.Queryer); ok {
		return q.Query(rebindPostgresPlaceholders(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.Conn.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, rebindPostgresPlaceholders(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *qmarkConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *qmarkConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *qmarkConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// rebindPostgresPlaceholders rewrites qmark placeholders to $1, $2, ...
// while leaving quoted strings and SQL comments untouched.
func rebindPostgresPlaceholders(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}
		if inLineComment {
			b.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			b.WriteByte(ch)
			if ch == '*' && next == '/' {
				b.WriteByte(next)
				i++
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			b.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					b.WriteByte(next)
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if inDouble {
			b.WriteByte(ch)
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		switch {
		case ch == '-' && next == '-':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			b.WriteByte(ch)
			b.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '\'':
			b.WriteByte(ch)
			inSingle = true
		case ch == '"':
			b.WriteByte(ch)
			inDouble = true
		case ch == '?':
			b.WriteString(fmt.Sprintf("$%d", arg))
			arg++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}
