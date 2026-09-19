package main

import (
	"strings"
	"testing"
	"time"
)

// declaredRegionsCount returns the row count in node_declared_regions.
func declaredRegionsCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_declared_regions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// testTargetPK is a valid, exactly-64-hex-char target pubkey (the repeater
// asked), distinct from testCompanionPK (the reporting companion) so a test
// that mixes them up is caught. Must stay exactly 64 characters — that is
// what targetPubkeyRe requires, and a shortened value here would make the
// happy-path tests pass for the wrong reason.
const testTargetPK = "bb11223344556677889900aabbccddeeff00112233445566778899aabbccddaa"

// TestHandleClientRegionsWritesRow proves a well-formed /regions message
// writes exactly one node_declared_regions row and touches nothing else.
func TestHandleClientRegionsWritesRow(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"type": "REGIONS", "timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be", "be-vlg"},
		"truncated": false,
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	if n := declaredRegionsCount(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.RegionsCSV != "be,be-vlg" {
		t.Errorf("RegionsCSV = %q, want %q", cur.RegionsCSV, "be,be-vlg")
	}
	if cur.RxPubkey != testCompanionPK {
		t.Errorf("RxPubkey = %q, want the topic pubkey %q", cur.RxPubkey, testCompanionPK)
	}
	if cur.Lat == nil || cur.Lon == nil || *cur.Lat != 51.05 || *cur.Lon != 3.72 {
		t.Errorf("lat/lon = %v/%v, want 51.05/3.72", cur.Lat, cur.Lon)
	}
	if _, err := time.Parse(rxTimeMillisLayout, cur.ObservedAt); err != nil {
		t.Errorf("observed_at = %q, want rxTimeMillisLayout format: %v", cur.ObservedAt, err)
	}
}

// TestHandleClientRegionsInvalidTopicPubkeyDropped proves an invalid topic
// pubkey (the reporter's identity) is rejected before any write, the same
// guard used by every other client sub-topic handler.
func TestHandleClientRegionsInvalidTopicPubkeyDropped(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
	}
	handleClientRegions(s, &Config{}, "test", "NOT-HEX!", msg)

	if n := declaredRegionsCount(t, s); n != 0 {
		t.Fatalf("rows = %d, want 0 for an invalid topic pubkey", n)
	}
}

// TestHandleClientRegionsRejectsInvalidTarget proves `target` is validated
// before being trusted — it is payload data, not ACL-bound identity.
func TestHandleClientRegionsRejectsInvalidTarget(t *testing.T) {
	s := newTestStore(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"timestamp": "2026-08-18T10:00:00.000Z",
			"regions":   []interface{}{"be"},
		}
	}

	missing := base()
	handleClientRegions(s, &Config{}, "test", testCompanionPK, missing)

	invalid := base()
	invalid["target"] = "not-hex!"
	handleClientRegions(s, &Config{}, "test", testCompanionPK, invalid)

	empty := base()
	empty["target"] = ""
	handleClientRegions(s, &Config{}, "test", testCompanionPK, empty)

	if n := declaredRegionsCount(t, s); n != 0 {
		t.Fatalf("rows = %d, want 0 — all three target values must be rejected", n)
	}
}

// TestHandleClientRegionsRejectsWrongLengthTarget proves a target that is
// valid hex but the wrong length is rejected, not silently accepted as a
// prefix. Task 1's buildRegionsRequest throws unless the pubkey it sends is
// exactly 64 hex characters, so the app can only ever ask (and report back)
// a full pubkey — anything shorter is a malformed/forged payload. Without
// this check, a short target would still insert a row (clientPubkeyRe alone
// accepts 2-64 hex), and CurrentDeclaredRegions matches on the exact target
// string, so that row would never surface for the real node's queries — it
// would just accumulate silently as junk that looks like data.
func TestHandleClientRegionsRejectsWrongLengthTarget(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK[:8], // valid hex, but only 8 of the required 64 chars
		"regions":   []interface{}{"be"},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	if n := declaredRegionsCount(t, s); n != 0 {
		t.Fatalf("rows = %d, want 0 — a target shorter than 64 hex chars must be rejected", n)
	}
}

