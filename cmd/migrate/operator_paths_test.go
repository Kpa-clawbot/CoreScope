package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestOperatorCustomSourcePathsAndOptionalAccounts(t *testing.T) {
	for _, existingAccounts := range []bool{false, true} {
		t.Run(map[bool]string{false: "no previous accounts", true: "preserve existing accounts"}[existingAccounts], func(t *testing.T) {
			t.Setenv("CORESCOPE_DATABASE_URL", pgtest.NewDatabase(t))
			accounts := pgtest.NewDatabase(t)
			t.Setenv("CORESCOPE_USERS_DATABASE_URL", accounts)
			dir := filepath.Join(t.TempDir(), "custom state and sources")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(dir, "telemetry retained.db")
			if err := os.Rename(telemetrySource(t), source); err != nil {
				t.Fatal(err)
			}
			args := []string{"-offline", "-from-sqlite", source, "-state-dir", filepath.Join(dir, "migration snapshots")}
			paths := []string{source}
			if existingAccounts {
				users := filepath.Join(dir, "existing accounts.db")
				if err := os.Rename(accountSource(t, 5), users); err != nil {
					t.Fatal(err)
				}
				args = append(args, "-users-from-sqlite", users)
				paths = append(paths, users)
			}
			before := make([]string, len(paths))
			for i, path := range paths {
				var err error
				before[i], err = fingerprint(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := runCommand(context.Background(), args, &output); err != nil {
				t.Fatal(err)
			}
			if err := runCommand(context.Background(), []string{"-check-ready"}, &output); err != nil {
				t.Fatal(err)
			}
			for i, path := range paths {
				kind, flag := "telemetry", "-from-sqlite"
				if i == 1 {
					kind, flag = "accounts", "-users-from-sqlite"
				}
				if err := runCommand(context.Background(), []string{"-check-import-kind=" + kind, flag, path}, &output); err != nil {
					t.Fatal(err)
				}
				after, err := fingerprint(context.Background(), path)
				if err != nil || before[i] != after {
					t.Fatal("operator import/check changed the retained source", err)
				}
			}
			var n int
			if err := openImportDB(t, accounts).QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (existingAccounts && n != 1) || (!existingAccounts && n != 0) {
				t.Fatal("optional account initialization lost or invented accounts")
			}
			for _, kind := range []string{"telemetry", "accounts"} {
				if !strings.Contains(output.String(), kind+" PostgreSQL schema is ready") {
					t.Fatal("documented readiness message is missing")
				}
			}
			if !existingAccounts && !strings.Contains(output.String(), "accounts PostgreSQL schema initialized") {
				t.Fatal("documented absent-account initialization message is missing")
			}
		})
	}
}
