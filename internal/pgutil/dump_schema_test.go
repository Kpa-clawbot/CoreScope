package pgutil_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestDumpPublicSchemaOwnerRestoresWholeDatabase(t *testing.T) {
	for _, schema := range []string{"public", `"public"`} {
		t.Run(schema, func(t *testing.T) {
			dsn := pgtest.NewDatabase(t)
			u, _ := url.Parse(dsn)
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			dsn = u.String()
			db, err := pgutil.Open(dsn, false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE sample(id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, value TEXT);
			 INSERT INTO sample(value) VALUES('kept'),('deleted'); DELETE FROM sample WHERE id=2;
			 CREATE SCHEMA auxiliary; CREATE TABLE auxiliary.retained(value TEXT); INSERT INTO auxiliary.retained VALUES('whole database')`); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "owner.dump")
			if err := pgutil.Dump(context.Background(), dsn, path); err != nil {
				t.Fatal(err)
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
				t.Fatal("native whole-database restore failed")
			}
			restored, err := pgutil.Open(target, false)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var kept, auxiliary string
			if err := restored.QueryRow(`SELECT value FROM sample WHERE id=1`).Scan(&kept); err != nil || kept != "kept" {
				t.Fatal("restored public data changed", err)
			}
			if err := restored.QueryRow(`SELECT value FROM auxiliary.retained`).Scan(&auxiliary); err != nil || auxiliary != "whole database" {
				t.Fatal("public search_path incorrectly filtered the native archive", err)
			}
			var nextID int
			if err := restored.QueryRow(`INSERT INTO sample(value) VALUES('next') RETURNING id`).Scan(&nextID); err != nil || nextID != 3 {
				t.Fatal("restored identity high-water changed", err)
			}
		})
	}
}

func TestDumpRejectsCustomOrMultipleSchemasBeforeOutput(t *testing.T) {
	for _, schema := range []string{"custom", "public,custom", `"public","custom"`, `"Public"`, "$user,public", "pg_catalog"} {
		t.Run(schema, func(t *testing.T) {
			u, _ := url.Parse("postgresql://owner:private-sentinel@localhost/example?sslmode=disable")
			q := u.Query()
			q.Set("search_path", schema)
			u.RawQuery = q.Encode()
			path := filepath.Join(t.TempDir(), "refused.dump")
			err := pgutil.Dump(context.Background(), u.String(), path)
			if err == nil || !strings.Contains(err.Error(), "default public schema") || strings.Contains(err.Error(), "private-sentinel") {
				t.Fatal("unsupported backup scope was accepted or exposed credentials")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("rejected schema left an output file")
			}
		})
	}
}
