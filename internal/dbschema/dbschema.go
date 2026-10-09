// Package dbschema owns telemetry schema entrypoints for SQLite and PostgreSQL.
// SQLite's writer applies migrations; PostgreSQL uses offline bootstrap. Readers
// only assert readiness. Apply and AssertReady retain PostgreSQL compatibility.
package dbschema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

type Logger func(string, ...interface{})

// SchemaSQL is inspectable native PostgreSQL DDL, shared with the importer.
//
//go:embed schema.sql
var SchemaSQL string

const Version = 1

// WriterLockKey serializes ID allocation through commit, including backfills,
// across ingestor processes. The server's ID cursor relies on this ordering.
const WriterLockKey int64 = 18950410699801933

type Querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func Apply(db *sql.DB, logf Logger) error { return apply(db, logf, true) }

// ApplyForImport atomically creates an unready target; runtime cannot see a
// temporary ready-empty schema while the importer starts its first batch.
func ApplyForImport(db *sql.DB, logf Logger) error { return apply(db, logf, false) }

// ApplyForImportTx lets the importer commit schema and source/progress metadata
// in one transaction. The caller owns commit/rollback; no ready window exists.
func ApplyForImportTx(tx *sql.Tx, _ Logger) error { return applyTx(tx, false) }
func apply(db *sql.DB, logf Logger, initialReady bool) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := applyTx(tx, initialReady); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if initialReady {
		if err := AssertReady(db); err != nil {
			return err
		}
	}
	if logf != nil {
		logf("[dbschema] PostgreSQL telemetry schema %d initialized", Version)
	}
	return nil
}
func applyTx(tx *sql.Tx, initialReady bool) error {
	var err error
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(current_schema(),$1))`, WriterLockKey); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS corescope_schema (version INTEGER NOT NULL, kind TEXT PRIMARY KEY, ready BOOLEAN NOT NULL)`); err != nil {
		return fmt.Errorf("create readiness marker: %w", err)
	}
	var version int
	var ready bool
	err = tx.QueryRow(`SELECT version,ready FROM corescope_schema WHERE kind='telemetry'`).Scan(&version, &ready)
	if err == nil {
		if version != Version {
			return fmt.Errorf("unsupported telemetry schema version %d (binary supports %d)", version, Version)
		}
		if !ready {
			return fmt.Errorf("telemetry import is incomplete; finish or restore the offline import")
		}
		if !initialReady {
			return fmt.Errorf("import target is already initialized")
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name <> 'corescope_schema'`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("refusing to initialize a non-empty unversioned schema")
	}
	if _, err = tx.Exec(SchemaSQL); err != nil {
		return fmt.Errorf("create telemetry schema: %w", err)
	}
	if _, err = tx.Exec(`INSERT INTO corescope_schema(version,kind,ready) VALUES($1,'telemetry',$2)`, Version, initialReady); err != nil {
		return err
	}

	return nil
}

func AssertReady(db *sql.DB) error {
	var version int
	var ready bool
	if err := db.QueryRow(`SELECT version,ready FROM corescope_schema WHERE kind='telemetry'`).Scan(&version, &ready); err != nil {
		return fmt.Errorf("telemetry schema is not initialized; run the offline migration command: %w", err)
	}
	if version != Version {
		return fmt.Errorf("unsupported telemetry schema version %d (binary supports %d)", version, Version)
	}
	if !ready {
		return fmt.Errorf("telemetry import is incomplete; finish or restore the offline import")
	}
	rows, err := db.Query(`SELECT table_name,column_name,data_type,is_identity FROM information_schema.columns WHERE table_schema=current_schema()`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := make(map[string]bool)
	types := make(map[string]string)
	identities := make(map[string]bool)
	for rows.Next() {
		var table, col, dataType, identity string
		if err := rows.Scan(&table, &col, &dataType, &identity); err != nil {
			return err
		}
		have[table+"."+col] = true
		types[table+"."+col] = strings.ToUpper(dataType)
		identities[table+"."+col] = identity == "YES"
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for _, table := range Tables {
		for _, col := range table.Columns {
			key := table.Name + "." + col.Name
			if !have[key] {
				missing = append(missing, key)
			} else if types[key] != col.Type || identities[key] != col.Identity {
				missing = append(missing, key+" (unexpected type or identity)")
			}
		}
	}
	if !have["packets_v.id"] {
		missing = append(missing, "packets_v")
	}
	indexRows, err := db.Query(`SELECT indexname FROM pg_indexes WHERE schemaname=current_schema()`)
	if err != nil {
		return err
	}
	indexes := make(map[string]bool)
	for indexRows.Next() {
		var name string
		if err := indexRows.Scan(&name); err != nil {
			indexRows.Close()
			return err
		}
		indexes[name] = true
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return err
	}
	indexRows.Close()
	for _, name := range Indexes {
		if !indexes[name] {
			missing = append(missing, "index:"+name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete telemetry schema: %s", strings.Join(missing, ", "))
	}
	return nil
}

func TableHasColumn(db Querier, table, column string) (bool, error) {
	var found bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1 AND column_name=$2)`, table, column).Scan(&found)
	return found, err
}

