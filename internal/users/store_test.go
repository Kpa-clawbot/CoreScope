package users

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "users.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	v, err := st.SchemaVersion()
	if err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()
	if v2, _ := st2.SchemaVersion(); v2 != len(migrations) {
		t.Fatalf("version after reopen = %d", v2)
	}
}

func TestOpenRefusesForbiddenPath(t *testing.T) {
	dir := t.TempDir()
	measurement := filepath.Join(dir, "meshcore.db")
	if err := os.WriteFile(measurement, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(measurement, measurement)
	if err == nil || !strings.Contains(err.Error(), "measurement database") {
		t.Fatalf("Open(measurement) err = %v; want refusal", err)
	}
	// A relative spelling of the same file is refused too.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	if _, err := Open("meshcore.db", measurement); err == nil {
		t.Fatal("relative path to the measurement DB was not refused")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE schema_version SET version = 999`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open with newer schema err = %v", err)
	}
}
