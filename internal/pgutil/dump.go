package pgutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommandEnv supplies libpq credentials through the child environment, never
// through argv. It removes inherited PG settings so a dump cannot reach a
// different database than the application. Callers must never log this slice.
func CommandEnv(dsn string) ([]string, error) {
	c, err := ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(c.ConnString())
	env := make([]string, 0, len(os.Environ())+12)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "PG") {
			env = append(env, e)
		}
	}
	env = append(env, "PGHOST="+c.Host, "PGPORT="+strconv.Itoa(int(c.Port)), "PGDATABASE="+c.Database,
		"PGUSER="+c.User, "PGPASSWORD="+c.Password, "PGCONNECT_TIMEOUT="+strconv.Itoa(int(c.ConnectTimeout/time.Second)))
	for _, pair := range [][2]string{{"sslmode", "PGSSLMODE"}, {"sslrootcert", "PGSSLROOTCERT"},
		{"sslcert", "PGSSLCERT"}, {"sslkey", "PGSSLKEY"}, {"sslpassword", "PGSSLPASSWORD"},
		{"target_session_attrs", "PGTARGETSESSIONATTRS"}, {"application_name", "PGAPPNAME"}} {
		if value := u.Query().Get(pair[0]); value != "" {
			env = append(env, pair[1]+"="+value)
		}
	}
	return env, nil
}

// Dump writes a consistent native custom-format archive with owner-only mode.
// It never overwrites an existing file, and removes its own partial output on
// any failure or cancellation. The PostgreSQL client executable must be on PATH.
func Dump(ctx context.Context, dsn, path string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	env, err := CommandEnv(dsn)
	if err != nil {
		return err
	}
	u, _ := url.Parse(dsn)
	// Installed selections pin the default schema explicitly. pg_dump still
	// exports the whole database; do not turn this into a schema-filtered dump.
	switch strings.TrimSpace(u.Query().Get("search_path")) {
	case "", "public", `"public"`:
	default:
		return errors.New("postgres: native backup requires the default public schema")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("postgres: snapshot target already exists")
		}
		return fmt.Errorf("postgres: create snapshot: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-password", "--no-owner", "--no-privileges")
	cmd.Env, cmd.Stdout = env, f
	// Never return client stderr: libpq errors can include connection settings.
	if runErr := cmd.Run(); runErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("postgres: pg_dump failed; check client installation, version and backup privileges")
	}
	return f.Sync()
}
