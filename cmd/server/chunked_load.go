package main

// Chunked startup load + early HTTP readiness for issue #1009.
//
// Design:
//   * LoadChunked paginates transmissions in bounded chunks of
//     `chunkSize` (default 10000 via Config.DBLoadChunkSize). After the
//     first chunk is merged into the store, FirstChunkReady is closed.
//     Memory-capped stores prioritize newest rows; unlimited stores use IDs.
//     main.go binds the HTTP listener on that signal and serves
//     partial data while remaining chunks stream in the background.
//   * RunStartupLoad is the orchestrator: it runs LoadChunked
//     synchronously, then on success runs loadBackgroundChunks
//     synchronously so s.oldestLoaded is guaranteed set before the
//     background loader reads it (#1809). main.go typically invokes
//     RunStartupLoad inside its own goroutine and waits on
//     FirstChunkReady() in parallel to bind the HTTP listener.
//   * loadStatusMiddleware stamps X-CoreScope-Load-Status on every
//     response: "loading; progress=<rows>" until LoadComplete()
//     reports true, then "ready". Dashboards and probes can read the
//     header without parsing JSON.
//   * OnChunkLoaded registers a per-chunk callback for progress
//     logging / tests.
//
// Concurrency: each chunk acquires s.mu.Lock() ONLY while merging the
// chunk's rows into store-shared maps. Database reads run without the store lock so
// HTTP handlers (which take s.mu.RLock) stay responsive.

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

// dbLoadConfig is the server-package alias for dbconfig.LoadConfig (#1009).
type dbLoadConfig = dbconfig.LoadConfig

// invariantViolation is the production-time handler for #1809-class
// invariant failures (see loadBackgroundChunks). In prod it logs a
// FATAL line and calls os.Exit(1) via log.Fatalf — a clean shutdown
// with no goroutine-stack noise. Tests override it to panic so they
// can recover() and assert the invariant message without crashing the
// test runner. adv #6 (PR #1811).
var invariantViolation = func(msg string) {
	log.Fatalf("invariant violation: %s", msg)
}

// DBLoadChunkSize returns the configured chunk size for chunked
// startup load (config: db.load.chunkSize), or 10000 default (#1009).
func (c *Config) DBLoadChunkSize() int {
	return c.DB.GetLoadChunkSize()
}

// chunkedLoadState holds the runtime gates for LoadChunked. It lives
// on PacketStore via embedded fields — see store.go additions in the
// same commit.

// FirstChunkReady returns a channel closed once the first chunk has
// been merged into the store, signalling the HTTP listener can bind.
func (s *PacketStore) FirstChunkReady() <-chan struct{} {
	s.chunkedLoadInit()
	return s.firstChunkReady
}

// LoadComplete reports whether LoadChunked has finished all chunks.
func (s *PacketStore) LoadComplete() bool {
	return s.loadComplete.Load()
}

// LoadProgress reports the number of transmission rows processed by
// the in-flight (or completed) LoadChunked call.
func (s *PacketStore) LoadProgress() int64 {
	return s.loadProgressRows.Load()
}

// OnChunkLoaded registers a callback fired once per chunk after that
// chunk has been merged into the store. The callback receives the
// number of transmission rows in that chunk and the running total.
// Multiple registrations chain.
func (s *PacketStore) OnChunkLoaded(fn func(rowsThisChunk, totalRows int)) {
	s.chunkedLoadInit()
	s.chunkCBMu.Lock()
	defer s.chunkCBMu.Unlock()
	s.chunkCallbacks = append(s.chunkCallbacks, fn)
}

// chunkedLoadInit lazily initialises the readiness channel + callback
// list under a mutex so concurrent first callers don't race.
func (s *PacketStore) chunkedLoadInit() {
	s.chunkInitOnce.Do(func() {
		s.firstChunkReady = make(chan struct{})
		s.startupLoadDone = make(chan struct{})
	})
}

