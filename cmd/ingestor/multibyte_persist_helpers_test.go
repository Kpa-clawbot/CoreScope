package main

import (
	"bytes"
	"database/sql"
	"log"
	"strings"
	"testing"
)

// captureLogs redirects the standard logger to a buffer for the
// duration of the test and returns the buffer. Restores the previous
// writer when the test ends.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

// logContains reports whether the captured log buffer contains substr
// (case-insensitive).
func logContains(buf *bytes.Buffer, substr string) bool {
	return strings.Contains(strings.ToLower(buf.String()), strings.ToLower(substr))
}

// columnExists reports whether the named column exists on the table.
func columnExists(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan PRAGMA: %v", err)
		}
		if name == col {
			return true
		}
	}
	return false
}
