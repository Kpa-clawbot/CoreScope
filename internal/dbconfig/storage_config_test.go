package dbconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStorageConfigKeepsRawPathsAndDisabledAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"db":{"backend":"sqlite"},"dbPath":"relative.db","databaseURL":"inactive","stateDir":"stable","userManagement":{"enabled":false,"dbPath":"accounts.db","databaseURL":"runtime","approvedChannelsDatabaseURL":"reader"},"port":3000}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStorageConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != SQLite || got.DBPath != "relative.db" || got.UsersDBPath != "accounts.db" || got.StateDir != "stable" || got.UsersDatabaseURL != "runtime" || got.ApprovedChannelsDatabaseURL != "reader" || got.DatabaseURL != "inactive" {
		t.Fatal("storage inputs were defaulted or dropped")
	}
	for _, invalid := range []string{`{"dbPath":8}`, `{} {}`, `null`, `{"db":{"backend":42}}`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadStorageConfig(path); err == nil {
			t.Fatal("invalid explicit config accepted", invalid)
		}
	}
	if _, err := ReadStorageConfig(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing explicit file accepted")
	}
}
