package main

import (
	"fmt"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	backend := os.Getenv("CORESCOPE_TEST_BACKEND")
	if backend != "" && backend != "sqlite" && backend != "postgres" {
		fmt.Fprintln(os.Stderr, "unsupported CORESCOPE_TEST_BACKEND")
		os.Exit(1)
	}
	if backend == "postgres" && os.Getenv("CORESCOPE_TEST_POSTGRES_URL") == "" {
		fmt.Fprintln(os.Stderr, "explicit PostgreSQL matrix requires CORESCOPE_TEST_POSTGRES_URL")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func requirePostgres(t testing.TB) {
	t.Helper()
	switch os.Getenv("CORESCOPE_TEST_BACKEND") {
	case "", "sqlite":
		t.Skip("select CORESCOPE_TEST_BACKEND=postgres for the required PostgreSQL matrix")
	case "postgres":
		if os.Getenv("CORESCOPE_TEST_POSTGRES_URL") == "" {
			t.Fatal("explicit PostgreSQL matrix requires CORESCOPE_TEST_POSTGRES_URL")
		}
	default:
		t.Fatal("unsupported CORESCOPE_TEST_BACKEND")
	}
}
func postgresSchema(t testing.TB) string { t.Helper(); requirePostgres(t); return pgtest.NewSchema(t) }
func postgresDatabase(t testing.TB) string {
	t.Helper()
	requirePostgres(t)
	return pgtest.NewDatabase(t)
}
