package dbschema

import (
	"reflect"
	"strings"
	"testing"
)

// Observers tables with a column named idx in the primary key that is NOT
// the rowid alias observations.observer_idx relies on. Each carries every
// canonical column, so only the key shape is wrong.
var (
	idxTextPKObserversDDL = strings.Replace(legacyObserversDDL,
		"id TEXT PRIMARY KEY,", "idx TEXT PRIMARY KEY,\n\tid TEXT NOT NULL UNIQUE,", 1)
	idxCompositePKObserversDDL = strings.Replace(strings.Replace(legacyObserversDDL,
		"id TEXT PRIMARY KEY,", "idx INTEGER,\n\tid TEXT NOT NULL,", 1),
		"can_relay_seen INTEGER DEFAULT 0\n)", "can_relay_seen INTEGER DEFAULT 0,\n\tPRIMARY KEY (idx, id)\n)", 1)
	// SQLite quirk: a column-constraint PRIMARY KEY DESC on an INTEGER column
	// does not make it the rowid alias (https://sqlite.org/lang_createtable.html#rowid).
	idxDescPKObserversDDL = strings.Replace(legacyObserversDDL,
		"id TEXT PRIMARY KEY,", "idx INTEGER PRIMARY KEY DESC,\n\tid TEXT NOT NULL UNIQUE,", 1)
	// A WITHOUT ROWID table has no rowid at all, so idx aliases nothing.
	idxWithoutRowidObserversDDL = strings.Replace(legacyObserversDDL,
		"id TEXT PRIMARY KEY,", "idx INTEGER PRIMARY KEY,\n\tid TEXT NOT NULL UNIQUE,", 1) + " WITHOUT ROWID"
)

const idxShapedSeed = `INSERT INTO observers (idx, id, name) VALUES (7, 'a', 'A'), (9, 'b', 'B')`

// A primary key that merely includes a column named idx is not the rebuilt
// shape: the rebuild must not take it for "already migrated", and refuses it
// on the unknown-column path (idx is canonical only as the rowid alias)
// without touching the table.
func TestObserversIdxRefusesNonRowidIdxKey(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"idx TEXT primary key", idxTextPKObserversDDL},
		{"composite primary key", idxCompositePKObserversDDL},
		{"INTEGER PRIMARY KEY DESC", idxDescPKObserversDDL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := legacyDBWith(t, tc.ddl, idxShapedSeed)
			assertRefusedUntouched(t, db, "outside the canonical shape (idx)")
		})
	}
}

// WITHOUT ROWID has no rowid, so assertRefusedUntouched's rowid reads (and
// the legacy rowid join of packets_v) don't apply; check schema and rows.
func TestObserversIdxRefusesWithoutRowid(t *testing.T) {
	db := legacyDBWith(t, idxWithoutRowidObserversDDL, idxShapedSeed)
	schema := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`)
	rows := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`)
	err := ensureObserversIdx(db, nil)
	if err == nil || !strings.Contains(err.Error(), "outside the canonical shape (idx)") {
		t.Fatalf("ensureObserversIdx err = %v, want the unknown-column refusal naming idx", err)
	}
	if got := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`); !reflect.DeepEqual(schema, got) {
		t.Errorf("schema changed by refused rebuild:\nbefore=%v\nafter=%v", schema, got)
	}
	if got := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(rows, got) {
		t.Errorf("observer rows changed by refused rebuild")
	}
}