// TestHandleClientRegionsEmptyListIsValid proves an empty regions array is a
// genuine, storable observation ("nothing flood-allowed"), not treated as a
// malformed/missing field.
func TestHandleClientRegionsEmptyListIsValid(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	if n := declaredRegionsCount(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1 — an empty regions list is a valid observation", n)
	}
	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row for the empty-list observation")
	}
	if cur.RegionsCSV != "" {
		t.Errorf("RegionsCSV = %q, want empty string", cur.RegionsCSV)
	}
}

// TestHandleClientRegionsMalformedPayloadDropped proves a malformed `regions`
// field (absent, wrong type, or containing a non-string element) is dropped
// with a log — never half-stored as an empty list standing in for "no
// answer", which would blur "declared nothing" with "did not answer".
func TestHandleClientRegionsMalformedPayloadDropped(t *testing.T) {
	s := newTestStore(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"timestamp": "2026-08-18T10:00:00.000Z",
			"target":    testTargetPK,
		}
	}

	missing := base()
	handleClientRegions(s, &Config{}, "test", testCompanionPK, missing)

	wrongType := base()
	wrongType["regions"] = "be,be-vlg"
	handleClientRegions(s, &Config{}, "test", testCompanionPK, wrongType)

	badElement := base()
	badElement["regions"] = []interface{}{"be", 42.0}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, badElement)

	if n := declaredRegionsCount(t, s); n != 0 {
		t.Fatalf("rows = %d, want 0 — all three malformed payloads must be dropped, not half-stored", n)
	}
}

// TestHandleClientRegionsNoGPSStoresNullLatLon proves a declared-regions
// answer without a position is still meaningful (unlike a coverage point) and
// is stored with NULL lat/lon rather than dropped.
func TestHandleClientRegionsNoGPSStoresNullLatLon(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	if n := declaredRegionsCount(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1 — no GPS must not drop the observation", n)
	}
	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.Lat != nil || cur.Lon != nil {
		t.Errorf("lat/lon = %v/%v, want nil/nil when gps is absent", cur.Lat, cur.Lon)
	}
}

// TestHandleClientRegionsTruncatedCarriedThrough proves `truncated` is
// stored as reported (it is a hint from the app, not something the handler
// re-detects).
func TestHandleClientRegionsTruncatedCarriedThrough(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
		"truncated": true,
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || !cur.Truncated {
		t.Errorf("Truncated = %v, want true (carried through from payload)", cur)
	}
}

// TestHandleClientRegionsRepeaterClockRoundTrips proves repeater_clock is
// read from the payload (optInt64, same helper handleClientRfSample uses for
// this shape) and stored — it is the only signal in this stream that would
// reveal a repeater with a broken RTC, and was previously discarded even
// though the app publishes it and the docs show it in the payload example.
func TestHandleClientRegionsRepeaterClockRoundTrips(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp":      "2026-08-18T10:00:00.000Z",
		"target":         testTargetPK,
		"regions":        []interface{}{"be"},
		"repeater_clock": 1755518096.0,
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.RepeaterClock == nil || *cur.RepeaterClock != 1755518096 {
		t.Errorf("RepeaterClock = %v, want 1755518096", cur.RepeaterClock)
	}
}

// TestHandleClientRegionsOmittedRepeaterClockIsNull proves a payload that
// omits repeater_clock stores NULL, not 0 — absent is not zero, the same
// rule this codebase enforces for recv_errors (client_rf_sample.go).
func TestHandleClientRegionsOmittedRepeaterClockIsNull(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.RepeaterClock != nil {
		t.Errorf("RepeaterClock = %v, want nil when the field is absent from the payload", *cur.RepeaterClock)
	}
}

// TestHandleClientRegionsPosAccMRoundTrips proves gps.acc_m is read and
// stored the same way handleClientRfSample stores it as pos_acc_m — it was
// previously dropped even though the app publishes it.
func TestHandleClientRegionsPosAccMRoundTrips(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.5},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.PosAccM == nil || *cur.PosAccM != 8.5 {
		t.Errorf("PosAccM = %v, want 8.5", cur.PosAccM)
	}
}

