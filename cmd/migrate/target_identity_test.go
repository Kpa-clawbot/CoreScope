package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestImportIdentityUsesConfiguredEndpoint(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parse test URL")
	}
	// Connect through the stable name instead of its transient resolved address.
	u.Host = "localhost:" + u.Port()
	o := importOptions{Source: accountSource(t, 5), DatabaseURL: u.String(), StateDir: t.TempDir(), Kind: "accounts"}
	if _, err := importSQLite(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(o.StateDir, o.Kind, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest sourceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	db := openImportDB(t, o.DatabaseURL)
	var database, schema string
	var databaseOID, schemaOID int64
	if err := db.QueryRow(`SELECT current_database(),current_schema(),(SELECT oid::bigint FROM pg_database WHERE datname=current_database()),(SELECT oid::bigint FROM pg_namespace WHERE nspname=current_schema())`).Scan(&database, &schema, &databaseOID, &schemaOID); err != nil {
		t.Fatal(err)
	}
	config, err := pgutil.ParseConfig(o.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := json.Marshal([]any{database, schema, databaseOID, schemaOID, config.Host, config.Port})
	want := sha256.Sum256(identity)
	if manifest.TargetDigest != hex.EncodeToString(want[:]) {
		t.Fatal("manifest identity used a transient resolved address instead of the configured endpoint")
	}
	o.Resume = true
	if _, err := importSQLite(context.Background(), o); err != nil {
		t.Fatalf("same configured endpoint did not resume: %v", err)
	}
	// The IP alias reaches the same database/OIDs but is a different configured
	// endpoint. Operators must retain their original endpoint when resuming.
	u.Host = "127.0.0.1:" + u.Port()
	o.DatabaseURL = u.String()
	if _, err := importSQLite(context.Background(), o); err == nil {
		t.Fatal("changed configured endpoint accepted")
	}
}