// StartupLoadDone returns a channel closed once RunStartupLoad has
// returned: LoadChunked AND the background fill loader are finished,
// whether they succeeded or not. Nothing more is loaded from the database
// after it closes. LoadComplete() is not a substitute: it flips at the
// end of the hot window, before the background fill starts.
func (s *PacketStore) StartupLoadDone() <-chan struct{} {
	s.chunkedLoadInit()
	return s.startupLoadDone
}

func (s *PacketStore) signalStartupLoadDone() {
	s.chunkedLoadInit()
	if !s.startupLoadSignaled.CompareAndSwap(false, true) {
		return
	}
	// The analytics recomputes this signal triggers read these TTL
	// caches. Drop what was computed from the partial store so they see
	// the loaded data now rather than after the TTL.
	s.hashSizeInfoMu.Lock()
	s.hashSizeInfoCache = nil
	s.hashSizeInfoMu.Unlock()
	s.clockSkew.Invalidate()
	close(s.startupLoadDone)
}

func (s *PacketStore) signalFirstChunk() {
	if s.firstChunkSignaled.CompareAndSwap(false, true) {
		close(s.firstChunkReady)
	}
}

func (s *PacketStore) fireChunkCallbacks(rowsThisChunk, totalRows int) {
	s.chunkCBMu.Lock()
	cbs := append([]func(int, int){}, s.chunkCallbacks...)
	s.chunkCBMu.Unlock()
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[store] OnChunkLoaded callback panic: %v", r)
				}
			}()
			cb(rowsThisChunk, totalRows)
		}()
	}
}