// TestHandleClientRegionsOmittedPosAccMIsNull proves a payload whose gps
// object omits acc_m stores NULL, not 0.
func TestHandleClientRegionsOmittedPosAccMIsNull(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.PosAccM != nil {
		t.Errorf("PosAccM = %v, want nil when gps.acc_m is absent", *cur.PosAccM)
	}
}

// TestHandleClientRegionsRejectedPositionDropsPosAccM proves an accuracy
// radius is not stored when the coordinates it qualifies were rejected: a
// pos_acc_m beside a NULL lat/lon describes the precision of no position.
func TestHandleClientRegionsRejectedPositionDropsPosAccM(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-18T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"be"},
		"gps":       map[string]interface{}{"lat": 151.05, "lon": 3.72, "acc_m": 8.5},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.Lat != nil || cur.Lon != nil {
		t.Fatalf("Lat/Lon = %v/%v, want nil for an out-of-range latitude", cur.Lat, cur.Lon)
	}
	if cur.PosAccM != nil {
		t.Errorf("PosAccM = %v, want nil when the position was rejected", *cur.PosAccM)
	}
}

func TestDeclaredRegionsCurrentIsLatestObservation(t *testing.T) {
	s := newTestStore(t)
	ins := func(at, csv string) {
		if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
			Target: "bb11", RxPubkey: "aa11", ObservedAt: at, IngestedAt: "2026-08-18T00:00:00.000Z",
			RegionsCSV: csv,
		}); err != nil {
			t.Fatalf("insert %s: %v", at, err)
		}
	}
	// Deliberately inserted newest-first, so a query ordering by rowid or by
	// ingest order would pick the wrong one.
	ins("2026-08-18T10:00:00.000Z", "be,be-vlg,be-van")
	ins("2026-08-17T10:00:00.000Z", "be,be-vlg")

	cur, err := s.CurrentDeclaredRegions("bb11")
	if err != nil {
		t.Fatal(err)
	}
	if cur.RegionsCSV != "be,be-vlg,be-van" {
		t.Errorf("current = %q, want the 18th's list — a late-arriving older drive must not win", cur.RegionsCSV)
	}
}

func TestDeclaredRegionsIdempotent(t *testing.T) {
	s := newTestStore(t)
	o := &ClientDeclaredRegions{Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: "be"}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || !ins {
		t.Fatalf("first: ins=%v err=%v", ins, err)
	}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || ins {
		t.Fatalf("duplicate must be a no-op: ins=%v err=%v", ins, err)
	}
}

// TestDeclaredRegionsEmptyListRoundTrips proves an empty regions_csv is a
// valid, retrievable observation — "a repeater with nothing flood-allowed
// genuinely declares nothing, and that gets a row." Nothing in the insert or
// query path may special-case the empty string into a NULL or a dropped row;
// this test would fail against a later "helpful" `if regionsCSV == "" {
// return }` guard.
func TestDeclaredRegionsEmptyListRoundTrips(t *testing.T) {
	s := newTestStore(t)
	o := &ClientDeclaredRegions{Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: ""}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || !ins {
		t.Fatalf("insert: ins=%v err=%v", ins, err)
	}

	cur, err := s.CurrentDeclaredRegions("bb11")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row for the empty-list observation")
	}
	if cur.RegionsCSV != "" {
		t.Errorf("RegionsCSV = %q, want empty string — an empty list is a valid observation, not absence", cur.RegionsCSV)
	}
}

