package main

import (
	"context"
	"errors"
	"strings"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
)

// Same narrow policy as docker/postgres-grants.sql, applied to the verified
// unselected destination before runtime validation and the atomic selection
// commit. Runtime role names come from their explicit credential URLs; no
// password or connection URL enters a SQL statement or child process argv.
func grantRuntimeTargets(ctx context.Context, selected dbconfig.Selection, owners dbconfig.Storage, raw dbconfig.StorageInputs) error {
	bound, err := selected.ApplyTo(raw)
	if err != nil {
		return err
	}
	runtime, err := dbconfig.ResolveStorage(bound)
	if err != nil {
		return err
	}
	role := func(dsn string) (string, error) {
		if dsn == "" {
			return "", errors.New("prepare the runtime role credentials before backend selection")
		}
		config, err := pgutil.ParseConfig(dsn)
		if err != nil {
			return "", err
		}
		return quote(config.User), nil
	}
	reader, err := role(runtime.ReaderDatabaseURL)
	if err != nil {
		return err
	}
	writer, err := role(runtime.WriterDatabaseURL)
	if err != nil {
		return err
	}
	if reader == writer {
		return errors.New("PostgreSQL telemetry reader and writer must use separate restricted roles")
	}
	type grants struct {
		kind, owner, schema string
		roles               []string
		queries             []string
	}
	telemetry := grants{kind: "telemetry", owner: owners.WriterDatabaseURL, schema: quote(selected.Telemetry.Postgres.Schema), roles: []string{reader, writer}}
	telemetry.queries = []string{
		`GRANT SELECT ON ALL TABLES IN SCHEMA ` + telemetry.schema + ` TO ` + reader,
		`GRANT SELECT ON ALL SEQUENCES IN SCHEMA ` + telemetry.schema + ` TO ` + reader,
		`GRANT SELECT ON corescope_schema,packets_v TO ` + writer,
		`GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA ` + telemetry.schema + ` TO ` + writer,
	}
	tables, err := layouts("telemetry")
	if err != nil {
		return err
	}
	for _, table := range tables {
		telemetry.queries = append(telemetry.queries, `GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE `+quote(table.Name)+` TO `+writer)
	}
	stores := []grants{telemetry}
	if selected.Accounts != nil {
		account, err := role(runtime.UsersDatabaseURL)
		if err != nil {
			return err
		}
		entry := grants{kind: "accounts", owner: owners.UsersDatabaseURL, schema: quote(selected.Accounts.Postgres.Schema), roles: []string{account}}
		entry.queries = []string{`GRANT SELECT ON ALL TABLES IN SCHEMA ` + entry.schema + ` TO ` + account, `GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA ` + entry.schema + ` TO ` + account}
		tables, err := layouts("accounts")
		if err != nil {
			return err
		}
		for _, table := range tables {
			entry.queries = append(entry.queries, `GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE `+quote(table.Name)+` TO `+account)
		}
		if runtime.ApprovedChannelsDatabaseURL != "" {
			channel, err := role(runtime.ApprovedChannelsDatabaseURL)
			if err != nil {
				return err
			}
			if channel == account {
				return errors.New("account writer and channel projection reader must use separate restricted roles")
			}
			entry.roles = append(entry.roles, channel)
			entry.queries = append(entry.queries, `GRANT SELECT ON approved_channels,corescope_schema TO `+channel)
		}
		stores = append(stores, entry)
	}
	for _, store := range stores {
		if store.owner == "" {
			continue
		} // Account-only initialization leaves telemetry grants unchanged.
		db, err := pgutil.Open(store.owner, false)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			db.Close()
			return err
		}
		queries := append([]string{`REVOKE CREATE ON SCHEMA ` + store.schema + ` FROM PUBLIC`, `GRANT USAGE ON SCHEMA ` + store.schema + ` TO ` + strings.Join(store.roles, ",")}, store.queries...)
		for _, query := range queries {
			if _, err = tx.ExecContext(ctx, query); err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		db.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
