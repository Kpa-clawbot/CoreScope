package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

// A recorded installation may only upgrade its known existing schemas. Unlike
// bootstrap, this path never initializes an empty replacement or adds accounts.
// Each database upgrade is atomic; across databases an interrupted run is
// retried while services remain stopped, with the selection bytes unchanged.
func upgradeRecordedPostgres(ctx context.Context, o storageOptions, expected dbconfig.Selection, raw dbconfig.StorageInputs) error {
	selected, lease, err := dbconfig.OpenSelectionForMaintenance(o.SelectionFile)
	if err != nil {
		return err
	}
	defer lease.Close()
	if selected.Generation != expected.Generation || selected.Backend != dbconfig.Postgres {
		return dbconfig.ErrSelectionChanged
	}
	if o.OwnerURL == "" || (selected.Accounts != nil && o.UsersOwnerURL == "") {
		return errors.New("recorded PostgreSQL upgrade requires owner credentials for every initialized store, including disabled accounts")
	}
	// Check the supplied endpoints before ApplyTo pins selected schema defaults;
	// a credential URL naming a different schema must not authorize its upgrade.
	if err := validateInitialOwnerTargets(ctx, selected, dbconfig.Storage{WriterDatabaseURL: o.OwnerURL, UsersDatabaseURL: o.UsersOwnerURL}); err != nil {
		return err
	}
	owners, err := bindOwnerURLs(selected, o.OwnerURL, o.UsersOwnerURL)
	if err != nil {
		return err
	}
	type storeUpgrade struct {
		kind   string
		db     *sql.DB
		needed bool
	}
	var plans []storeUpgrade
	defer func() {
		for _, plan := range plans {
			plan.db.Close()
		}
	}()
	stores := []struct{ kind, url string }{{"telemetry", owners.WriterDatabaseURL}}
	if selected.Accounts != nil {
		stores = append(stores, struct{ kind, url string }{"accounts", owners.UsersDatabaseURL})
	}
	// Finish all owner, readiness, version, and physical-layout preflights
	// before mutating either store. Unknown extensions are never discarded.
	for _, store := range stores {
		db, err := pgutil.Open(store.url, false)
		if err != nil {
			return err
		}
		plan := storeUpgrade{kind: store.kind, db: db}
		plans = append(plans, plan)
		needed, err := validateRecordedPostgresUpgrade(ctx, db, store.kind)
		if err != nil {
			return err
		}
		plans[len(plans)-1].needed = needed
	}
	for _, plan := range plans {
		if !plan.needed {
			continue
		}
		if plan.kind == "telemetry" {
			err = dbschema.ApplyPostgres(plan.db, nil)
		} else {
			err = users.ApplyPostgres(plan.db)
		}
		if err != nil {
			return err
		}
	}
	if err := grantRuntimeTargets(ctx, selected, owners, raw); err != nil {
		return err
	}
	return validateRuntimeSelection(ctx, selected, raw)
}

func validateRecordedPostgresUpgrade(ctx context.Context, db *sql.DB, kind string) (bool, error) {
	var owner bool
	if err := db.QueryRowContext(ctx, `SELECT pg_has_role(current_user,n.nspowner,'MEMBER')
		AND NOT EXISTS(SELECT 1 FROM pg_class c WHERE c.relnamespace=n.oid AND c.relkind IN ('r','p','v','m','S','f') AND NOT pg_has_role(current_user,c.relowner,'MEMBER'))
		FROM pg_namespace n WHERE n.nspname=current_schema()`).Scan(&owner); err != nil || !owner {
		return false, errors.New("recorded PostgreSQL upgrade requires the selected schema and object owner credentials")
	}
	var count, version int
	var recordedKind string
	var ready bool
	if err := db.QueryRowContext(ctx, `SELECT count(*),min(kind),min(version),bool_and(ready) FROM corescope_schema`).Scan(&count, &recordedKind, &version, &ready); err != nil || count != 1 || recordedKind != kind || !ready {
		return false, errors.New("recorded PostgreSQL store is missing, unsupported or incomplete; it was not initialized")
	}
	previous, current := 1, dbschema.Version
	if kind == "accounts" {
		if version != users.PostgresSchemaVersion {
			return false, errors.New("unsupported PostgreSQL account readiness version")
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*),coalesce(max(version),0) FROM schema_version`).Scan(&count, &version); err != nil || count != 1 {
			return false, errors.New("recorded PostgreSQL account schema is incomplete")
		}
		previous, current = 5, users.CurrentSchemaVersion
	}
	if version != previous && version != current {
		return false, errors.New("unsupported PostgreSQL schema version; upgrade was not attempted")
	}
	var dropped bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid WHERE c.relnamespace=current_schema()::regnamespace AND a.attisdropped)`).Scan(&dropped); err != nil || dropped {
		return false, errors.New("unsupported PostgreSQL dropped-column layout; upgrade was not attempted")
	}
	tables, err := layouts(kind)
	if err != nil {
		return false, err
	}
	if version == current {
		return false, validatePostgresLayout(db, kind, tables)
	}
	// The historical layouts differ only by these exact additive objects.
	// All other columns, constraints, indexes, views and policies must match
	// the canonical manifest, including user extensions that would be unsafe.
	oldTables := make([]importTable, 0, len(tables))
	for _, table := range tables {
		if !upgradeAddedRelation(kind, table.Name) {
			oldTables = append(oldTables, table)
		}
	}
	got, err := postgresLayout(db, kind, oldTables)
	if err != nil {
		return false, err
	}
	var canonical map[string][]string
	if err := json.Unmarshal(postgresLayoutJSON, &canonical); err != nil {
		return false, err
	}
	var want []string
	for _, entry := range canonical[kind] {
		var parts []any
		if err := json.Unmarshal([]byte(entry), &parts); err != nil {
			return false, err
		}
		if len(parts) < 2 {
			return false, errors.New("invalid canonical PostgreSQL layout")
		}
		relation, _ := parts[1].(string)
		if upgradeAddedRelation(kind, relation) {
			continue
		}
		if kind == "accounts" && relation == "sessions" {
			if parts[0] == "column" && len(parts) > 3 && (parts[3] == "kind" || parts[3] == "label" || parts[3] == "scopes") {
				continue
			}
			if parts[0] == "constraint" && len(parts) > 2 {
				switch parts[2] {
				case "sessions_kind_check", "sessions_kind_not_null", "sessions_label_not_null", "sessions_scopes_not_null":
					continue
				}
			}
		}
		want = append(want, entry)
	}
	if !slices.Equal(got, want) {
		return false, errors.New("unsupported PostgreSQL upgrade layout; source data and selection were not changed")
	}
	return true, nil
}

func upgradeAddedRelation(kind, name string) bool {
	if kind == "telemetry" {
		return name == "tx_rewrite_feed" || name == "key_backfill_state" || name == "key_backfill_done"
	}
	return name == "companion_links" || name == "link_challenges" || name == "linked_companions"
}