// RunStartupLoad orchestrates the startup load sequence:
//  1. run LoadChunked synchronously (FirstChunkReady is signaled
//     internally so callers waiting on it in parallel can bind HTTP)
//  2. on success, run loadBackgroundChunks synchronously so
//     s.oldestLoaded is guaranteed set before the bg loader reads
//     it (#1809).
//
// chunkSize=0 uses the LoadChunked default. The function blocks until
// LoadChunked AND any background loader have finished. Callers that
// want to bind the HTTP listener at FirstChunkReady should run this
// in a goroutine and wait on FirstChunkReady() themselves.
//
// SYNCHRONOUS-CALL DEADLOCK WARNING (adv #10): callers that block on
// FirstChunkReady() from the SAME goroutine that calls RunStartupLoad
// will deadlock — LoadChunked only closes firstChunkReady from within
// this call, and RunStartupLoad does not return until ALL chunks
// (including the background loader) have finished. main.go's pattern
// is correct: spawn RunStartupLoad in a goroutine and wait on
// FirstChunkReady() on the parent stack. Do not call RunStartupLoad
// inline.
//
// SINGLE-CALL INVARIANT (adv #7): RunStartupLoad is invoked exactly
// once per process from main.go boot. Tests open a fresh store per
// test. Re-entry would observe a stale backgroundLoadErr from a
// previous failed call until the new LoadChunked or
// loadBackgroundChunks rewrote it. We clear backgroundLoadErr on
// entry to make repeat invocations in tests safe; a panic on
// reentrant concurrent calls is not added because the production
// invariant (one call per boot) is already documented above
// LoadChunked.
//
// Steady-state contracts post-return:
//   - StartupLoadDone() is closed, on every path below.
//   - LoadChunked error: backgroundLoadFailed=true, backgroundLoadDone
//     is also set true (terminal observable state — see dij #1).
//     backgroundLoadErr non-empty. Returns the error.
//   - hotStartupHours == 0: backgroundLoadDone=true, failed=false
//     (no background work was needed). Coverage is computed against
//     row count so /api/perf does not report ratio=0.0 (dij #2).
//   - hotStartupHours > 0 success: terminal state is whatever
//     loadBackgroundChunks set (done=true on full coverage or the configured
//     memory limit; failed=true on unexpected partial coverage/chunk errors).
//
// Issue #1809 root cause: previously main.go spawned loadBackgroundChunks
// at FirstChunkReady while LoadChunked was still merging the remainder
// of the hot window. s.oldestLoaded is only assigned at the end of
// LoadChunked, so the bg loader read "" and bailed → coverage gate
// trips → backgroundLoadFailed=true. Running the bg loader after
// LoadChunked returns preserves the FirstChunkReady HTTP-bind
// parallelism while ensuring oldestLoaded has a valid floor when the
// bg loader starts.
func (s *PacketStore) RunStartupLoad(chunkSize int) error {
	defer s.signalStartupLoadDone()
	// Clear any stale error from a previous invocation (single-call
	// invariant — see godoc above). Production never re-enters but
	// test fixtures may construct fresh stores that share no state;
	// keeping this explicit avoids surprising "ghost error" reads
	// across test runs that share a store.
	s.bgErrMu.Lock()
	s.backgroundLoadErr = ""
	s.bgErrMu.Unlock()

	if err := s.LoadChunked(chunkSize); err != nil {
		// Pick a terminal state on the error path so /api/healthz +
		// backgroundLoadComplete don't stay undefined forever. Both
		// done AND failed are set true: the LoadChunked read-protocol
		// contract documented in store.go says `done` is the primary
		// observable terminal signal and `failed` qualifies it (read
		// done first, then check failed). See dij #1.
		s.bgErrMu.Lock()
		s.backgroundLoadErr = fmt.Sprintf("LoadChunked failed: %v", err)
		s.bgErrMu.Unlock()
		s.backgroundLoadFailed.Store(true)
		s.backgroundLoadDone.Store(true)
		return err
	}
	if s.hotStartupHours <= 0 {
		// No bg work required → terminal steady state is done=true,
		// failed=false. Without this the healthz probe would see
		// backgroundLoadComplete=false indefinitely. dij #2: compute
		// coverage so /api/perf doesn't report ratio=0 on the
		// "no-work" path — empty DB is 1.0, otherwise everything in
		// the DB is what we already loaded into memory.
		s.mu.RLock()
		loadedCount := int64(len(s.packets))
		s.mu.RUnlock()
		var totalInDB int64
		if err := s.db.conn.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&totalInDB); err != nil {
			totalInDB = -1
		}
		var ratio float64
		switch {
		case totalInDB <= 0:
			// Empty DB (0) or count failed (-1) → treat as fully covered.
			ratio = 1.0
		case loadedCount >= totalInDB:
			ratio = 1.0
		default:
			ratio = float64(loadedCount) / float64(totalInDB)
		}
		s.bgErrMu.Lock()
		s.loadCoverageRatio = ratio
		s.bgErrMu.Unlock()
		log.Printf("[store] RunStartupLoad: hotStartupHours=0 — background fill loader SKIPPED (coverage=%.1f%%, loaded=%d, totalInDB=%d)",
			ratio*100, loadedCount, totalInDB)
		s.backgroundLoadDone.Store(true)
		s.backgroundLoadProgress.Store(100)
		return nil
	}
	// INFO signal between LoadChunked completion and the bg loader
	// kick-off. The post-mortem of #1809 needed exactly this line to
	// confirm the bg loader actually started after oldestLoaded was set.
	log.Printf("[store] LoadChunked complete (oldestLoaded=%q) — starting background fill loader (retentionHours=%gh, hotStartupHours=%gh)",
		s.oldestLoaded, s.retentionHours, s.hotStartupHours)
	s.loadBackgroundChunks()
	return nil
}

