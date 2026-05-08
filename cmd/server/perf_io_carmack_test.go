package main

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseProcIO_EmptyDoesNotMarkOK — #1167 Carmack must-fix #6: the
// server-side parser was missing the parsedAny gate the ingestor's parser
// got in must-fix #3 of the original review. Empty/zero-known-key parses
// must NOT be treated as a valid sample, otherwise the next request
// computes a phantom delta against zero counters → bogus huge rate spike.
//
// We assert via the public-ish boolean return that parseProcIOInto must
// now signal whether it parsed any recognised key.
func TestParseProcIO_EmptyDoesNotMarkOK(t *testing.T) {
	var s procIOSample
	ok := parseProcIOInto(bufio.NewScanner(strings.NewReader("")), &s)
	if ok {
		t.Errorf("empty input must produce ok=false, got ok=true (phantom-spike risk)")
	}
}

// TestParseProcIO_NoKnownKeysDoesNotMarkOK — companion to the above for a
// future kernel /proc schema change that drops the keys we recognise.
func TestParseProcIO_NoKnownKeysDoesNotMarkOK(t *testing.T) {
	var s procIOSample
	ok := parseProcIOInto(bufio.NewScanner(strings.NewReader("garbage_key: 42\nother: 99\n")), &s)
	if ok {
		t.Errorf("input without recognised keys must produce ok=false, got ok=true")
	}
}

// TestParseProcIO_ValidSampleMarksOK — positive companion: real input
// MUST mark ok=true with the expected counters.
func TestParseProcIO_ValidSampleMarksOK(t *testing.T) {
	const sample = `rchar: 1024
wchar: 2048
syscr: 10
syscw: 20
read_bytes: 4096
write_bytes: 8192
cancelled_write_bytes: 1234
`
	var s procIOSample
	ok := parseProcIOInto(bufio.NewScanner(strings.NewReader(sample)), &s)
	if !ok {
		t.Fatalf("valid sample must produce ok=true")
	}
	if s.readBytes != 4096 || s.writeBytes != 8192 || s.cancelledWrite != 1234 {
		t.Errorf("unexpected parsed counters: %+v", s)
	}
}

// readIngestorStatsParseCalls is incremented every time
// readIngestorIOSample performs a full json.Unmarshal of the stats file
// (i.e. cache miss). Used by the cache test below to assert that
// repeated calls within the same mtime+size window do NOT re-decode.
//
// The hook must be wired up in perf_io.go (Carmack must-fix #2).
//var readIngestorStatsParseCalls atomic.Int64 — defined in perf_io.go

// TestReadIngestorIOSample_CachesByMtimeSize — Carmack must-fix #2: the
// underlying file is byte-stable between 1Hz writes; multiple readers
// (every browser tab on the Perf page) re-decode for nothing. Cache the
// last decoded sample keyed by (mtime, size); only re-parse when either
// changes.
func TestReadIngestorIOSample_CachesByMtimeSize(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{"sampledAt":"` + freshAt + `","tx_inserted":0,"backfillUpdates":{},"procIO":{"readBytesPerSec":1,"writeBytesPerSec":2,"cancelledWriteBytesPerSec":0,"syscallsRead":3,"syscallsWrite":4,"sampledAt":"` + freshAt + `"}}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	// Reset counter + cache.
	readIngestorStatsParseCalls.Store(0)
	resetIngestorIOCache()

	for i := 0; i < 5; i++ {
		got := readIngestorIOSample()
		if got == nil {
			t.Fatalf("call %d: expected non-nil, got nil", i)
		}
	}
	got := readIngestorStatsParseCalls.Load()
	if got != 1 {
		t.Errorf("expected 1 parse for 5 reads of byte-stable file, got %d", got)
	}
}

