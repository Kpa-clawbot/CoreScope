package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
)

// Captured from the versioned native ApplyPostgres entrypoints. The regression
// recreates both canonical schemas and compares this manifest, so a physical
// schema change requires an explicit converter compatibility decision.
//
//go:embed postgres_layout.json
var postgresLayoutJSON []byte

func postgresLayout(db *sql.DB, kind string, tables []importTable) ([]string, error) {
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	allowed := map[string]bool{"corescope_schema": true, "corescope_import": true, "corescope_import_progress": true}
	if kind == "accounts" {
		allowed["schema_version"] = true
	}
	var tableNames []string
	for _, table := range tables {
		tableNames = append(tableNames, `'`+strings.ReplaceAll(table.Name, `'`, `''`)+`'`)
		allowed[table.Name] = true
	}
	view := "packets_v"
	if kind == "accounts" {
		view = "approved_channels"
	}
	allowed[view] = true
	relationRows, err := db.Query(`SELECT c.relname,c.relkind::text FROM pg_class c WHERE c.relnamespace=current_schema()::regnamespace AND c.relkind IN ('r','p','v','m','S','f') ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	var relations [][2]string
	for relationRows.Next() {
		var pair [2]string
		if err := relationRows.Scan(&pair[0], &pair[1]); err != nil {
			relationRows.Close()
			return nil, err
		}
		relations = append(relations, pair)
	}
	err = relationRows.Err()
	relationRows.Close()
	if err != nil {
		return nil, err
	}
	for _, table := range tables {
		for _, col := range table.Columns {
			if !col.Identity {
				continue
			}
			var name string
			if err := db.QueryRow(`SELECT c.relname FROM pg_class c WHERE c.oid=pg_get_serial_sequence($1,$2)::regclass`, table.Name, col.Name).Scan(&name); err != nil {
				return nil, err
			}
			allowed[name] = true
		}
	}
	for _, pair := range relations {
		if !allowed[pair[0]] {
			return nil, errors.New("unsupported PostgreSQL source object; no source data was discarded")
		}
	}
	names := strings.Join(tableNames, ",")
	queries := []string{
		`SELECT json_build_array('column',table_name,ordinal_position,column_name,data_type,is_nullable,column_default,collation_name,is_identity,identity_generation,is_generated,generation_expression)::text FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN (` + names + `)`,
		`SELECT json_build_array('table',c.relname,c.relkind,c.relrowsecurity,c.relforcerowsecurity)::text FROM pg_class c WHERE c.relnamespace=current_schema()::regnamespace AND c.relname IN (` + names + `)`,
		`SELECT json_build_array('constraint',c.relname,k.conname,k.convalidated,pg_get_constraintdef(k.oid))::text FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid WHERE c.relnamespace=current_schema()::regnamespace AND c.relname IN (` + names + `)`,
		`SELECT json_build_array('index',c.relname,pg_get_indexdef(i.indexrelid))::text FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid WHERE c.relnamespace=current_schema()::regnamespace AND c.relname IN (` + names + `)`,
		`SELECT json_build_array('trigger',c.relname,pg_get_triggerdef(t.oid))::text FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid WHERE NOT t.tgisinternal AND c.relnamespace=current_schema()::regnamespace AND c.relname IN (` + names + `)`,
		`SELECT json_build_array('policy',c.relname,p.polname,pg_get_expr(p.polqual,p.polrelid),pg_get_expr(p.polwithcheck,p.polrelid))::text FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid WHERE c.relnamespace=current_schema()::regnamespace AND c.relname IN (` + names + `)`,
		`SELECT json_build_array('view',c.relname,pg_get_viewdef(c.oid))::text FROM pg_class c WHERE c.relnamespace=current_schema()::regnamespace AND c.relkind='v' AND c.relname='` + view + `'`,
	}
	var out []string
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			// Only catalog qualification varies between installations. User data is
			// never normalized, decoded or rewritten by these schema comparisons.
			value = strings.ReplaceAll(value, strings.ReplaceAll(quote(schema), `"`, `\"`)+".", "{schema}.")
			value = strings.ReplaceAll(value, schema+".", "{schema}.")
			out = append(out, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func validatePostgresLayout(db *sql.DB, kind string, tables []importTable) error {
	var expected map[string][]string
	if err := json.Unmarshal(postgresLayoutJSON, &expected); err != nil {
		return errors.New("invalid compiled PostgreSQL conversion layout")
	}
	got, err := postgresLayout(db, kind, tables)
	if err != nil {
		return err
	}
	if !slices.Equal(got, expected[kind]) {
		return errors.New("unsupported PostgreSQL columns, constraints, indexes, policies or views; conversion refused without changing the source")
	}
	return nil
}
