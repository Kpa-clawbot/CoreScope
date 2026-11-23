package dbschema

import (
	"database/sql"
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

// nonAliasIdxRefusal is how the rebuild names an idx that is not the rowid
// alias.
const nonAliasIdxRefusal = "idx column that is not the rowid alias"

// assertKeyIdxWayOut checks the refusal for an idx in the primary key: it
// must not suggest ALTER TABLE DROP COLUMN, which SQLite rejects for a
// primary-key column (review of #2445) — proven here by running it.
func assertKeyIdxWayOut(t *testing.T, db *sql.DB) {
	t.Helper()
	err := ensureObserversIdx(db, nil)
	if err == nil {
		t.Fatal("ensureObserversIdx accepted an idx that is not the rowid alias")
	}
	for _, want := range []string{"in the primary key", "recreate the observers table without an idx column", "previous CoreScope version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "DROP COLUMN") {
		t.Errorf("refusal suggests DROP COLUMN for a primary-key idx: %v", err)
	}
	if _, derr := db.Exec(dropPlainIdxSQL); derr == nil || !strings.Contains(derr.Error(), "PRIMARY KEY") {
		t.Fatalf("%s on a primary-key idx = %v, want SQLite's PRIMARY KEY refusal (fixture is not the case under test)", dropPlainIdxSQL, derr)
	}
}

// A primary key that merely includes a column named idx is not the rebuilt
// shape: the rebuild must not take it for "already migrated", and refuses it
// (idx is canonical only as the rowid alias) without touching the table.
func TestObserversIdxRefusesNonRowidIdxKey(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"idx TEXT primary key", idxTextPKObserversDDL},
		{"composite primary key", idxCompositePKObserversDDL},
		{"INTEGER PRIMARY KEY DESC", idxDescPKObserversDDL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := legacyDBWith(t, tc.ddl, idxShapedSeed)
			assertRefusedUntouched(t, db, nonAliasIdxRefusal)
			assertKeyIdxWayOut(t, db)
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
	if err == nil || !strings.Contains(err.Error(), nonAliasIdxRefusal) {
		t.Fatalf("ensureObserversIdx err = %v, want the refusal of an idx that is not the rowid alias", err)
	}
	assertKeyIdxWayOut(t, db)
	if got := dumpRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`); !reflect.DeepEqual(schema, got) {
		t.Errorf("schema changed by refused rebuild:\nbefore=%v\nafter=%v", schema, got)
	}
	if got := dumpRows(t, db, `SELECT * FROM observers ORDER BY idx`); !reflect.DeepEqual(rows, got) {
		t.Errorf("observer rows changed by refused rebuild")
	}
}

// A plain idx column (in no key) is refused too, with its own way out: the
// statement the refusal gives runs as given, and the next start then
// completes the rebuild.
func TestObserversIdxRefusesPlainIdxColumn(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db, `ALTER TABLE observers ADD COLUMN idx INTEGER`, `UPDATE observers SET idx = rowid + 100`)
	assertRefusedUntouched(t, db, nonAliasIdxRefusal)
	err := ensureObserversIdx(db, nil)
	for _, want := range []string{"(" + dropPlainIdxSQL + ")", "previous CoreScope version"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %v does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "canonical shape") {
		t.Errorf("idx reported as an unknown column: %v", err)
	}
	mustExec(t, db, dropPlainIdxSQL)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after the suggested %s: %v", dropPlainIdxSQL, err)
	}
	if got, want := dumpRows(t, db, `SELECT idx, id FROM observers ORDER BY idx`), []string{"[1 a]", "[2 b]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("observers after the rebuild = %v, want %v", got, want)
	}
}

// SQLite refuses DROP COLUMN while an index, view or trigger uses the column,
// so the refusal says to drop those first; with the index gone the statement
// it gives runs and the rebuild completes (review of #2445).
func TestObserversIdxPlainIdxRefusalCoversIndexedColumn(t *testing.T) {
	db := legacyFullDB(t)
	mustExec(t, db,
		`ALTER TABLE observers ADD COLUMN idx INTEGER`,
		`UPDATE observers SET idx = rowid + 100`,
		`CREATE INDEX my_idx ON observers(idx)`,
	)
	assertRefusedUntouched(t, db, nonAliasIdxRefusal)
	err := ensureObserversIdx(db, nil)
	for _, want := range []string{"(" + dropPlainIdxSQL + ")", "first drop any index, view or trigger that uses the column", "recreate it after"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %v does not say %q", err, want)
		}
	}
	if _, derr := db.Exec(dropPlainIdxSQL); derr == nil {
		t.Fatalf("%s ran with my_idx on the column (fixture is not the case under test)", dropPlainIdxSQL)
	}
	mustExec(t, db, `DROP INDEX my_idx`, dropPlainIdxSQL)
	if err := ensureObserversIdx(db, nil); err != nil {
		t.Fatalf("rebuild after dropping my_idx and the suggested %s: %v", dropPlainIdxSQL, err)
	}
	if got, want := dumpRows(t, db, `SELECT idx, id FROM observers ORDER BY idx`), []string{"[1 a]", "[2 b]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("observers after the rebuild = %v, want %v", got, want)
	}
}
