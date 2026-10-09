package dbconfig

import (
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Backend string

const (
	SQLite   Backend = "sqlite"
	Postgres Backend = "postgres"
)

// Parameter emits a numbered positional placeholder. SQLite drivers differ in
// how they bind dollar names; question-mark numbers work positionally in both.
// Construct queries once when preparing; this is not a SQL-text rewriting API.
// Invalid backend/position values are programming errors.
func (b Backend) Parameter(position int) string {
	if position < 1 {
		panic("database parameter position must be positive")
	}
	switch b {
	case SQLite:
		return "?" + strconv.Itoa(position)
	case Postgres:
		return "$" + strconv.Itoa(position)
	default:
		panic("database parameter requires a validated backend")
	}
}

// StorageInputs contains raw configured values, before defaults are inserted.
// The caller supplies installation facts; resolution does no filesystem,
// environment or database reads. ExistingBackend is the recorded choice,
// and FreshInstall must not be inferred from a failed database connection.
type StorageInputs struct {
	Backend, EnvBackend, ExistingBackend              Backend
	FreshInstall                                      bool
	BaseDir, StateDir                                 string
	DBPath, UsersDBPath                               string
	DatabaseURL, ReaderDatabaseURL, WriterDatabaseURL string
	UsersDatabaseURL, ApprovedChannelsDatabaseURL     string
}

// Storage contains only the selected engine's targets. Role URLs can be empty
// when that service/optional feature is not configured; the caller opening a
// connection must require its role's URL. Never log this value: URLs may carry
// credentials. Runtime openers retain their engine-specific validation and
// read-only/role checks, including actual telemetry/account identity checks.
type Storage struct {
	Backend                                       Backend
	StateDir                                      string
	DBPath, UsersDBPath                           string
	ReaderDatabaseURL, WriterDatabaseURL          string
	UsersDatabaseURL, ApprovedChannelsDatabaseURL string
}

func ResolveStorage(in StorageInputs) (Storage, error) {
	var out Storage
	for _, backend := range []Backend{in.Backend, in.EnvBackend, in.ExistingBackend} {
		if backend != "" && backend != SQLite && backend != Postgres {
			return out, errors.New("unsupported database backend; choose sqlite or postgres")
		}
	}
	selected := in.Backend
	if in.EnvBackend != "" {
		if selected != "" && selected != in.EnvBackend {
			return out, errors.New("database backend conflicts between configuration and environment")
		}
		selected = in.EnvBackend
	}
	if in.ExistingBackend != "" {
		if selected != "" && selected != in.ExistingBackend {
			return out, errors.New("changing an existing database backend requires verified offline conversion")
		}
		selected = in.ExistingBackend
	}
	if selected == "" {
		hasSQLite := in.DBPath != "" || in.UsersDBPath != ""
		hasPostgres := in.DatabaseURL != "" || in.ReaderDatabaseURL != "" || in.WriterDatabaseURL != "" || in.UsersDatabaseURL != "" || in.ApprovedChannelsDatabaseURL != ""
		switch {
		case hasSQLite && hasPostgres:
			return out, errors.New("ambiguous legacy database settings; record the existing backend explicitly")
		case hasPostgres:
			selected = Postgres
		case hasSQLite || in.FreshInstall:
			selected = SQLite
		default:
			return out, errors.New("existing database backend is unknown; supply the recorded choice or unambiguous legacy settings")
		}
	}
	if !filepath.IsAbs(in.BaseDir) {
		return out, errors.New("storage base directory must be absolute")
	}
	out.Backend = selected
	stateDir := in.StateDir
	if selected == SQLite {
		path := in.DBPath
		if path == "" {
			path = filepath.Join("data", "meshcore.db")
		}
		var err error
		if out.DBPath, err = storagePath(in.BaseDir, path); err != nil {
			return Storage{}, err
		}
		path = in.UsersDBPath
		if path == "" && in.ExistingBackend == "" {
			path = filepath.Join(filepath.Dir(out.DBPath), "users.db")
		}
		if path != "" {
			if out.UsersDBPath, err = storagePath(in.BaseDir, path); err != nil {
				return Storage{}, err
			}
		}
		same := out.DBPath == out.UsersDBPath
		if runtime.GOOS == "windows" {
			same = strings.EqualFold(out.DBPath, out.UsersDBPath)
		}
		if same {
			return Storage{}, errors.New("telemetry and accounts require separate database paths")
		}
		if stateDir == "" {
			stateDir = filepath.Dir(out.DBPath)
		}
	} else {
		out.ReaderDatabaseURL = in.ReaderDatabaseURL
		if out.ReaderDatabaseURL == "" {
			out.ReaderDatabaseURL = in.DatabaseURL
		}
		out.WriterDatabaseURL = in.WriterDatabaseURL
		if out.WriterDatabaseURL == "" {
			out.WriterDatabaseURL = in.DatabaseURL
		}
		if out.ReaderDatabaseURL == "" && out.WriterDatabaseURL == "" {
			return Storage{}, errors.New("PostgreSQL is selected but its telemetry connection settings are missing")
		}
		out.UsersDatabaseURL, out.ApprovedChannelsDatabaseURL = in.UsersDatabaseURL, in.ApprovedChannelsDatabaseURL
		for _, value := range []string{out.ReaderDatabaseURL, out.WriterDatabaseURL, out.UsersDatabaseURL, out.ApprovedChannelsDatabaseURL} {
			if value == "" {
				continue
			}
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
				return Storage{}, errors.New("PostgreSQL targets require explicit PostgreSQL database URLs")
			}
		}
		if stateDir == "" {
			stateDir = "data"
		}
	}
	var err error
	if out.StateDir, err = storagePath(in.BaseDir, stateDir); err != nil {
		return Storage{}, err
	}
	return out, nil
}

func storagePath(base, path string) (string, error) {
	if strings.Contains(path, "://") || strings.HasPrefix(path, "postgres:") || strings.HasPrefix(path, "postgresql:") {
		return "", errors.New("a database URL cannot be used as a storage path")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if filepath.VolumeName(path) != "" {
		return "", errors.New("drive-relative storage paths are ambiguous; use an absolute path")
	}
	return filepath.Join(base, path), nil
}
