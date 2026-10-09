package pgutil_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestParseConfigSecretsAndSupportedURL(t *testing.T) {
	for _, dsn := range []string{
		"postgresql://hidden-user:hidden-password@host:bad/db",
		"postgresql://hidden-user:hidden-password@host/db?password=hidden-password",
		"postgresql://hidden-user:hidden-password@one,two/db",
		"postgresql://hidden-user:hidden-password@host/db?hostaddr=elsewhere",
		"postgresql://hidden-user:hidden-password@host/db?options=-csearch_path=elsewhere",
		"postgresql://hidden-user:hidden-password@host/db?sslmode=disable&sslmode=require",
	} {
		_, err := pgutil.ParseConfig(dsn)
		if err == nil {
			t.Fatal("unsupported connection URL accepted")
		}
		if strings.Contains(err.Error(), "hidden-") {
			t.Fatal("connection error leaked a credential")
		}
	}
	c, err := pgutil.ParseConfig("postgresql://user:p%40ss@localhost:5433/accounts?sslmode=disable&search_path=test_schema")
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "p@ss" || c.Database != "accounts" || c.Port != 5433 || c.RuntimeParams["search_path"] != "test_schema" {
		t.Fatal("URL parameters changed")
	}
}

func TestParseConfigRejectsMalformedQueryWithoutTLSDowngrade(t *testing.T) {
	for _, key := range []string{"PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLPASSWORD", "PGSERVICE", "PGOPTIONS"} {
		t.Setenv(key, "")
	}
	for _, test := range []struct{ name, query string }{
		{"bad_value_escape", "sslmode=verify-full%ZZ"},
		{"unescaped_separator", "sslmode=verify-full;application_name=hidden-param"},
		{"bad_key_escape", "ssl%ZZmode=verify-full"},
		{"bad_other_parameter", "sslmode=verify-full&sslpassword=hidden-param%ZZ"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := "postgresql://hidden-user:hidden-password@localhost/accounts?" + test.query
			if _, err := pgutil.ParseConfig(dsn); err == nil {
				t.Fatal("malformed query accepted instead of preserving the requested connection policy")
			} else if strings.Contains(err.Error(), "hidden-") || strings.Contains(err.Error(), test.query) {
				t.Fatal("malformed query error leaked connection settings")
			}
			if _, err := pgutil.CommandEnv(dsn); err == nil {
				t.Fatal("native client accepted the malformed query")
			}
		})
	}
	c, err := pgutil.ParseConfig("postgresql://encoded%20user:p%40ss%25@localhost/accounts?ssl%6dode=verify%2Dfull&application_name=synthetic%3Btls%3Dstrict%26name%3Dread%2Bonly")
	if err != nil {
		t.Fatal("valid encoded URL refused", err)
	}
	if c.User != "encoded user" || c.Password != "p@ss%" || c.RuntimeParams["application_name"] != "synthetic;tls=strict&name=read+only" {
		t.Fatal("valid encoded URL changed")
	}
	if c.TLSConfig == nil || c.TLSConfig.InsecureSkipVerify || len(c.Fallbacks) != 0 {
		t.Fatal("valid strict-TLS URL was downgraded")
	}
}

func TestCommandEnvMatchesConnection(t *testing.T) {
	dsn := "postgresql://someone:secret%40value@localhost:5433/accounts?sslmode=require"
	env, err := pgutil.CommandEnv(dsn)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		values[k] = v
	}
	for k, want := range map[string]string{"PGHOST": "localhost", "PGPORT": "5433", "PGDATABASE": "accounts", "PGUSER": "someone", "PGPASSWORD": "secret@value", "PGSSLMODE": "require"} {
		if values[k] != want {
			t.Errorf("%s did not preserve connection settings", k)
		}
	}
}

func TestConnectTimeoutKeepsShorterLimitAndCapsLonger(t *testing.T) {
	for _, test := range []struct {
		seconds string
		want    time.Duration
	}{{"1", time.Second}, {"30", 10 * time.Second}, {"0", 10 * time.Second}} {
		c, err := pgutil.ParseConfig("postgresql://user:password@localhost/database?connect_timeout=" + test.seconds)
		if err != nil {
			t.Fatal(err)
		}
		if c.ConnectTimeout != test.want {
			t.Fatalf("timeout %s became %s; want %s", test.seconds, c.ConnectTimeout, test.want)
		}
	}
}

func TestSameDatabaseChecksActualTarget(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	reader := pgtest.ReadOnly(t, dsn)
	u, _ := url.Parse(reader)
	q := u.Query()
	q.Set("application_name", "identity-test")
	q.Del("search_path")
	u.RawQuery = q.Encode()
	same, err := pgutil.SameDatabase(context.Background(), dsn, u.String())
	if err != nil || !same {
		t.Fatalf("same database across roles/search paths=%v,%v", same, err)
	}
	other := pgtest.NewDatabase(t)
	same, err = pgutil.SameDatabase(context.Background(), dsn, other)
	if err != nil || same {
		t.Fatalf("separate databases=%v,%v", same, err)
	}
}

func TestReadOnlyRoleDeniesWritesAndDDL(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	owner, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(`CREATE TABLE sample(id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	readerDSN := pgtest.ReadOnly(t, dsn)
	db, err := pgutil.Open(readerDSN, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := pgutil.AssertReadOnly(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`SET default_transaction_read_only=off`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`INSERT INTO sample VALUES(1)`, `UPDATE sample SET id=2`, `DELETE FROM sample`, `CREATE TABLE forbidden(id int)`} {
		if _, err := db.Exec(stmt); err == nil {
			t.Fatalf("reader accepted write: %s", stmt)
		}
	}
	u, _ := url.Parse(readerDSN)
	var database string
	if err := owner.QueryRow(`SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`GRANT CREATE ON DATABASE "` + database + `" TO "` + u.User.Username() + `"`); err != nil {
		t.Fatal(err)
	}
	if err := pgutil.AssertReadOnly(db); err == nil {
		t.Fatal("database CREATE privilege was missed")
	}
}

func TestDumpRestoreAndFailureCleanup(t *testing.T) {
	dsn := pgtest.NewDatabase(t)
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sample(id BIGINT PRIMARY KEY,value TEXT); INSERT INTO sample VALUES(17,'unchanged')`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.dump")
	if err := pgutil.Dump(context.Background(), dsn, path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "PGDMP") {
		t.Fatal("backup is not a native custom archive")
	}
	target := pgtest.NewDatabase(t)
	env, err := pgutil.CommandEnv(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgutil.ParseConfig(target)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("pg_restore", "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "--dbname="+cfg.Database, path)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		t.Fatal("native restore failed")
	}
	restored, err := pgutil.Open(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value string
	if err := restored.QueryRow(`SELECT value FROM sample WHERE id=17`).Scan(&value); err != nil || value != "unchanged" {
		t.Fatalf("restored data=%q,%v", value, err)
	}
	if err := pgutil.Dump(context.Background(), dsn, path); err == nil {
		t.Fatal("existing backup overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	partial := filepath.Join(t.TempDir(), "partial.dump")
	if err := pgutil.Dump(ctx, dsn, partial); err == nil {
		t.Fatal("canceled dump succeeded")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("partial backup remains")
	}
	t.Setenv("PATH", t.TempDir())
	if err := pgutil.Dump(context.Background(), dsn, partial); err == nil {
		t.Fatal("dump succeeded without pg_dump")
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("failed dump left partial backup")
	}
}
