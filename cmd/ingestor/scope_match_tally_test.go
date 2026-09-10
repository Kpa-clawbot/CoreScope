package main

import (
	"testing"
	"time"
)

// resetScopeMatchCounters zeroes the package-level tally and puts it back
// afterwards. The counters are global and OpenStore restores them from the
// DB, so every test here has to start from a known state; no test in this
// package calls t.Parallel, which is what makes that safe.
func resetScopeMatchCounters(t *testing.T) {
	t.Helper()
	prevUnique := scopeMatchCounters.unique.Load()
	prevExplicit := scopeMatchCounters.explicitOverDerived.Load()
	prevAmbiguous := scopeMatchCounters.ambiguous.Load()
	prevNone := scopeMatchCounters.none.Load()
	prevSince := scopeMatchCounters.sinceUnix.Load()
	t.Cleanup(func() {
		scopeMatchCounters.unique.Store(prevUnique)
		scopeMatchCounters.explicitOverDerived.Store(prevExplicit)
		scopeMatchCounters.ambiguous.Store(prevAmbiguous)
		scopeMatchCounters.none.Store(prevNone)
		scopeMatchCounters.sinceUnix.Store(prevSince)
	})
	scopeMatchCounters.unique.Store(0)
	scopeMatchCounters.explicitOverDerived.Store(0)
	scopeMatchCounters.ambiguous.Store(0)
	scopeMatchCounters.none.Store(0)
	scopeMatchCounters.sinceUnix.Store(0)
}

// TestScopeMatchTallySurvivesReopen is the whole point of the table: a
// restart used to reset the tally to zero and delete the container log that
// held the previous number, so a measurement in progress was unrecoverable.
func TestScopeMatchTallySurvivesReopen(t *testing.T) {
	resetScopeMatchCounters(t)
	dbPath := t.TempDir() + "/tally.db"

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	since := scopeMatchCounters.sinceUnix.Load()
	scopeMatchCounters.unique.Store(19692)
	scopeMatchCounters.ambiguous.Store(80)
	scopeMatchCounters.explicitOverDerived.Store(3)
	scopeMatchCounters.none.Store(41)
	if err := s.SaveScopeMatchTotals(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// A new process starts with zeroed counters.
	scopeMatchCounters.unique.Store(0)
	scopeMatchCounters.ambiguous.Store(0)
	scopeMatchCounters.explicitOverDerived.Store(0)
	scopeMatchCounters.none.Store(0)
	scopeMatchCounters.sinceUnix.Store(0)

	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if got := scopeMatchCounters.unique.Load(); got != 19692 {
		t.Errorf("unique = %d, want 19692 restored from the previous run", got)
	}
	if got := scopeMatchCounters.ambiguous.Load(); got != 80 {
		t.Errorf("ambiguous = %d, want 80 restored from the previous run", got)
	}
	if got := scopeMatchCounters.explicitOverDerived.Load(); got != 3 {
		t.Errorf("explicitOverDerived = %d, want 3", got)
	}
	if got := scopeMatchCounters.none.Load(); got != 41 {
		t.Errorf("none = %d, want 41", got)
	}
	if got := scopeMatchCounters.sinceUnix.Load(); got != since {
		t.Errorf("sinceUnix = %d, want %d — the window start must carry across the restart, or the ratio has no denominator", got, since)
	}
}

// TestScopeMatchTallyAnchorsSinceOnFirstOpen pins that a DB with no row gets
// one immediately. Without it the first window's start would only be written
// once something had already been counted, and a tally read after one restart
// would report a rate over an unknown period.
func TestScopeMatchTallyAnchorsSinceOnFirstOpen(t *testing.T) {
	resetScopeMatchCounters(t)
	before := time.Now().Unix()

	s, err := OpenStore(t.TempDir() + "/tally.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	since := scopeMatchCounters.sinceUnix.Load()
	if since < before || since > time.Now().Unix() {
		t.Errorf("sinceUnix = %d, want a stamp between %d and now", since, before)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM scope_match_totals`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("scope_match_totals rows = %d, want exactly 1 — the table holds a single running total", rows)
	}
}

// TestScopeMatchTallyContinuesAfterRestore checks the restored values are a
// starting point and not a ceiling: recording after a restart adds to the
// carried total rather than counting from zero again.
func TestScopeMatchTallyContinuesAfterRestore(t *testing.T) {
	resetScopeMatchCounters(t)
	dbPath := t.TempDir() + "/tally.db"

	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	scopeMatchCounters.unique.Store(10)
	scopeMatchCounters.ambiguous.Store(2)
	if err := s.SaveScopeMatchTotals(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	recordScopeMatch(scopeMatch{Reason: scopeReasonUnique, Name: "#be"})
	recordScopeMatch(scopeMatch{Reason: scopeReasonAmbiguous, Candidates: []string{"#be", "#nl"}})
	if err := s2.SaveScopeMatchTotals(); err != nil {
		t.Fatal(err)
	}

	var uniq, ambiguous int64
	if err := s2.db.QueryRow(`SELECT unique_matches, ambiguous FROM scope_match_totals WHERE id = 1`).Scan(&uniq, &ambiguous); err != nil {
		t.Fatal(err)
	}
	if uniq != 11 || ambiguous != 3 {
		t.Errorf("stored unique=%d ambiguous=%d, want 11 and 3 — counting must resume on top of the restored totals", uniq, ambiguous)
	}
}

// TestScopeMatchTallySaveIsIdempotent pins the single-row upsert: repeated
// saves must update the row, never accumulate rows. The CHECK(id = 1) makes a
// second row impossible, and this fails loudly if the INSERT is ever changed
// to one without the conflict clause.
func TestScopeMatchTallySaveIsIdempotent(t *testing.T) {
	resetScopeMatchCounters(t)

	s, err := OpenStore(t.TempDir() + "/tally.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		scopeMatchCounters.unique.Add(1)
		if err := s.SaveScopeMatchTotals(); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	var rows, uniq int64
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(unique_matches) FROM scope_match_totals`).Scan(&rows, &uniq); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || uniq != 3 {
		t.Errorf("rows=%d unique=%d, want 1 row holding 3", rows, uniq)
	}
}
