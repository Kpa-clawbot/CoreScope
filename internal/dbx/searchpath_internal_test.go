package dbx

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSafeSearchPath(t *testing.T) {
	for _, tc := range []struct{ setting, user, owner, want string }{
		{`"$user", public`, "corescope", "", "public"},                       // the standard setup
		{`"$user", public`, "corescope", "intruder", "public"},               // planted
		{`"$user", public`, "corescope", "corescope", `"corescope", public`}, // the role's own
		{`$user,public`, "w", "w", `"w", public`},
		{`"$user", public`, `we"ird`, `we"ird`, `"we""ird", public`},
		{`myschema`, "corescope", "intruder", "myschema"},
		{`"my, schema", public`, "corescope", "", `"my, schema", public`},
		{`"$user"`, "corescope", "", ""},
		{``, "corescope", "", ""},
	} {
		if got := safeSearchPath(tc.setting, tc.user, tc.owner); got != tc.want {
			t.Errorf("safeSearchPath(%q, %q, %q) = %q; want %q", tc.setting, tc.user, tc.owner, got, tc.want)
		}
	}
}

func TestSetsSearchPath(t *testing.T) {
	for dsn, want := range map[string]bool{
		"postgres://u@h/db":                                           false,
		"postgres://u@h/db?search_path=s":                             true,
		"postgres://u@h/db?options=-c%20search_path%3Ds":              true,
		"postgres://u@h/db?options=--search-path%3Ds":                 true,
		"postgres://u@h/db?options=-c%20statement_timeout%3D5":        false,
		"host=h user=u dbname=db":                                     false,
		"host=h user=u dbname=db search_path=s":                       true,
		"host=h user=u dbname=db options='-c search_path=s'":          true,
		"host=h user=u dbname=db application_name=search_path_lookup": false,
	} {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		if got := setsSearchPath(cfg.RuntimeParams); got != want {
			t.Errorf("setsSearchPath(%s) = %v; want %v", dsn, got, want)
		}
	}
}

// The pinned value reaches pgx's startup parameters intact in both DSN forms,
// and the rest of the DSN reaches pgx as it did before.
func TestWithSearchPath(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:p@h:5432/db?sslmode=disable",
		"postgres://u:p@h:5432/db?sslmode=disable&search_path=old&Search_Path=older",
		// libpq-style values pgx reads literally: "+" is a plus sign, %20 a
		// space. A net/url round trip would turn the first into a space and
		// the second into a "+".
		"postgres://u:p@h:5432/db?application_name=a+b&options=-c%20work_mem%3D64MB",
		"host=h user=u password='a b' dbname=db",
		"host=h user=u dbname=db search_path=old",
	} {
		before, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"public", `"corescope", public`, `"it's", public`, `"back\slash"`, `"a+b", "50%"`, ""} {
			got, err := WithSearchPath(dsn, path)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := pgx.ParseConfig(got)
			if err != nil {
				t.Fatalf("%q: %v", got, err)
			}
			if v, ok := cfg.RuntimeParams["search_path"]; !ok || v != path {
				t.Errorf("%s + %q: search_path = %q (%v)", dsn, path, v, ok)
			}
			if cfg.User != "u" || cfg.Database != "db" || cfg.Password != before.Password {
				t.Errorf("%s + %q: lost the rest of the DSN: %+v", dsn, path, cfg)
			}
			for k, v := range before.RuntimeParams {
				if !strings.EqualFold(k, "search_path") && cfg.RuntimeParams[k] != v {
					t.Errorf("%s + %q: %s = %q; was %q", dsn, path, k, cfg.RuntimeParams[k], v)
				}
			}
			for k := range cfg.RuntimeParams {
				if strings.EqualFold(k, "search_path") && k != "search_path" {
					t.Errorf("%s + %q: a second search_path survived as %s", dsn, path, k)
				}
			}
		}
	}
}

func TestWithSearchPathErrorOmitsDSN(t *testing.T) {
	_, err := WithSearchPath("postgres://u:s3cret@h/%zz", "public")
	if err == nil {
		t.Fatal("want an error for an unparseable URL")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error quotes the DSN: %v", err)
	}
}

// A DSN that sets search_path is kept as given, without connecting: the
// host here does not resolve, so a probe would fail.
func TestPinSearchPathKeepsDSNSearchPathWithoutConnecting(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:p@cs-no-such-host.invalid/db?search_path=mine",
		"postgres://u:p@cs-no-such-host.invalid/db?options=-c%20search_path%3Dmine",
		"host=cs-no-such-host.invalid user=u password=p dbname=db options='--search-path=mine'",
	} {
		pinged := false
		got, err := PinSearchPath(context.Background(), dsn, SearchPathProbe{
			Role: "reader",
			Ping: func(context.Context, *sql.DB) error { pinged = true; return nil },
		})
		if err != nil || got != dsn || pinged {
			t.Errorf("%s: got %q, %v (pinged %v); want the DSN unchanged and no probe", dsn, got, err, pinged)
		}
	}
}

func TestPinSearchPathErrorsOmitDSN(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:s3cret@h:notaport/db",
		"host=h password=s3cret port=notaport",
	} {
		_, err := PinSearchPath(context.Background(), dsn, SearchPathProbe{Role: "reader"})
		if err == nil {
			t.Errorf("%s: want an error", dsn)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error quotes the DSN: %v", err)
		}
	}
	// A probe that cannot connect: the error is the driver's, wrapped, and
	// pgx leaves the password out of it.
	_, err := PinSearchPath(context.Background(), "postgres://u:s3cret@127.0.0.1:1/db?connect_timeout=2", SearchPathProbe{Role: "reader"})
	if err == nil || !strings.HasPrefix(err.Error(), "pinging postgres: ") {
		t.Fatalf("err = %v; want a ping failure", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error quotes the DSN: %v", err)
	}
}
