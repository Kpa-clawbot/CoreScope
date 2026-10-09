// Package pgutil contains PostgreSQL connection and native backup helpers.
package pgutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// ParseConfig requires an explicit PostgreSQL URL. Errors never include the
// URL: driver parse errors can contain credentials, including URL parameters.
func ParseConfig(dsn string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("postgres: expected a PostgreSQL database URL")
	}
	if u.Hostname() == "" || strings.Contains(u.Host, ",") || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return nil, errors.New("postgres: database URL requires one host, username and database")
	}
	allowed := map[string]bool{"sslmode": true, "sslrootcert": true, "sslcert": true, "sslkey": true, "sslpassword": true,
		"target_session_attrs": true, "connect_timeout": true, "application_name": true, "search_path": true}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("postgres: invalid database URL query")
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return nil, errors.New("postgres: unsupported or repeated database URL parameter")
		}
	}
	// Normalize TLS environment defaults into the parsed URL so libpq and pgx
	// receive the same connection policy. Service files/options and multi-host
	// URLs are deliberately unsupported rather than silently changing a backup.
	if os.Getenv("PGSERVICE") != "" || os.Getenv("PGOPTIONS") != "" {
		return nil, errors.New("postgres: use explicit database URL settings instead of PGSERVICE or PGOPTIONS")
	}
	for _, pair := range [][2]string{{"sslmode", "PGSSLMODE"}, {"sslrootcert", "PGSSLROOTCERT"}, {"sslcert", "PGSSLCERT"}, {"sslkey", "PGSSLKEY"}, {"sslpassword", "PGSSLPASSWORD"}} {
		if !q.Has(pair[0]) && os.Getenv(pair[1]) != "" {
			q.Set(pair[0], os.Getenv(pair[1]))
		}
	}
	if !q.Has("sslmode") {
		q.Set("sslmode", "prefer")
	}
	u.RawQuery = q.Encode()
	c, err := pgx.ParseConfig(u.String())
	if err != nil || c.Database == "" {
		return nil, errors.New("postgres: invalid database URL or missing database name")
	}
	// Ten seconds is the connection upper bound; retain a shorter explicit limit.
	if c.ConnectTimeout <= 0 || c.ConnectTimeout > 10*time.Second {
		c.ConnectTimeout = 10 * time.Second
	}
	return c, nil
}

// Open returns a bounded pgx database/sql pool. readOnly is defense in depth;
// deployments must also grant the runtime role only its required privileges.
func Open(dsn string, readOnly bool) (*sql.DB, error) {
	c, err := ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	c.RuntimeParams["timezone"] = "UTC"
	if readOnly {
		c.RuntimeParams["default_transaction_read_only"] = "on"
	}
	db := stdlib.OpenDB(*c)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("postgres: connection failed; check database URL, credentials and server availability")
	}
	return db, nil
}

// AssertReadOnly checks effective grants, including inherited grants. The
// session's read-only setting alone is insufficient: a client can change it.
func AssertReadOnly(db *sql.DB) error {
	var writable bool
	err := db.QueryRow(`SELECT
		EXISTS (SELECT 1 FROM pg_roles WHERE rolname=current_user AND (rolsuper OR rolcreaterole OR rolcreatedb))
		OR has_database_privilege(current_database(), 'CREATE')
		OR EXISTS (SELECT 1 FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname <> 'information_schema'
			AND has_schema_privilege(oid, 'CREATE'))
		OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r','p','v','m','f')
			AND has_table_privilege(c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER'))`).Scan(&writable)
	if err != nil {
		return errors.New("postgres: cannot verify reader privileges")
	}
	if writable {
		return errors.New("postgres: reader role has write or schema privileges")
	}
	return nil
}

// SameDatabase compares the actual databases reached by two URLs, independent
// of aliases, proxies, usernames, search_path, and URL spelling. Advisory locks
// are database-local and need no DDL/data write privilege. Both transactions
// always roll back; the random lock leaves no persistent state.
func SameDatabase(ctx context.Context, left, right string) (bool, error) {
	a, err := Open(left, true)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := Open(right, true)
	if err != nil {
		return false, err
	}
	defer b.Close()
	ta, err := a.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("postgres: database identity check failed")
	}
	defer ta.Rollback()
	tb, err := b.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("postgres: database identity check failed")
	}
	defer tb.Rollback()
	var key [8]byte
	if _, err := rand.Read(key[:]); err != nil {
		return false, err
	}
	id := int64(binary.BigEndian.Uint64(key[:]))
	var locked bool
	if err := ta.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, id).Scan(&locked); err != nil || !locked {
		return false, errors.New("postgres: database identity lock unavailable")
	}
	if err := tb.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, id).Scan(&locked); err != nil {
		return false, fmt.Errorf("postgres: database identity probe failed")
	}
	return !locked, nil
}