// AssertWriter refuses migration-owner credentials in the runtime process.
func AssertWriter(db *sql.DB) error {
	var elevated, createSchema, ownsSchema bool
	err := db.QueryRow(`SELECT r.rolsuper OR r.rolcreatedb OR r.rolcreaterole, has_schema_privilege(current_schema(),'CREATE') OR has_database_privilege(current_database(),'CREATE'), pg_has_role(current_user,n.nspowner,'MEMBER') OR EXISTS(SELECT 1 FROM pg_class c WHERE c.relnamespace=n.oid AND pg_has_role(current_user,c.relowner,'MEMBER')) OR has_table_privilege('corescope_schema','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER') FROM pg_roles r JOIN pg_namespace n ON n.nspname=current_schema() WHERE r.rolname=current_user`).Scan(&elevated, &createSchema, &ownsSchema)
	if err != nil {
		return err
	}
	if elevated || createSchema || ownsSchema {
		return fmt.Errorf("telemetry runtime requires a restricted writer role without schema ownership, CREATE, or administrative privileges")
	}
	for _, table := range Tables {
		var allowed bool
		if err := db.QueryRow(`SELECT has_table_privilege($1,'SELECT') AND has_table_privilege($1,'INSERT') AND has_table_privilege($1,'UPDATE') AND has_table_privilege($1,'DELETE')`, table.Name).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("telemetry writer lacks data privileges on %s", table.Name)
		}
		for _, column := range table.Columns {
			if !column.Identity {
				continue
			}
			if err := db.QueryRow(`SELECT has_sequence_privilege(pg_get_serial_sequence($1,$2),'USAGE')`, table.Name, column.Name).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("telemetry writer lacks sequence USAGE on %s.%s", table.Name, column.Name)
			}
		}
	}
	return nil
}

// ReseedIdentities preserves imported IDs and legacy deleted high-water marks.
// The caller runs this in its import transaction before marking ready.
func ReseedIdentities(tx *sql.Tx, highWater map[string]int64) error {
	for _, table := range Tables {
		for _, column := range table.Columns {
			if !column.Identity {
				continue
			}
			key := table.Name + "." + column.Name
			var high int64
			if err := tx.QueryRow(`SELECT COALESCE(MAX("` + column.Name + `"),0) FROM "` + table.Name + `"`).Scan(&high); err != nil {
				return err
			}
			if highWater[key] > high {
				high = highWater[key]
			}
			if high < 1 {
				high = 1
				if _, err := tx.Exec(`SELECT setval(pg_get_serial_sequence($1,$2),$3,false)`, table.Name, column.Name, high); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(`SELECT setval(pg_get_serial_sequence($1,$2),$3,true)`, table.Name, column.Name, high); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func SoftDeleteBlacklistedObservers(db *sql.DB, blacklist []string) (int64, error) {
	var slots []string
	var args []any
	for _, id := range blacklist {
		if id = strings.TrimSpace(id); id != "" {
			args = append(args, id)
			slots = append(slots, fmt.Sprintf("LOWER($%d)", len(args)))
		}
	}
	if len(args) == 0 {
		return 0, nil
	}
	res, err := db.Exec(`UPDATE observers SET inactive=1 WHERE LOWER(id) IN (`+strings.Join(slots, ",")+`) AND (inactive IS NULL OR inactive=0)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
