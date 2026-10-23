package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Frozen SQLite layout installed by #2214. Do not regenerate this allowlist
// when runtime DDL changes: a new supported layout needs a separate version
// and migration tests. Exact SQL preserves literals, column types, NOT NULL,
// CHECK, primary-key order, WITHOUT ROWID, and trigger bodies.
//
//go:embed sqlite_hop_index_v1.json
var sqliteHopIndexV1JSON []byte

type sqliteDerivedObject struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Table string `json:"table"`
	SQL   string `json:"sql"`
}

type derivedTableReport struct {
	Table         string `json:"table"`
	SchemaVersion int    `json:"schema_version"`
	Reason        string `json:"reason"`
}

// knownSQLiteHopIndex recognizes the complete schema even when its data
// backfill is partial. No derived content is trusted or promoted to canonical
// data. Unknown indexes on either table also fail this exact-object comparison;
// v1 has only the implicit WITHOUT ROWID primary key, no standalone indexes.
func knownSQLiteHopIndex(db *sql.DB) ([]sqliteDerivedObject, error) {
	var known struct {
		Version int                   `json:"version"`
		Objects []sqliteDerivedObject `json:"objects"`
	}
	if err := json.Unmarshal(sqliteHopIndexV1JSON, &known); err != nil || known.Version != 1 || len(known.Objects) != 5 {
		return nil, errors.New("invalid built-in SQLite hop-index layout")
	}
	rows, err := db.Query(`SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema
		WHERE name LIKE 'observation_hops%' OR name LIKE 'trg_observation_hops%'
		   OR tbl_name IN ('observation_hops','observation_hops_coverage')
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []sqliteDerivedObject
	for rows.Next() {
		var o sqliteDerivedObject
		if err := rows.Scan(&o.Type, &o.Name, &o.Table, &o.SQL); err != nil {
			return nil, err
		}
		got = append(got, o)
		if len(got) > len(known.Objects) {
			return nil, errors.New("unsupported SQLite hop-index objects; source data was not discarded")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(got) > 0 && !slices.Equal(got, known.Objects) {
		return nil, errors.New("unsupported SQLite hop-index layout (expected complete v1 tables, constraints, indexes and triggers); source data was not discarded")
	}
	return got, nil
}

func sqliteDerivedExclusions(db *sql.DB, kind string) ([]derivedTableReport, error) {
	if kind != "telemetry" {
		return nil, nil
	}
	objects, err := knownSQLiteHopIndex(db)
	if err != nil {
		return nil, err
	}
	var reports []derivedTableReport
	for _, o := range objects {
		if o.Type == "table" {
			reports = append(reports, derivedTableReport{
				Table: o.Name, SchemaVersion: 1,
				Reason: "derived SQLite hop-index rows and coverage excluded; rebuild through the SQLite ingestor",
			})
		}
	}
	return reports, nil
}

func recoveryDerivedExclusions(rawPath, kind string) ([]derivedTableReport, error) {
	db, err := openSQLite(rawPath, "ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA trusted_schema=OFF`); err != nil {
		return nil, err
	}
	reports, err := sqliteDerivedExclusions(db, kind)
	if err != nil {
		return nil, fmt.Errorf("validate recovery derived schema: %w", err)
	}
	return reports, nil
}
