package dbschema

import (
	"testing"

	"github.com/meshcore-analyzer/pgutil"
)

func TestTelemetryTextUsesBinaryCollation(t *testing.T) {
	db, err := pgutil.Open(postgresSchema(t), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Apply(db, t.Logf); err != nil {
		t.Fatal(err)
	}
	var inherited int
	err = db.QueryRow(`SELECT COUNT(*) FROM pg_attribute a
 JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=current_schema() AND c.relkind='r' AND c.relname<>'corescope_schema'
 AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='text'::regtype
 AND a.attcollation<>'"C"'::regcollation`).Scan(&inherited)
	if err != nil {
		t.Fatal(err)
	}
	if inherited != 0 {
		t.Fatalf("%d telemetry text columns inherit host-specific collation instead of SQLite byte order", inherited)
	}
}
