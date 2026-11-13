package dbconfig

import (
	"strings"
	"testing"
)

func TestGetIncrementalVacuumPages_Default(t *testing.T) {
	var c *DBConfig
	if got := c.GetIncrementalVacuumPages(); got != 1024 {
		t.Fatalf("nil DBConfig: got %d, want 1024", got)
	}
	c = &DBConfig{}
	if got := c.GetIncrementalVacuumPages(); got != 1024 {
		t.Fatalf("zero DBConfig: got %d, want 1024", got)
	}
}

func TestGetIncrementalVacuumPages_Configured(t *testing.T) {
	c := &DBConfig{IncrementalVacuumPages: 512}
	if got := c.GetIncrementalVacuumPages(); got != 512 {
		t.Fatalf("got %d, want 512", got)
	}
}

func TestGetDriver(t *testing.T) {
	cases := map[string]string{
		"": DriverSQLite, "sqlite": DriverSQLite, "sqlite3": DriverSQLite,
		"  SQLite3 ": DriverSQLite, "SQLITE": DriverSQLite,
		"postgres": DriverPostgres, "postgresql": DriverPostgres, "pg": DriverPostgres,
		" PG ": DriverPostgres, "PostgreSQL": DriverPostgres, "POSTGRES\t": DriverPostgres,
	}
	for in, want := range cases {
		got, err := (&DBConfig{Driver: in}).GetDriver()
		if err != nil || got != want {
			t.Errorf("GetDriver(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestGetDriverInvalid(t *testing.T) {
	_, err := (&DBConfig{Driver: "mysql"}).GetDriver()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{"mysql", "sqlite", "postgres"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q should mention %q", err, s)
		}
	}
}

func TestGetDriverNil(t *testing.T) {
	var c *DBConfig
	if got, err := c.GetDriver(); err != nil || got != DriverSQLite {
		t.Fatalf("nil: %q, %v", got, err)
	}
}

func TestValidate(t *testing.T) {
	var nilCfg *DBConfig
	ok := []*DBConfig{nilCfg, {}, {Driver: "sqlite"}, {Driver: "sqlite", DSN: "ignored"},
		{Driver: "pg", DSN: "postgres://u:p@h/db"}}
	for i, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("case %d: unexpected error %v", i, err)
		}
	}
	for _, c := range []*DBConfig{{Driver: "bogus"}, {Driver: "postgres"}, {Driver: "postgresql", DSN: "  "}} {
		if err := c.Validate(); err == nil {
			t.Errorf("%+v: expected error", c)
		}
	}
	err := (&DBConfig{Driver: "postgres", DSN: ""}).Validate()
	if err == nil || !strings.Contains(err.Error(), "dsn") {
		t.Errorf("postgres without dsn: %v", err)
	}
}
