package main

import (
	"database/sql"
	"errors"
	"time"
)

// The scope-match tally lives in scopeMatchCounters, four atomics in this
// process and nowhere else. Every restart — a deploy, a crash, an operator
// bouncing the container — sets them back to zero and takes the container's
// log with it, so the previous count is not recoverable afterwards. That is
// how a finished 24h ambiguity measurement was lost.
//
// The numbers only say something over days: "ambiguous is 0.4% of matches"
// needs a denominator large enough to be stable, and the decision they gate
// (whether a collision tie-break is worth building) is not one to take on an
// hour of traffic. So they have to outlive the process doing the counting.
//
// scope_match_totals is one row. The ingestor reads it into the counters at
// startup and writes them back periodically and on shutdown, which makes the
// tally cumulative since `since_unix` instead of since boot. It is deliberately
// NOT in internal/dbschema: the server neither reads this table nor detects it,
// and putting it in AssertReady would make an old DB without the table fail the
// server's startup check for data the server never looks at.

const scopeMatchTotalsSchema = `
	CREATE TABLE IF NOT EXISTS scope_match_totals (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		since_unix INTEGER NOT NULL,
		unique_matches INTEGER NOT NULL,
		explicit_over_derived INTEGER NOT NULL,
		ambiguous INTEGER NOT NULL,
		none_matches INTEGER NOT NULL,
		updated_unix INTEGER NOT NULL
	)`

// LoadScopeMatchTotals seeds the in-process counters from the stored row so
// counting continues where the previous process stopped. On a DB that has no
// row yet it anchors `since` at now and stores the zeroes, so the very first
// window has a start time too — without it, a tally read after one restart
// would have no honest denominator.
func (s *Store) LoadScopeMatchTotals() error {
	var since, uniq, explicit, ambiguous, none int64
	err := s.db.QueryRow(`
		SELECT since_unix, unique_matches, explicit_over_derived, ambiguous, none_matches
		FROM scope_match_totals WHERE id = 1`).Scan(&since, &uniq, &explicit, &ambiguous, &none)
	if errors.Is(err, sql.ErrNoRows) {
		scopeMatchCounters.sinceUnix.Store(time.Now().Unix())
		return s.SaveScopeMatchTotals()
	}
	if err != nil {
		return err
	}
	scopeMatchCounters.sinceUnix.Store(since)
	scopeMatchCounters.unique.Store(uniq)
	scopeMatchCounters.explicitOverDerived.Store(explicit)
	scopeMatchCounters.ambiguous.Store(ambiguous)
	scopeMatchCounters.none.Store(none)
	return nil
}

// SaveScopeMatchTotals writes the current counters to the single row. Each
// atomic is loaded independently, so a save taken under live traffic can miss
// a match that lands mid-write; the next save picks it up. The tally is a
// long-window ratio, not an audit trail.
func (s *Store) SaveScopeMatchTotals() error {
	_, err := s.db.Exec(`
		INSERT INTO scope_match_totals
			(id, since_unix, unique_matches, explicit_over_derived, ambiguous, none_matches, updated_unix)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			since_unix = excluded.since_unix,
			unique_matches = excluded.unique_matches,
			explicit_over_derived = excluded.explicit_over_derived,
			ambiguous = excluded.ambiguous,
			none_matches = excluded.none_matches,
			updated_unix = excluded.updated_unix`,
		scopeMatchCounters.sinceUnix.Load(),
		scopeMatchCounters.unique.Load(),
		scopeMatchCounters.explicitOverDerived.Load(),
		scopeMatchCounters.ambiguous.Load(),
		scopeMatchCounters.none.Load(),
		time.Now().Unix())
	return err
}
