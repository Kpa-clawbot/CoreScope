package dbschema

import (
	"os"
	"testing"

	"github.com/meshcore-analyzer/pgutil/pgtest"
)

// PostgreSQL-only assertions belong to the explicitly selected PG matrix.
// A missing URL in that matrix is an error, never a skipped integration test.
func postgresSchema(t testing.TB) string {
	t.Helper()
	switch os.Getenv("CORESCOPE_TEST_BACKEND") {
	case "", "sqlite":
		t.Skip("PostgreSQL-specific test; select CORESCOPE_TEST_BACKEND=postgres")
	case "postgres":
		if os.Getenv("CORESCOPE_TEST_POSTGRES_URL") == "" {
			t.Fatal("PostgreSQL test matrix requires CORESCOPE_TEST_POSTGRES_URL")
		}
		return pgtest.NewSchema(t)
	default:
		t.Fatal("CORESCOPE_TEST_BACKEND must be sqlite or postgres")
	}
	return ""
}
