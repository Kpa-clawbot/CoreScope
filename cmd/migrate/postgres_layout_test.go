package main

import (
	"encoding/json"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/users"
	"os"
	"testing"
)

func TestPostgresConversionLayoutMatchesNativeSchema(t *testing.T) {
	for _, kind := range []string{"telemetry", "accounts"} {
		t.Run(kind, func(t *testing.T) {
			db := openImportDB(t, postgresSchema(t))
			var err error
			if kind == "telemetry" {
				err = dbschema.ApplyPostgres(db, nil)
			} else {
				err = users.ApplyPostgres(db)
			}
			if err != nil {
				t.Fatal(err)
			}
			tables, err := layouts(kind)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePostgresLayout(db, kind, tables); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE INDEX unexpected_extension ON ` + quote(tables[0].Name) + ` (` + quote(tables[0].Columns[0].Name) + `)`); err != nil {
				t.Fatal(err)
			}
			if err := validatePostgresLayout(db, kind, tables); err == nil {
				t.Fatal("custom source extension would be discarded")
			}
		})
	}
}

// This explicit developer mode exports a prospective layout for review. It
// never changes the compiled manifest or skips its normal compatibility test.
func TestExportPostgresConversionLayout(t *testing.T) {
	path := os.Getenv("CORESCOPE_EXPORT_POSTGRES_LAYOUT")
	if path == "" {
		t.Skip("explicit private layout export only")
	}
	output := map[string][]string{}
	for _, kind := range []string{"telemetry", "accounts"} {
		db := openImportDB(t, postgresSchema(t))
		var err error
		if kind == "telemetry" {
			err = dbschema.ApplyPostgres(db, nil)
		} else {
			err = users.ApplyPostgres(db)
		}
		if err != nil {
			t.Fatal(err)
		}
		tables, err := layouts(kind)
		if err != nil {
			t.Fatal(err)
		}
		if output[kind], err = postgresLayout(db, kind, tables); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
