package dbschema

import (
	"strings"
	"testing"
)

// AssertReady must require observers.idx to be the primary key, not merely a
// column of that name: observations.observer_idx relies on idx being the
// rowid alias, and a plain idx column says nothing about which row it names.
func TestAssertReadyRequiresObserversIdxPrimaryKey(t *testing.T) {
	cases := []struct {
		name    string
		reshape []string // applied to observers after a full Apply
		wantErr string   // "" = AssertReady passes
	}{
		{"rebuilt shape", nil, ""},
		{"plain idx column", []string{
			`DROP TABLE observers`,
			legacyObserversDDL,
			`ALTER TABLE observers ADD COLUMN idx INTEGER`,
			`UPDATE observers SET idx = rowid`,
		}, "observers.idx (not the primary key)"},
		{"no idx column", []string{
			`DROP TABLE observers`,
			legacyObserversDDL,
		}, "missing: observers.idx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := minimalDB(t)
			defer db.Close()
			if err := Apply(db, nil); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			mustExec(t, db, tc.reshape...)
			err := AssertReady(db)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("AssertReady: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AssertReady err = %v, want mention of %q", err, tc.wantErr)
			}
			if !strings.HasPrefix(err.Error(), "schema not migrated by ingestor; restart ingestor first.") {
				t.Errorf("AssertReady err does not tell the operator what to do: %v", err)
			}
		})
	}
}
