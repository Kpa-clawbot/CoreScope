package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// observers.rowid must not be used: observations.observer_idx holds
// observers.idx, the explicit key column. A bare rowid would silently break
// if the table were ever rebuilt (VACUUM renumbers rowids of tables without
// an INTEGER PRIMARY KEY).
func TestNoObserversRowidInServerSources(t *testing.T) {
	re := regexp.MustCompile(`\.rowid\b|rowid FROM observers`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				t.Errorf("%s:%d uses rowid on observers; use idx: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
