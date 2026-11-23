package dbx

import (
	"database/sql"
	"database/sql/driver"
	"testing"
)

// database/sql probes the driver conn for these optional interfaces and
// silently falls back to different behaviour when one is missing (e.g. no
// NamedValueChecker means default argument conversion, which rejects slices).
// The wrapper must keep every one of them.
func TestConnImplementsOptionalInterfaces(t *testing.T) {
	var c any = &conn{}
	checks := map[string]bool{}
	_, checks["driver.Conn"] = c.(driver.Conn)
	_, checks["driver.ConnBeginTx"] = c.(driver.ConnBeginTx)
	_, checks["driver.ConnPrepareContext"] = c.(driver.ConnPrepareContext)
	_, checks["driver.QueryerContext"] = c.(driver.QueryerContext)
	_, checks["driver.ExecerContext"] = c.(driver.ExecerContext)
	_, checks["driver.Pinger"] = c.(driver.Pinger)
	_, checks["driver.SessionResetter"] = c.(driver.SessionResetter)
	_, checks["driver.Validator"] = c.(driver.Validator)
	_, checks["driver.NamedValueChecker"] = c.(driver.NamedValueChecker)
	for name, ok := range checks {
		if !ok {
			t.Errorf("*conn does not implement %s", name)
		}
	}
}

func TestDriverRegistered(t *testing.T) {
	db, err := sql.Open(PostgresDriverName, "postgres://unused.invalid/db")
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", PostgresDriverName, err)
	}
	defer db.Close()
	d, ok := db.Driver().(*qmarkDriver)
	if !ok {
		t.Fatalf("registered driver is %T, want *qmarkDriver", db.Driver())
	}
	if _, ok := any(d).(driver.DriverContext); !ok {
		t.Fatal("driver does not implement driver.DriverContext")
	}
	cn, err := d.OpenConnector("postgres://unused.invalid/db")
	if err != nil {
		t.Fatalf("OpenConnector: %v", err)
	}
	if cn.Driver() != d {
		t.Fatalf("connector.Driver() = %T, want the qmark driver", cn.Driver())
	}
}
