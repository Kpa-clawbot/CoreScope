package dbconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const selectionVersion = 1

var (
	ErrSelectionMissing    = errors.New("installation selection is absent; setup or the writer must validate and adopt existing storage before reader startup")
	ErrSelectionBusy       = errors.New("installation is in use; stop its services before backend adoption, switching or recovery")
	ErrSelectionInProgress = errors.New("an incomplete backend switch blocks startup; keep writers stopped and explicitly resume or abort that job")
	ErrSelectionChanged    = errors.New("recorded storage target or generation changed; use a verified offline selection update")
)

type PostgresTarget struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

type Target struct {
	SQLitePath string          `json:"sqlite_path,omitempty"`
	Postgres   *PostgresTarget `json:"postgres,omitempty"`
}

// Selection records endpoints, never authentication or TLS settings. Accounts
// describe the configured store independently of whether its feature is enabled.
type Selection struct {
	Version    int     `json:"version"`
	Generation string  `json:"generation"`
	Backend    Backend `json:"backend"`
	StateDir   string  `json:"state_dir"`
	Telemetry  Target  `json:"telemetry"`
	Accounts   *Target `json:"accounts"`
}

func SelectionPath(stateDir string) string { return filepath.Join(stateDir, "storage-selection.json") }

func newSelectionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validSelectionID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

// NewSelection constructs a proposal. Callers validate the same native targets
// before adopting/committing it. It never connects or infers freshness.
func NewSelection(storage Storage) (Selection, error) {
	s := Selection{Version: selectionVersion, Backend: storage.Backend, StateDir: storage.StateDir}
	var err error
	if s.Generation, err = newSelectionID(); err != nil {
		return Selection{}, err
	}
	switch s.Backend {
	case SQLite:
		s.Telemetry = Target{SQLitePath: storage.DBPath}
		if storage.UsersDBPath != "" {
			s.Accounts = &Target{SQLitePath: storage.UsersDBPath}
		}
	case Postgres:
		if s.Telemetry, err = postgresRoleTarget(storage.ReaderDatabaseURL, storage.WriterDatabaseURL); err != nil {
			return Selection{}, err
		}
		if storage.UsersDatabaseURL != "" || storage.ApprovedChannelsDatabaseURL != "" {
			target, err := postgresRoleTarget(storage.UsersDatabaseURL, storage.ApprovedChannelsDatabaseURL)
			if err != nil {
				return Selection{}, err
			}
			s.Accounts = &target
		}
	default:
		return Selection{}, errors.New("selection requires a validated backend")
	}
	return s, s.validate()
}

func (s Selection) validate() error {
	if s.Version != selectionVersion || !validSelectionID(s.Generation) || !filepath.IsAbs(s.StateDir) || filepath.Clean(s.StateDir) != s.StateDir {
		return errors.New("invalid or unsupported installation selection")
	}
	check := func(target Target) bool {
		if s.Backend == SQLite {
			return target.Postgres == nil && filepath.IsAbs(target.SQLitePath) && filepath.Clean(target.SQLitePath) == target.SQLitePath && !strings.ContainsRune(target.SQLitePath, 0)
		}
		p := target.Postgres
		return s.Backend == Postgres && target.SQLitePath == "" && p != nil && p.Host != "" && p.Port != 0 && p.Database != "" && p.Schema != "" && !strings.ContainsRune(p.Host+p.Database+p.Schema, 0)
	}
	if !check(s.Telemetry) || (s.Accounts != nil && !check(*s.Accounts)) {
		return errors.New("installation selection has invalid storage targets")
	}
	if s.Accounts != nil {
		if s.Backend == SQLite && s.Telemetry.SQLitePath == s.Accounts.SQLitePath {
			return errors.New("telemetry and account targets must remain separate")
		}
		if s.Backend == Postgres {
			a, b := s.Telemetry.Postgres, s.Accounts.Postgres
			if a.Host == b.Host && a.Port == b.Port && a.Database == b.Database {
				return errors.New("telemetry and account databases must remain separate")
			}
		}
	}
	return nil
}

func postgresRoleTarget(first, second string) (Target, error) {
	if first == "" {
		first = second
	}
	if first == "" {
		return Target{}, errors.New("PostgreSQL target requires a role URL")
	}
	value, err := postgresTarget(first)
	if err != nil {
		return Target{}, err
	}
	if err := bootstrapPostgresEnvironment(first); err != nil {
		return Target{}, err
	}
	if second != "" {
		other, err := postgresTarget(second)
		if err != nil {
			return Target{}, err
		}
		if value != other {
			return Target{}, ErrSelectionChanged
		}
		if err := bootstrapPostgresEnvironment(second); err != nil {
			return Target{}, err
		}
	}
	return Target{Postgres: &value}, nil
}

func bootstrapPostgresEnvironment(value string) error {
	if os.Getenv("PGOPTIONS") != "" || os.Getenv("PGSERVICE") != "" {
		return errors.New("initial PostgreSQL selection requires explicit URL settings instead of PGSERVICE or PGOPTIONS")
	}
	u, err := url.Parse(value)
	if err != nil {
		return errors.New("invalid PostgreSQL target URL")
	}
	if u.Port() == "" && os.Getenv("PGPORT") != "" {
		port, err := strconv.ParseUint(os.Getenv("PGPORT"), 10, 16)
		if err != nil || port != 5432 {
			return errors.New("initial PostgreSQL selection has an ambiguous ambient PGPORT; supply an explicit URL port")
		}
	}
	return nil
}

