package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

const revPK = "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"

func revisionOf(t *testing.T, st *Store) int64 {
	t.Helper()
	info, err := st.GetRepeaterInfo(revPK)
	if errors.Is(err, ErrNotFound) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Revision
}

func TestRepeaterInfoRevisionCountsSaves(t *testing.T) {
	st, _ := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "Aa")
	if got := revisionOf(t, st); got != 0 {
		t.Fatalf("revision without a row = %d, want 0", got)
	}
	for want := int64(1); want <= 3; want++ {
		if _, err := st.SaveRepeaterInfo(u.ID, revPK, RepeaterEdit{GainDBi: float64(want)}); err != nil {
			t.Fatal(err)
		}
		if got := revisionOf(t, st); got != want {
			t.Fatalf("after save %d revision = %d", want, got)
		}
	}
	rows, err := st.ListRepeaterInfo()
	if err != nil || len(rows) != 1 || rows[0].Revision != 3 {
		t.Fatalf("ListRepeaterInfo = %+v, %v", rows, err)
	}
}

func TestSaveRepeaterInfoAtFirstSave(t *testing.T) {
	st, _ := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "Aa")
	// A base above 0 when nothing is saved yet is stale.
	if _, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Notes: "x"}, 1); !errors.Is(err, ErrRepeaterConflict) {
		t.Fatalf("base 1 without a row: %v, want ErrRepeaterConflict", err)
	}
	if _, err := st.GetRepeaterInfo(revPK); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused save created a row: %v", err)
	}
	prev, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Notes: "first"}, 0)
	if err != nil || prev != nil || revisionOf(t, st) != 1 {
		t.Fatalf("first save from 0: prev=%+v err=%v rev=%d", prev, err, revisionOf(t, st))
	}
	// Base 0 again means "nothing saved yet", which is no longer true.
	if _, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Notes: "second"}, 0); !errors.Is(err, ErrRepeaterConflict) {
		t.Fatalf("second save from 0: %v, want ErrRepeaterConflict", err)
	}
	if info, _ := st.GetRepeaterInfo(revPK); info.Notes != "first" || info.Revision != 1 {
		t.Fatalf("row after the refused save = %+v", info)
	}
}

func TestSaveRepeaterInfoAtStaleBase(t *testing.T) {
	st, _ := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "Aa")
	if _, err := st.SaveRepeaterInfo(u.ID, revPK, RepeaterEdit{Notes: "one"}); err != nil {
		t.Fatal(err)
	}
	prev, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Notes: "two"}, 1)
	if err != nil || prev == nil || prev.Notes != "one" || revisionOf(t, st) != 2 {
		t.Fatalf("save from 1: prev=%+v err=%v rev=%d", prev, err, revisionOf(t, st))
	}
	if _, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Notes: "stale"}, 1); !errors.Is(err, ErrRepeaterConflict) {
		t.Fatalf("save from stale 1: %v, want ErrRepeaterConflict", err)
	}
	// Validation still comes first.
	if _, err := st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{Status: "planned"}, 2); errors.Is(err, ErrRepeaterConflict) || err == nil {
		t.Fatalf("invalid edit: %v, want a validation error", err)
	}
	if info, _ := st.GetRepeaterInfo(revPK); info.Notes != "two" || info.Revision != 2 {
		t.Fatalf("row = %+v", info)
	}
}

// Of several saves from the same base, exactly one wins: both for the first
// save of a repeater and for a later one.
func TestSaveRepeaterInfoAtConcurrent(t *testing.T) {
	st, _ := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "Aa")
	race := func(base int64) (wins, conflicts int) {
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = st.SaveRepeaterInfoAt(u.ID, revPK, RepeaterEdit{GainDBi: float64(i)}, base)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrRepeaterConflict):
				conflicts++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		return
	}
	if w, c := race(0); w != 1 || c != 7 {
		t.Fatalf("first-save race: %d wins, %d conflicts", w, c)
	}
	if w, c := race(1); w != 1 || c != 7 {
		t.Fatalf("update race: %d wins, %d conflicts", w, c)
	}
	if got := revisionOf(t, st); got != 2 {
		t.Fatalf("revision = %d, want 2", got)
	}
}

// A users.db written by a v9 binary keeps its repeater details, which start
// at revision 1.
func TestMigrateV9DatabaseToV10(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (9)`}
	for _, m := range migrations[:9] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts, `INSERT INTO repeater_info (pubkey, notes, updated_at) VALUES ('`+revPK+`', 'old', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v9 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v9 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) || v < 10 {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	info, err := st.GetRepeaterInfo(revPK)
	if err != nil || info.Notes != "old" || info.Revision != 1 {
		t.Fatalf("migrated row = %+v, %v", info, err)
	}
}
