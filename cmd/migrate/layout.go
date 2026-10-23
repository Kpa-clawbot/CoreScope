package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/users"
)

type importColumn struct {
	Name     string
	Type     string
	Identity bool
}
type importTable struct {
	Name    string
	Columns []importColumn
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func layouts(kind string) ([]importTable, error) {
	var out []importTable
	if kind == "telemetry" {
		for _, table := range dbschema.Tables {
			t := importTable{Name: table.Name}
			for _, col := range table.Columns {
				t.Columns = append(t.Columns, importColumn{col.Name, col.Type, col.Identity})
			}
			out = append(out, t)
		}
		return out, nil
	}
	if kind != "accounts" {
		return nil, errors.New("import kind must be telemetry or accounts")
	}
	// SQLite is used only by this offline importer. Derive the legacy account
	// layout from the immutable migration metadata instead of duplicating it.
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, migration := range users.LegacyMigrations() {
		for _, stmt := range migration {
			if _, err := db.Exec(stmt); err != nil {
				return nil, err
			}
		}
	}
	identities := map[string]bool{"users": true, "sessions": true, "audit_log": true, "mail_log": true, "proposals": true, "mail_events": true}
	for _, name := range users.Tables() {
		t := importTable{Name: name}
		if name == "mail_events" {
			t.Columns = append(t.Columns, importColumn{"id", "BIGINT", true})
		}
		cols, err := sqliteColumns(db, name)
		if err != nil {
			return nil, err
		}
		for _, col := range cols {
			kind := "TEXT"
			if strings.Contains(strings.ToUpper(col.Type), "INT") {
				kind = "BIGINT"
			}
			t.Columns = append(t.Columns, importColumn{col.Name, kind, col.Name == "id" && identities[name]})
		}
		out = append(out, t)
	}
	return out, nil
}

func sqliteColumns(db *sql.DB, table string) ([]importColumn, error) {
	// table_info omits generated and hidden columns, which must be refused even
	// when their names match an ordinary column in a supported legacy layout.
	rows, err := db.Query(`PRAGMA table_xinfo(` + quote(table) + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []importColumn
	for rows.Next() {
		var cid, notnull, pk, hidden int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk, &hidden); err != nil {
			return nil, err
		}
		if hidden != 0 {
			return nil, fmt.Errorf("unsupported generated or hidden SQLite column %s.%s", table, name)
		}
		out = append(out, importColumn{Name: name, Type: kind})
	}
	return out, rows.Err()
}

func validateSource(db *sql.DB, kind string, tables []importTable) error {
	derived := map[string]bool{}
	if kind == "telemetry" {
		objects, err := knownSQLiteHopIndex(db)
		if err != nil {
			return err
		}
		for _, object := range objects {
			derived[object.Name] = true
		}
	}
	var accountVersion int
	if kind == "accounts" {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(version),0) FROM schema_version`).Scan(&n, &accountVersion); err != nil {
			return errors.New("account source must contain its supported schema_version")
		}
		if n != 1 || accountVersion < 0 || accountVersion > users.SQLiteSchemaVersion {
			return fmt.Errorf("unsupported account schema version %d (supported 0 through %d)", accountVersion, users.SQLiteSchemaVersion)
		}
	}
	allowed := map[string]map[string]bool{}
	for _, table := range tables {
		cols := map[string]bool{}
		for _, col := range table.Columns {
			cols[col.Name] = true
		}
		allowed[table.Name] = cols
	}
	if kind == "accounts" {
		allowed["schema_version"] = map[string]bool{"version": true}
	}
	rows, err := db.Query(`SELECT name,type,COALESCE(sql,'') FROM sqlite_master WHERE type IN ('table','view','trigger')`)
	if err != nil {
		return err
	}
	type object struct{ name, kind, sql string }
	internalTables := map[string]bool{"sqlite_sequence": true, "sqlite_stat1": true, "sqlite_stat2": true, "sqlite_stat3": true, "sqlite_stat4": true}
	var objects []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.name, &o.kind, &o.sql); err != nil {
			rows.Close()
			return err
		}
		ordinary := strings.HasPrefix(strings.ToUpper(strings.Join(strings.Fields(o.sql), " ")), "CREATE TABLE ")
		if o.kind == "table" && internalTables[o.name] && ordinary {
			continue
		}
		if derived[o.name] {
			continue // Complete definitions were checked above, not names alone.
		}
		objects = append(objects, o)
		if len(objects) > len(allowed)+1 {
			rows.Close()
			return errors.New("source has unexpected schema objects")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, o := range objects {
		if o.kind == "table" && !strings.HasPrefix(strings.ToUpper(strings.Join(strings.Fields(o.sql), " ")), "CREATE TABLE ") {
			return fmt.Errorf("unsupported nonordinary source table %q", o.name)
		}
		if o.kind == "view" && kind == "telemetry" && o.name == "packets_v" {
			continue
		}
		cols, known := allowed[o.name]
		if o.kind != "table" || !known {
			return fmt.Errorf("unsupported SQLite %s %q; no source data was discarded", o.kind, o.name)
		}
		have, err := sqliteColumns(db, o.name)
		if err != nil {
			return err
		}
		for _, col := range have {
			if !cols[col.Name] {
				if kind == "telemetry" && o.name == "observations" && col.Name == "observer_id" {
					return errors.New("legacy v2 observer_id layout requires an offline upgrade with the last SQLite release before importing")
				}
				return fmt.Errorf("unsupported SQLite column %s.%s", o.name, col.Name)
			}
		}
	}
	if kind == "telemetry" {
		return legacy.CheckLegacySource(db)
	}
	if err := users.ValidateSQLiteSourceLayout(db); err != nil {
		return err
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("account source failed SQLite integrity check")
	}
	return nil
}

func normalizeSource(db *sql.DB, kind, rawPath string, tables []importTable) error {
	if err := validateSource(db, kind, tables); err != nil {
		return err
	}
	if kind == "accounts" {
		// Only the private snapshot is normalized; exact known old/native v6
		// layouts upgrade through the same lossless writer migration as runtime.
		return users.ApplySQLite(db)
	}

	// This is the private normalized snapshot. Keep every source/recovery
	// object intact, but remove the validated derived cache before canonical
	// normalization so its SQLite JSON triggers cannot execute during import.
	if _, err := dbschema.DropObservationHopsSchema(db); err != nil {
		return err
	}
	if err := legacy.Normalize(db, nil); err != nil {
		return err
	}
	// Mutation inventory of legacy.Normalize: only these columns can change
	// existing rows. Duplicate/invalid-row deletion paths were refused above.
	// Restore changed tuples by rowid, without rewriting the observation corpus.
	recoveryURI, err := sqliteURL(rawPath, "ro")
	if err != nil {
		return err
	}
	if _, err := db.Exec(`ATTACH DATABASE ? AS recovery`, recoveryURI); err != nil {
		return err
	}
	defer db.Exec(`DETACH DATABASE recovery`)
	mutated := map[string]map[string]bool{
		"nodes":         {"public_key": true, "advert_count": true},
		"observers":     {"noise_floor": true, "last_packet_at": true},
		"transmissions": {"channel_hash": true},
	}
	for _, table := range tables {
		selected, changes := mutated[table.Name]
		if !changes {
			continue
		}
		var exists int
		if err := db.QueryRow(`SELECT count(*) FROM recovery.sqlite_master WHERE type='table' AND name=?`, table.Name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		rows, err := db.Query(`PRAGMA recovery.table_info(` + quote(table.Name) + `)`)
		if err != nil {
			return err
		}
		var columns []string
		for rows.Next() {
			var cid, nn, pk int
			var name, kind string
			var def sql.NullString
			if err := rows.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			if selected[name] {
				columns = append(columns, name)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		var sourceCount, targetCount int64
		if err := db.QueryRow(`SELECT count(*) FROM recovery.` + quote(table.Name)).Scan(&sourceCount); err != nil {
			return err
		}
		if err := db.QueryRow(`SELECT count(*) FROM main.` + quote(table.Name)).Scan(&targetCount); err != nil {
			return err
		}
		if sourceCount != targetCount {
			return fmt.Errorf("normalization changed row count in %s; source requires an explicit repair", table.Name)
		}
		if len(columns) == 0 {
			continue
		}
		var targets, originals, differences []string
		for _, name := range columns {
			targets = append(targets, quote(name))
			originals = append(originals, "old."+quote(name))
			differences = append(differences, "current."+quote(name)+" IS NOT old."+quote(name))
		}
		restore := `UPDATE main.` + quote(table.Name) + ` AS current SET (` + strings.Join(targets, ",") + `)=(SELECT ` + strings.Join(originals, ",") + ` FROM recovery.` + quote(table.Name) + ` old WHERE old.rowid=current.rowid)
  WHERE EXISTS(SELECT 1 FROM recovery.` + quote(table.Name) + ` old WHERE old.rowid=current.rowid AND (` + strings.Join(differences, " OR ") + `))`
		if _, err := db.Exec(restore); err != nil {
			return fmt.Errorf("preserve original %s values: %w", table.Name, err)
		}
	}
	return nil
}