var unquotedSchema = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func selectionSchema(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "public", nil
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) && len(value) > 2 {
		inner := value[1 : len(value)-1]
		if strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`) || strings.ContainsRune(inner, 0) {
			return "", errors.New("selection requires one explicit PostgreSQL schema")
		}
		return strings.ReplaceAll(inner, `""`, `"`), nil
	}
	if !unquotedSchema.MatchString(value) {
		return "", errors.New("selection requires one explicit PostgreSQL schema")
	}
	return strings.ToLower(value), nil
}

func postgresTarget(value string) (PostgresTarget, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || strings.Contains(u.Host, ",") || strings.TrimPrefix(u.Path, "/") == "" {
		return PostgresTarget{}, errors.New("invalid PostgreSQL target URL")
	}
	port := uint64(5432)
	if u.Port() != "" {
		port, err = strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return PostgresTarget{}, errors.New("invalid PostgreSQL target port")
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["search_path"]) > 1 {
		return PostgresTarget{}, errors.New("invalid PostgreSQL target schema setting")
	}
	// Drivers accept connection fields in the query as overrides of the URL.
	// Keep the recorded endpoint unambiguous, including service-file settings
	// and startup options that can replace search_path outside this parser.
	for _, key := range []string{"host", "hostaddr", "port", "dbname", "database", "service", "servicefile", "options"} {
		if _, present := q[key]; present {
			return PostgresTarget{}, errors.New("PostgreSQL target overrides must be expressed in the URL authority, database path and search_path")
		}
	}
	schema, err := selectionSchema(q.Get("search_path"))
	if err != nil {
		return PostgresTarget{}, err
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return PostgresTarget{Host: host, Port: uint16(port), Database: strings.TrimPrefix(u.Path, "/"), Schema: schema}, nil
}

func bindSelectedURL(value string, target *PostgresTarget) (string, error) {
	if value == "" {
		return "", nil
	}
	if target == nil {
		return "", ErrSelectionChanged
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid PostgreSQL target URL")
	}
	// Missing port/schema are bootstrap defaults, not permission to change an
	// installed target through PGPORT or a rotated role's default search_path.
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(int(target.Port)))
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("invalid PostgreSQL target URL query")
	}
	if !q.Has("search_path") {
		q.Set("search_path", `"`+strings.ReplaceAll(target.Schema, `"`, `""`)+`"`)
		u.RawQuery = q.Encode()
	}
	actual, err := postgresTarget(u.String())
	if err != nil {
		return "", err
	}
	if actual != *target {
		return "", ErrSelectionChanged
	}
	u.Host = net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port)))
	u.Path = "/" + target.Database
	u.RawPath = ""
	// Pin the selected schema even when rotated role defaults differ. Host and
	// database are never rebound; credentials and TLS options remain untouched.
	q.Set("search_path", `"`+strings.ReplaceAll(target.Schema, `"`, `""`)+`"`)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ApplyTo makes the installed record authoritative over bootstrap selectors
// and paths. Only active PostgreSQL role credentials/TLS remain caller inputs.
func (s Selection) ApplyTo(raw StorageInputs) (StorageInputs, error) {
	if err := s.validate(); err != nil {
		return StorageInputs{}, err
	}
	raw.Backend, raw.EnvBackend, raw.ExistingBackend = s.Backend, s.Backend, s.Backend
	raw.FreshInstall = false
	raw.StateDir = s.StateDir
	raw.DBPath, raw.UsersDBPath = "", ""
	if s.Backend == SQLite {
		raw.DBPath = s.Telemetry.SQLitePath
		if s.Accounts != nil {
			raw.UsersDBPath = s.Accounts.SQLitePath
		}
		return raw, nil
	}
	reader, writer := raw.ReaderDatabaseURL, raw.WriterDatabaseURL
	if reader == "" {
		reader = raw.DatabaseURL
	}
	if writer == "" {
		writer = raw.DatabaseURL
	}
	var err error
	if raw.ReaderDatabaseURL, err = bindSelectedURL(reader, s.Telemetry.Postgres); err != nil {
		return StorageInputs{}, err
	}
	if raw.WriterDatabaseURL, err = bindSelectedURL(writer, s.Telemetry.Postgres); err != nil {
		return StorageInputs{}, err
	}
	var account *PostgresTarget
	if s.Accounts != nil {
		account = s.Accounts.Postgres
	} else {
		raw.UsersDatabaseURL, raw.ApprovedChannelsDatabaseURL = "", ""
	}
	if raw.UsersDatabaseURL, err = bindSelectedURL(raw.UsersDatabaseURL, account); err != nil {
		return StorageInputs{}, err
	}
	if raw.ApprovedChannelsDatabaseURL, err = bindSelectedURL(raw.ApprovedChannelsDatabaseURL, account); err != nil {
		return StorageInputs{}, err
	}
	raw.DatabaseURL = "" // Role URLs are now bound; an obsolete fallback is inactive.
	return raw, nil
}

func sameSelectionTargets(a, b Selection) bool {
	a.Generation, b.Generation = "", ""
	return reflect.DeepEqual(a, b)
}

func cloneSelection(s Selection) Selection {
	if s.Telemetry.Postgres != nil {
		target := *s.Telemetry.Postgres
		s.Telemetry.Postgres = &target
	}
	if s.Accounts != nil {
		target := *s.Accounts
		s.Accounts = &target
		if target.Postgres != nil {
			postgres := *target.Postgres
			target.Postgres = &postgres
		}
	}
	return s
}