// TestDeclaredRegionsDifferentReportersPersistBoth pins the UNIQUE
// constraint's shape from the permitting side: two different companions
// (rx_pubkey) reporting the same target's regions at the same observed_at
// are two distinct observations and must both persist. Only a genuine
// duplicate delivery from the SAME reporter (same target, rx_pubkey AND
// observed_at — see TestDeclaredRegionsIdempotent) collapses. A later
// constraint change to (target, observed_at) would fail this test loudly
// instead of silently discarding the second reporter's reading.
func TestDeclaredRegionsDifferentReportersPersistBoth(t *testing.T) {
	s := newTestStore(t)
	at := "2026-08-18T10:00:00.000Z"
	for _, rx := range []string{"aa11", "aa22"} {
		if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
			Target: "bb11", RxPubkey: rx, ObservedAt: at, IngestedAt: at, RegionsCSV: "be",
		}); err != nil {
			t.Fatalf("insert %s: %v", rx, err)
		}
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_declared_regions WHERE target = ? AND observed_at = ?`, "bb11", at).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows = %d, want 2 — two different reporters observing the same target at the same time must both persist", n)
	}
}

// TestCurrentDeclaredRegionsNormalizesTargetCase proves CurrentDeclaredRegions
// lowercases its target argument before querying — mirroring every write
// path's normalization. Without it, a target pasted in uppercase (e.g. from a
// URL) silently returns zero rows and no error, exactly the sibling failure
// this table's read query must not repeat.
func TestCurrentDeclaredRegionsNormalizesTargetCase(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
		Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: "be",
	}); err != nil {
		t.Fatal(err)
	}

	cur, err := s.CurrentDeclaredRegions("BB11")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a hit for uppercase target BB11")
	}
	if cur.RegionsCSV != "be" {
		t.Errorf("current.RegionsCSV = %q, want %q", cur.RegionsCSV, "be")
	}
}

// TestPruneClientDeclaredRegionsUsesIndex pins the retention DELETE to an
// index seek on idx_ndr_prune rather than a full table scan.
func TestPruneClientDeclaredRegionsUsesIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM node_declared_regions WHERE observed_at < ?`, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_ndr_prune") {
		t.Fatalf("retention DELETE should use idx_ndr_prune, plan was:\n%s", plan)
	}
}