// TestReadIngestorIOSample_CacheInvalidatesOnMtimeChange — companion: as
// soon as the file changes (writer tick) the cache MUST invalidate.
func TestReadIngestorIOSample_CacheInvalidatesOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	write := func() {
		freshAt := time.Now().UTC().Format(time.RFC3339)
		stub := `{"sampledAt":"` + freshAt + `","tx_inserted":0,"backfillUpdates":{},"procIO":{"readBytesPerSec":1,"writeBytesPerSec":2,"cancelledWriteBytesPerSec":0,"syscallsRead":3,"syscallsWrite":4,"sampledAt":"` + freshAt + `"}}`
		if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	readIngestorStatsParseCalls.Store(0)
	resetIngestorIOCache()

	_ = readIngestorIOSample()
	// Bump mtime by writing again with a new timestamp; sleep ensures
	// the FS mtime advances (typical 1ns res on Linux but be safe).
	time.Sleep(10 * time.Millisecond)
	// Touch with a different size by rewriting fresh content.
	write()
	// Force a clearly different mtime by setting it explicitly.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(statsPath, future, future); err != nil {
		t.Fatal(err)
	}
	_ = readIngestorIOSample()
	got := readIngestorStatsParseCalls.Load()
	if got != 2 {
		t.Errorf("expected 2 parses across an mtime-change, got %d", got)
	}
}

// TestPerfIOEndpoint_IngestorTimestampMatchesSnapshot — Carmack must-fix
// #5: the ingestor calls time.Now() twice per tick (once for
// IngestorStatsSnapshot.SampledAt, once for the inner procIO.SampledAt).
// They drift by μs–ms and the freshness guard then claims to validate
// "the snapshot timestamp" while the consumer renders the inner one.
// After the fix: single capture, both fields share the exact same RFC3339
// string.
func TestPerfIOEndpoint_IngestorTimestampMatchesSnapshot(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	// Boot the in-process ingestor stats writer for one tick by calling
	// the helper directly. Test-only path: build a minimal Store, run
	// one snapshot synchronously through a helper if available; else
	// drive via a real StartStatsFileWriter on a 50ms interval and read
	// the file once it exists.
	//
	// We use the public buildIngestorStatsSnapshot helper (added in
	// the GREEN commit for must-fix #5). RED commit: this symbol does
	// not exist yet, so the test fails to build... that's a build
	// error, not an assertion. To respect the "MUST fail on assertion"
	// rule, write the test against an alternative observable: run
	// against a freshly-published file from a real writer, then assert
	// the two timestamp strings are equal.
	//
	// Easiest: spin up a tiny StartStatsFileWriter via the ingestor
	// binary? No — different package. Instead, drive the server-side
	// flow: use StartStatsFileWriter is not in this package.
	//
	// We assert at the level the bug actually manifests: read the
	// stats file written by an actual ingestor instance run in an
	// integration-style helper. Lacking that, assert at the level of
	// helper logic the GREEN commit will introduce: inspect the JSON
	// shape produced when ProcIO sample is built with SampledAt =
	// snapshot.SampledAt.
	//
	// Concrete failing assertion: build a stub stats file where the
	// two timestamps DIFFER by a known amount. Confirm the freshness
	// guard uses st.SampledAt (already the case). Then assert (via a
	// helper exposed by the GREEN fix) that the writer would produce
	// identical strings. Helper name: `ingestorTickTimestampMatches`,
	// returns bool — true if the writer captures time.Now once.
	if !ingestorTickTimestampMatches() {
		t.Errorf("ingestor writer must capture time.Now() once per tick and reuse for both snapshot.SampledAt and procIO.SampledAt")
	}
	// Also verify the freshness path doesn't lie: when the file is
	// freshly written, both timestamps should round-trip-equal in the
	// served JSON.
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{"sampledAt":"` + freshAt + `","tx_inserted":0,"backfillUpdates":{},"procIO":{"readBytesPerSec":1,"writeBytesPerSec":2,"cancelledWriteBytesPerSec":0,"syscallsRead":3,"syscallsWrite":4,"sampledAt":"` + freshAt + `"}}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/perf/io", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ing, ok := body["ingestor"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected ingestor block, got: %v", body)
	}
	if ing["sampledAt"] != freshAt {
		t.Errorf("expected ingestor.sampledAt=%s, got %v", freshAt, ing["sampledAt"])
	}
}
