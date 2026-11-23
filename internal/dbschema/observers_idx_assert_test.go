package dbschema

import (
	"strings"
	"testing"
)

// AssertReady must require observers.idx to be the rowid alias, not merely a
// column or a primary key of that name: observations.observer_idx relies on
// idx being the rowid, and any other idx says nothing about which row it
// names. The message says "rowid alias" because in three of these shapes idx
// IS the primary key.
func TestAssertReadyRequiresObserversIdxRowidAlias(t *testing.T) {
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
		}, "observers.idx (not the rowid alias)"},
		{"idx TEXT primary key", []string{
			`DROP TABLE observers`,
			idxTextPKObserversDDL,
		}, "observers.idx (not the rowid alias)"},
		{"composite primary key", []string{
			`DROP TABLE observers`,
			idxCompositePKObserversDDL,
		}, "observers.idx (not the rowid alias)"},
		{"INTEGER PRIMARY KEY DESC", []string{
			`DROP TABLE observers`,
			idxDescPKObserversDDL,
		}, "observers.idx (not the rowid alias)"},
		{"WITHOUT ROWID", []string{
			`DROP TABLE observers`,
			idxWithoutRowidObserversDDL,
		}, "observers.idx (not the rowid alias)"},
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