// TestPruneOldClientDeclaredRegions verifies the declared-regions reaper
// deletes rows older than the window and keeps recent ones, and that days=0
// disables it. It only exercises the days-to-cutoff arithmetic (a row well
// outside vs. well inside the window) — the millisecond-precision boundary
// that distinguishes a correct cutoff from an RFC3339 one is covered
// separately by TestPruneOldClientDeclaredRegionsAtMillisecondBoundary, which
// pins its own cutoff instant instead of deriving one from time.Now(); see
// that test's comment for why this one deliberately does not attempt it.
func TestPruneOldClientDeclaredRegions(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	recent := now.AddDate(0, 0, -1).Format(rxTimeMillisLayout)
	old := now.AddDate(0, 0, -40).Format(rxTimeMillisLayout)

	mk := func(observedAt string) *ClientDeclaredRegions {
		return &ClientDeclaredRegions{
			Target: "bb11", RxPubkey: "aa11", ObservedAt: observedAt, IngestedAt: observedAt,
			RegionsCSV: "be",
		}
	}
	if _, err := s.InsertClientDeclaredRegions(mk(recent)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientDeclaredRegions(mk(old)); err != nil {
		t.Fatal(err)
	}

	if n, _ := s.PruneOldClientDeclaredRegions(0); n != 0 {
		t.Fatalf("days=0 must be a no-op, got %d", n)
	}
	n, err := s.PruneOldClientDeclaredRegions(7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 old row pruned, got %d", n)
	}
	if got := declaredRegionsCount(t, s); got != 1 {
		t.Fatalf("expected 1 row remaining (recent), got %d", got)
	}
}

// TestPruneOldClientDeclaredRegionsAtMillisecondBoundary proves the pruner's
// cutoff comparison distinguishes a millisecond-precision stored value from
// an RFC3339 (whole-second) cutoff: observed_at is written via
// rxTimeMillisLayout, and SQLite compares it lexicographically as a string,
// so a cutoff formatted without the ".mmm" component sorts AFTER a
// chronologically-newer row that has one (since '.' is 0x2E and 'Z' is
// 0x5A) and would wrongly delete it.
//
// It calls the unexported pruneOldClientDeclaredRegionsAt seam with a cutoff
// instant the test controls, rather than deriving one from time.Now() the
// way PruneOldClientDeclaredRegions(days) does. That is deliberate: the
// obvious construction — take "now", subtract N days via AddDate (which
// shifts only the calendar date), then add a sub-second offset — inherits
// whatever sub-second component "now" happened to have. If that leftover
// fraction is e.g. 700ms, adding another 500ms rolls into the next second
// and the boundary row stops testing what it claims to (it would survive
// under the buggy RFC3339 cutoff too, so the assertion passes either way).
// Truncating "now" first avoids that but reintroduces the opposite failure:
// the boundary can then land BEFORE the real cutoff and the test fails
// against correct code. That is the same flake already fixed once on the
// sibling client_rf_samples table (~50% of runs, in one direction or the
// other depending on which fix is chosen). Pinning both the cutoff and the
// offset removes time.Now() from the test entirely, so the result no longer
// depends on wall-clock phase.
func TestPruneOldClientDeclaredRegionsAtMillisecondBoundary(t *testing.T) {
	s := newTestStore(t)
	cutoffInstant := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC).Add(400 * time.Millisecond)
	before := cutoffInstant.Add(-1 * time.Millisecond).Format(rxTimeMillisLayout)
	boundary := cutoffInstant.Add(1 * time.Millisecond).Format(rxTimeMillisLayout)

	mk := func(observedAt string) *ClientDeclaredRegions {
		return &ClientDeclaredRegions{
			Target: "bb11", RxPubkey: "aa11", ObservedAt: observedAt, IngestedAt: observedAt,
			RegionsCSV: "be",
		}
	}
	if _, err := s.InsertClientDeclaredRegions(mk(before)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertClientDeclaredRegions(mk(boundary)); err != nil {
		t.Fatal(err)
	}

	n, err := s.pruneOldClientDeclaredRegionsAt(cutoffInstant)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row pruned (1ms before the cutoff), got %d", n)
	}
	var boundarySurvived int
	s.db.QueryRow(`SELECT COUNT(*) FROM node_declared_regions WHERE observed_at = ?`, boundary).Scan(&boundarySurvived)
	if boundarySurvived != 1 {
		t.Fatalf("boundary row (1ms after a cutoff with a non-zero millisecond component) must survive; an RFC3339 (no-ms) cutoff would wrongly delete it because '.' < 'Z' lexicographically — got %d", boundarySurvived)
	}
}

// TestHandleClientRegionsTrimsBlockPadding proves the ingestor strips the AES
// block padding a repeater's reply carries, whatever the client version sent it.
// coredrive-rx trims it since v1.10.2, but a PWA runs its cached build until the
// user reloads and this instance controls none of the independent clients — the
// padding was observed arriving from a client 40 minutes AFTER that release.
// Stored verbatim it lands inside the LAST region name, so "be-vli\x00\x00" matches
// no observed scope and reads as a region declared but never forwarded.
func TestHandleClientRegionsTrimsBlockPadding(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-19T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"*", "be", "be-vli\x00\x00\x00"},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.RegionsCSV != "*,be,be-vli" {
		t.Errorf("regions_csv = %q, want %q", cur.RegionsCSV, "*,be,be-vli")
	}
}

// TestHandleClientRegionsPaddingOnlyEntryDropped: an entry that is nothing but
// padding is not a declared region and must not become an empty-named one.
func TestHandleClientRegionsPaddingOnlyEntryDropped(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"timestamp": "2026-08-19T10:00:00.000Z",
		"target":    testTargetPK,
		"regions":   []interface{}{"*", "\x00\x00\x00\x00"},
	}
	handleClientRegions(s, &Config{}, "test", testCompanionPK, msg)

	cur, err := s.CurrentDeclaredRegions(testTargetPK)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row")
	}
	if cur.RegionsCSV != "*" {
		t.Errorf("regions_csv = %q, want %q", cur.RegionsCSV, "*")
	}
}
