package dbx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SearchPathProbe configures ResolveSearchPath and PinSearchPath.
type SearchPathProbe struct {
	// Role names the connecting role in log lines and errors ("writer",
	// "reader").
	Role string
	// Ping makes the probe's first connection (a caller's retrying ping);
	// nil uses db.PingContext.
	Ping func(ctx context.Context, db *sql.DB) error
	// Logf receives the pin's log lines; nil logs nothing.
	Logf func(format string, args ...any)
}

// PinSearchPath returns dsn with the connecting role's search_path pinned as
// a startup parameter (ResolveSearchPath), so every connection a pool opens
// with it resolves unqualified names without the "$user" hazard. A DSN that
// sets search_path itself is returned unchanged.
//
// No error it returns quotes the DSN; driver errors are passed through
// wrapped (pgx leaves the password out of them), for the caller to scrub.
func PinSearchPath(ctx context.Context, dsn string, p SearchPathProbe) (string, error) {
	path, pin, err := ResolveSearchPath(ctx, dsn, p)
	if err != nil || !pin {
		return dsn, err
	}
	return WithSearchPath(dsn, path)
}

// ResolveSearchPath decides the search_path a role connecting with dsn should
// be pinned to. The default search_path starts with "$user": a schema named
// after the role, which any role allowed to CREATE in the database could
// create, would come first and capture the role's unqualified table names
// (and, for a writer, run triggers of its own on every INSERT).
//
// A search_path the DSN sets (as a parameter or in options, PGOPTIONS
// included) is the operator's explicit choice: pin is false and nothing is
// connected to. Otherwise one probe connection reads the session's own value
// (built in, or from ALTER ROLE / ALTER DATABASE / postgresql.conf) and path
// is that value with "$user" resolved now: to the role's own schema when one
// by that name exists and is owned by the role (only the role or a superuser
// could have made it), else dropped with a warning naming the owner. The
// standard setup (the built-in "$user", public with no such schema) pins
// public.
func ResolveSearchPath(ctx context.Context, dsn string, p SearchPathProbe) (path string, pin bool, err error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Not wrapped: pgx's parse error quotes the connection string.
		return "", false, errors.New("opening postgres: db.dsn does not parse")
	}
	if setsSearchPath(cfg.RuntimeParams) {
		return "", false, nil
	}
	logf := p.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ping := p.Ping
	if ping == nil {
		ping = func(ctx context.Context, db *sql.DB) error { return db.PingContext(ctx) }
	}

	probe, err := sql.Open(PostgresDriverName, dsn)
	if err != nil {
		return "", false, fmt.Errorf("opening postgres: %w", err)
	}
	defer probe.Close()
	probe.SetMaxOpenConns(1)
	if err := ping(ctx, probe); err != nil {
		return "", false, fmt.Errorf("pinging postgres: %w", err)
	}
	// Everything is qualified: this session still has the unpinned path
	// (pg_catalog is searched first anyway, so = and casts are the built-in
	// ones).
	var setting, user string
	var owner sql.NullString
	if err := probe.QueryRowContext(ctx, `
		SELECT pg_catalog.current_setting('search_path'), current_user,
		       (SELECT pg_catalog.pg_get_userbyid(n.nspowner) FROM pg_catalog.pg_namespace n WHERE n.nspname = current_user)`,
	).Scan(&setting, &user, &owner); err != nil {
		return "", false, fmt.Errorf("reading the %s's search_path: %w", p.Role, err)
	}
	path = safeSearchPath(setting, user, owner.String)
	if owner.Valid && owner.String != user && strings.Contains(setting, "$user") {
		logf("[db] WARNING: schema %q is named after the %s role but owned by %q; it is left out of the %s's search_path", user, p.Role, owner.String, p.Role)
	}
	logf("[db] %s search_path pinned to %s", p.Role, path)
	return path, true, nil
}

// setsSearchPath reports whether the DSN's startup parameters set
// search_path, directly or through options ("-c search_path=..." or
// "--search-path=...").
func setsSearchPath(params map[string]string) bool {
	for k, v := range params {
		switch strings.ToLower(k) {
		case "search_path":
			return true
		case "options":
			if strings.Contains(strings.ReplaceAll(strings.ToLower(v), "-", "_"), "search_path") {
				return true
			}
		}
	}
	return false
}

// safeSearchPath is setting (a search_path value as current_setting returns
// it) with every "$user" element replaced by user's schema when owner, the
// owner of the schema named user ("" when there is none), is user itself,
// and dropped otherwise. Other elements are kept as written.
func safeSearchPath(setting, user, owner string) string {
	var out []string
	for _, el := range splitSearchPath(setting) {
		if el == `"$user"` || el == `$user` {
			if owner != "" && owner == user {
				out = append(out, `"`+strings.ReplaceAll(user, `"`, `""`)+`"`)
			}
			continue
		}
		out = append(out, el)
	}
	return strings.Join(out, ", ")
}

// splitSearchPath splits a search_path value at the commas outside double
// quotes and trims each element.
func splitSearchPath(setting string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	flush := func() {
		if el := strings.TrimSpace(b.String()); el != "" {
			out = append(out, el)
		}
		b.Reset()
	}
	for _, c := range setting {
		switch {
		case c == '"':
			quoted = !quoted // a doubled "" toggles twice and stays quoted
		case c == ',' && !quoted:
			flush()
			continue
		}
		b.WriteRune(c)
	}
	flush()
	return out
}

// WithSearchPath returns dsn, a postgres:// URL or a keyword/value string,
// with search_path set to path as a startup parameter: a URL's existing
// search_path parameters are replaced, and in a keyword/value string the
// one appended last wins. The rest of the DSN is kept byte for byte: a URL's query is not decoded and re-encoded, since pgx reads it the
// way libpq does ("+" is a plus sign, not a space) and a round trip through
// net/url would change such values. Errors never quote the DSN.
func WithSearchPath(dsn, path string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			// Not wrapped: url.Error echoes the DSN, password included.
			return "", errors.New("opening postgres: db.dsn is not a valid postgres:// URL")
		}
		var kept []string
		for _, seg := range strings.Split(u.RawQuery, "&") {
			k, _, _ := strings.Cut(seg, "=")
			if key, err := url.PathUnescape(k); seg == "" || (err == nil && strings.EqualFold(key, "search_path")) {
				continue
			}
			kept = append(kept, seg)
		}
		// pgx does not read "+" in a query value as a space; %20 it does.
		// QueryEscape writes a literal "+" as %2B, so every "+" left is a
		// space.
		kept = append(kept, "search_path="+strings.ReplaceAll(url.QueryEscape(path), "+", "%20"))
		u.RawQuery = strings.Join(kept, "&")
		return u.String(), nil
	}
	esc := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(path)
	return dsn + " search_path='" + esc + "'", nil
}
