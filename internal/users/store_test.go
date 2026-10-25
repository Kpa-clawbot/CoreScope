package users

import (
	"github.com/meshcore-analyzer/dbconfig"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	dsn := testTarget(t, false)
	db := testOwner(t, dsn)
	if err := applyTestSchema(t, db); err != nil {
		t.Fatal(err)
	}
	runtimeDSN := testRuntimeURL(t, dsn)
	for i := 0; i < 2; i++ {
		st, err := Open(runtimeDSN)
		if err != nil {
			t.Fatal(err)
		}
		if v, err := st.SchemaVersion(); err != nil || v != schemaVersion(t) {
			t.Fatalf("schema=%d,%v", v, err)
		}
		st.Close()
		if err := applyTestSchema(t, db); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRefusesForbiddenPath(t *testing.T) {
	dsn := testTarget(t, false)
	alias, _ := url.Parse(dsn)
	if testBackend(t) == dbconfig.SQLite {
		if st, err := Open(dsn, filepath.Join(filepath.Dir(dsn), ".", filepath.Base(dsn))); err == nil {
			st.Close()
			t.Fatal("same SQLite target accepted")
		}
		return
	}
	q := alias.Query()
	q.Set("application_name", "different-client")
	q.Set("search_path", "public")
	alias.RawQuery = q.Encode()
	if st, err := Open(dsn, alias.String()); err == nil || !strings.Contains(err.Error(), "measurement database") {
		if st != nil {
			st.Close()
		}
		t.Fatal("the same effective database was not refused")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	dsn := testTarget(t, false)
	db := testOwner(t, dsn)
	if err := applyTestSchema(t, db); err != nil {
		t.Fatal(err)
	}
	runtimeDSN := testRuntimeURL(t, dsn)
	if _, err := db.Exec(`UPDATE schema_version SET version=999`); err != nil {
		t.Fatal(err)
	}
	if st, err := Open(runtimeDSN); err == nil {
		if st != nil {
			st.Close()
		}
		t.Fatalf("Open with newer schema error=%v", err)
	}
	if err := applyTestSchema(t, db); err == nil {
		t.Fatal("bootstrap accepted a newer schema")
	}
}

func TestOpenRejectsDSNCharacters(t *testing.T) {
	for _, dsn := range []string{"", "file:users.db", "postgresql://u:p@host/a?host=elsewhere", "postgresql://u:p@host1,host2/a"} {
		if st, err := Open(dsn); err == nil {
			st.Close()
			t.Fatal("unsupported database address accepted")
		}
	}
}

func TestApplyConcurrentAndIncomplete(t *testing.T) {
	requirePostgres(t)
	dsn := testTarget(t, false)
	db := testOwner(t, dsn)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := applyTestSchema(t, db); err != nil {
				t.Errorf("concurrent bootstrap: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := db.Exec(`UPDATE corescope_schema SET ready=false`); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err == nil {
		t.Fatal("incomplete import accepted")
	}
	if err := applyTestSchema(t, db); err == nil {
		t.Fatal("bootstrap completed an unverified import")
	}
}
