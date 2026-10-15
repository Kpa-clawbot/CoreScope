package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestApprovedChannelsPendingSQLiteImportKeepsLastGoodKeys(t *testing.T) {
	t.Setenv("CORESCOPE_TEST_BACKEND", "sqlite")
	path := filepath.Join(t.TempDir(), "accounts.db")
	writeUsersDB(t, path, testProposal{"#complete", "approved", 1})
	keys := newApprovedTestKeySet(t, configuredKeys(), path, 128)
	defer keys.Close()
	keys.refresh()
	want := keys.Snapshot()["#complete"]
	if want == "" {
		t.Fatal("complete initial approval was not loaded")
	}
	execUsersDB(t, path, `CREATE TABLE corescope_reverse_progress (table_name TEXT)`)
	execUsersDB(t, path, `UPDATE proposals SET subject='#partial'`)
	if _, err := keys.readApproved(); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
		t.Fatalf("partial import was accepted: %v", err)
	}
	keys.refresh()
	if keys.Snapshot()["#complete"] != want || keys.Snapshot()["#partial"] != "" {
		t.Fatal("partial import replaced the last verified approval set")
	}
}
