package main

import (
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/meshcore-analyzer/dbconfig"
)

type storageFlags struct {
	Backend                       dbconfig.Backend
	DBPath, DatabaseURL, StateDir string
}

// storageInputs gathers bootstrap values without guessing an installation's
// state. The shared persisted selector applies before ResolveStorage runs.
func (c *Config) storageInputs(baseDir string, flags storageFlags, getenv func(string) string) (dbconfig.StorageInputs, error) {
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return dbconfig.StorageInputs{}, errors.New("resolve storage base directory")
	}
	in := dbconfig.StorageInputs{BaseDir: base, DBPath: c.DBPath, StateDir: envOrValue(getenv, "CORESCOPE_STATE_DIR", c.StateDir),
		EnvBackend:        dbconfig.Backend(getenv("CORESCOPE_DB_BACKEND")),
		DatabaseURL:       envOrValue(getenv, "CORESCOPE_DATABASE_URL", c.DatabaseURL),
		ReaderDatabaseURL: getenv("CORESCOPE_READER_DATABASE_URL"), WriterDatabaseURL: getenv("CORESCOPE_WRITER_DATABASE_URL"),
		UsersDatabaseURL: getenv("CORESCOPE_USERS_DATABASE_URL"), ApprovedChannelsDatabaseURL: getenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL")}
	if in.DBPath == "" {
		in.DBPath = getenv("DB_PATH")
	}
	if c.DB != nil {
		in.Backend = c.DB.Backend
	}
	if c.UserManagement != nil {
		in.UsersDBPath = strings.TrimSpace(c.UserManagement.DBPath)
		in.UsersDatabaseURL = envOrValue(getenv, "CORESCOPE_USERS_DATABASE_URL", c.UserManagement.DatabaseURL)
	}
	if flags.Backend != "" {
		in.Backend = flags.Backend
	}
	if flags.DBPath != "" {
		in.DBPath = flags.DBPath
	}
	if flags.DatabaseURL != "" {
		in.DatabaseURL = flags.DatabaseURL
		in.ReaderDatabaseURL = flags.DatabaseURL
	}
	if flags.StateDir != "" {
		in.StateDir = flags.StateDir
	}
	// Legacy explicit paths are relative to the working directory, whereas the
	// server's omitted telemetry path defaults relative to config-dir.
	for _, p := range []*string{&in.DBPath, &in.UsersDBPath} {
		if *p == "" || strings.Contains(*p, "://") {
			continue
		}
		if filepath.VolumeName(*p) != "" && !filepath.IsAbs(*p) {
			return in, errors.New("drive-relative storage paths are ambiguous")
		}
		*p, err = filepath.Abs(*p)
		if err != nil {
			return in, errors.New("resolve SQLite storage path")
		}
	}
	return in, nil
}

// Discovery uses the stable legacy anchor, including after a backend switch.
func storageSelectionPath(raw dbconfig.StorageInputs) (string, error) {
	state := raw.StateDir
	if state == "" {
		if strings.Contains(raw.DBPath, "://") || strings.HasPrefix(raw.DBPath, "postgres:") || strings.HasPrefix(raw.DBPath, "postgresql:") {
			return "", errors.New("a database URL cannot be used as the legacy state directory anchor")
		}
		path := raw.DBPath
		if path == "" {
			path = filepath.Join(raw.BaseDir, "data", "meshcore.db")
		}
		state = filepath.Dir(path)
	}
	if strings.Contains(state, "://") || strings.ContainsRune(state, 0) {
		return "", errors.New("invalid storage state directory")
	}
	if !filepath.IsAbs(state) {
		state = filepath.Join(raw.BaseDir, state)
	}
	return dbconfig.SelectionPath(filepath.Clean(state)), nil
}
func resolveRuntimeStorage(raw dbconfig.StorageInputs) (dbconfig.Storage, io.Closer, error) {
	path, err := storageSelectionPath(raw)
	if err != nil {
		return dbconfig.Storage{}, nil, err
	}
	selected, lease, err := dbconfig.OpenSelection(path)
	if err != nil {
		return dbconfig.Storage{}, nil, err
	}
	fail := func(err error) (dbconfig.Storage, io.Closer, error) {
		lease.Close()
		return dbconfig.Storage{}, nil, err
	}
	raw, err = selected.ApplyTo(raw)
	if err != nil {
		return fail(err)
	}
	storage, err := dbconfig.ResolveStorage(raw)
	if err != nil {
		return fail(err)
	}
	return storage, lease, nil
}