// LoadChunked streams transmissions + observations from PostgreSQL into
// the in-memory store in bounded chunks of `chunkSize` rows. Pass
// 0 to use the default (10000).
//
// After the first chunk is merged, FirstChunkReady is closed and the
// HTTP listener may bind. Remaining chunks stream while handlers run
// against partially-populated data; loadStatusMiddleware advertises
// loading status until LoadComplete() returns true.
//
// Re-entrancy: LoadChunked is NOT safe to call concurrently with
// itself on the same PacketStore — it resets loadComplete /
// loadProgressRows and mutates store-shared maps under s.mu. In
// production it is invoked exactly once from main.go boot. Tests that
// open a fresh store per test are also safe. If a future caller needs
// repeat or concurrent loads, add a top-level mutex first.
func (s *PacketStore) LoadChunked(chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = 10000
	}
	// Startup-ordering invariant (PR #1643 R1 munger #2). Mirror the
	// guard in Load() so the production async path also fast-fails when
	// neighbor_edges has rows but the graph is missing. See Load() for
	// the full rationale.
	if neighborEdgesTableExists(s.db.conn) && s.graph.Load() == nil {
		panic("packet store LoadChunked(): neighbor_edges table has rows but s.graph is nil — graph must be loaded before packet load (see main.go #1643 invariant)")
	}
	s.chunkedLoadInit()
	// Reset state for repeat calls in tests.
	s.loadComplete.Store(false)
	s.loadProgressRows.Store(0)

	// On any return — error OR success — unblock listeners that gate on
	// the readiness signal so an empty/failed DB does not deadlock the
	// caller. Note: loadComplete is set on the success path only (see
	// the end of this function) so probes do NOT see ready=true after a
	// failed load.
	defer s.signalFirstChunk()

	t0 := time.Now()

	// Build the retention/memory filter the legacy Load() uses so
	// behavior is preserved when callers migrate from Load → LoadChunked.
	// Built against the `t2` alias used inside the chunk subquery so we
	// don't need brittle post-hoc string rewrites.
	var loadConditions []string
	hotCutoffHours := s.retentionHours
	if s.hotStartupHours > 0 {
		hotCutoffHours = s.hotStartupHours
	}
	var hotCutoffStr string
	var hotCutoffUnix int64
	if hotCutoffHours > 0 {
		hotCutoffT := time.Now().UTC().Add(-time.Duration(hotCutoffHours * float64(time.Hour)))
		hotCutoffStr = hotCutoffT.Format(time.RFC3339)
		hotCutoffUnix = hotCutoffT.Unix()
		_ = hotCutoffUnix
		// #1690: filter on the denormalized last_seen (effective recency)
		// rather than first_seen, so long-lived hashes with recent traffic
		// load on cold-start. first_seen is set once and never updated, so
		// the prior `t2.first_seen >= cutoff` query loaded only hashes
		// first-inserted within the window (0.3% of DB on prod).
		//
		// Test/legacy DBs without the column (schema-detected as
		// hasLastSeen=false) fall back to the legacy first_seen axis to
		// keep existing fixtures green. Production goes through
		// dbschema.AssertReady which fail-fasts when the column is
		// missing — so the fallback is only ever hit in tests.
		if s.db.hasLastSeen {
			loadConditions = append(loadConditions, fmt.Sprintf("t2.last_seen >= %d", hotCutoffUnix))
		} else {
			loadConditions = append(loadConditions, fmt.Sprintf("t2.first_seen >= '%s'", hotCutoffStr))
		}
	}

	// COUNT honours the same retention/hot-startup filter the chunk
	// loop applies, so the logged "DB total" matches the rows the
	// loop will actually walk. Use a `t2` alias to share the WHERE
	// builder above. If the count fails (e.g. empty DB, locked WAL),
	// fall through with -1 — it's only used for the post-load log line.
	totalInDB := -1
	countSQL := "SELECT COUNT(*) FROM transmissions t2"
	if len(loadConditions) > 0 {
		countSQL += " WHERE " + strings.Join(loadConditions, " AND ")
	}
	if err := s.db.conn.QueryRow(countSQL).Scan(&totalInDB); err != nil {
		totalInDB = -1
	}

	// Polling starts after the first chunk. Capture the complete database's
	// high-water marks before loading a subset, so older omitted rows are not
	// mistaken for new ingest and replayed into the cache immediately.
	if s.maxMemoryMB > 0 {
		var maxTxID, maxObsID int
		if err := s.db.conn.QueryRow(`SELECT
			(SELECT COALESCE(MAX(id), 0) FROM transmissions),
			(SELECT COALESCE(MAX(id), 0) FROM observations)`).Scan(&maxTxID, &maxObsID); err != nil {
			return fmt.Errorf("load watermarks: %w", err)
		}
		s.mu.Lock()
		s.maxTxID, s.maxObsID = maxTxID, maxObsID
		s.mu.Unlock()
	}
	var from time.Time
	if hotCutoffStr != "" {
		from, _ = time.Parse(time.RFC3339, hotCutoffStr)
	}
	chunkIdx := 0
	totalLoaded, err := s.loadPacketRange(from, time.Time{}, chunkSize, func(count, total int) {
		chunkIdx++
		s.loadProgressRows.Store(int64(total))
		s.signalFirstChunk()
		s.fireChunkCallbacks(count, total)
	})
	if err != nil {
		return fmt.Errorf("chunk %d: %w", chunkIdx, err)
	}

	// Each chunk publishes complete packets and indexes atomically in
	// first_seen order. Build the derived analytics indexes once at the end.
	s.mu.Lock()
	s.buildSubpathIndex()
	s.buildPathHopIndex()
	s.buildDistanceIndex()
	if !s.startupMemoryLimited {
		if s.hotStartupHours > 0 {
			s.oldestLoaded = hotCutoffStr
		} else if len(s.packets) > 0 {
			s.oldestLoaded = s.packets[0].FirstSeen
		}
	}
	s.loaded = true
	s.mu.Unlock()

	// #1009 / PR #1596: flip the subpath + pathHop ready flags now that
	// the chunk loader has built both indexes synchronously above.
	// Without this, WaitIndexesReady (used by
	// StartRepeaterEnrichmentRecomputer at boot) blocks for up to
	// repeaterEnrichmentPrewarmWait (60s), delaying HTTP listener bind
	// past CI's 30s /api/healthz deadline.
	s.markIndexesReadySync()

	elapsed := time.Since(t0)
	log.Printf("[store] LoadChunked: %d transmissions (%d observations) across %d chunk(s) in %v (chunkSize=%d, DB total=%d)",
		totalLoaded, s.totalObs, chunkIdx, elapsed, chunkSize, totalInDB)
	s.loadMultibyteCapFromDB()
	// Mark complete on the success path only — see the function-level
	// defer above for why this is NOT in a deferred call. Probes that
	// read LoadComplete()==true after a failed load would otherwise
	// see ready=true for a half-loaded store.
	s.loadComplete.Store(true)
	return nil
}

// loadStatusMiddleware sets X-CoreScope-Load-Status on every response.
// While LoadChunked is in flight the header reports
// "loading; progress=<rows>"; after completion it reports "ready".
// The header is set BEFORE calling the next handler so probes can
// observe it on any response (including streaming bodies).
func loadStatusMiddleware(s *PacketStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s != nil && s.LoadComplete() {
			w.Header().Set("X-CoreScope-Load-Status", "ready")
		} else if s != nil {
			w.Header().Set("X-CoreScope-Load-Status",
				fmt.Sprintf("loading; progress=%d", s.LoadProgress()))
		} else {
			w.Header().Set("X-CoreScope-Load-Status", "loading")
		}
		next.ServeHTTP(w, r)
	})
}

// --- runtime state stitched into PacketStore via store_chunked.go ---

// Forward declarations of the new PacketStore fields used above. The
// actual struct fields live in store.go; placing them here as a
// reminder keeps the chunked-load surface easy to audit.
var _ = sync.Once{}
var _ atomic.Bool
