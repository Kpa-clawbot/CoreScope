// Issue #1008: background-deferred subpath + pathHop index builds.
//
// Pattern mirrors the distance index (#1011) — but where distance is
// fully lazy (built on first request), these two indexes are kicked off
// eagerly by Load() in a background goroutine so HTTP becomes ready
// immediately while the indexes finish populating.
//
// Concurrency model:
//
//   - subpathReady / pathHopReady are atomic.Bool flags written exactly
//     once by the background builder (false → true) and never reset
//     thereafter. Handlers read them via SubpathIndexReady() /
//     PathHopIndexReady() before touching s.spIndex / s.spTxIndex /
//     s.byPathHop. While a flag is false, the handler responds 503 +
//     Retry-After: 5.
//
//   - The builder itself acquires s.mu.Lock() and calls the existing
//     buildSubpathIndex() / buildPathHopIndex() methods. Those methods
//     replace s.spIndex / s.spTxIndex / s.byPathHop with freshly-
//     allocated maps under the write lock, so handlers that respect the
//     ready gate never observe a partial map: they either see "not
//     ready" (and skip the read) or "ready" (and the maps are fully
//     populated, the lock release on the build completion happens-
//     before the atomic store of true, per Go memory model — see Go
//     spec "channels and other synchronization primitives": an atomic
//     store after an unlock is observed by readers that acquire the
//     atomic and then the read lock).
//
//   - Ingest-side incremental updates in StoreNewTransmissions /
//     pruning / hash-collision paths continue to write s.spIndex /
//     s.spTxIndex / s.byPathHop directly under s.mu.Lock(). Because
//     the builder also runs under s.mu.Lock() and the builder
//     overwrites whatever is there, the brief window between Load()
//     returning and the goroutine acquiring s.mu means any
//     concurrent ingest writes will be overwritten by the build —
//     this matches the prior behavior where ingest could not start
//     until Load() released s.mu, so in practice ingest does not
//     run during the build window. Documenting this rather than
//     adding a separate gate: the existing main.go boot sequence
//     does not start ingest goroutines until after store.Load()
//     and graph init complete.
package main

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// writeIndexLoading503 emits the standard 503 response used by handlers
// that depend on a not-yet-built index (#1008). Body shape matches the
// triage spec: {"error":"index loading","retryAfter":5}. The Retry-After
// header is also set so well-behaved clients back off automatically.
func writeIndexLoading503(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"index loading","retryAfter":5}`))
}

// subpathReady and pathHopReady gate handler reads of the corresponding
// indexes. Stored as a struct field via the *PacketStore methods below;
// since Go doesn't allow adding fields from a separate file without
// touching the struct, we use package-level sync.Map keyed by the store
// pointer? No — we add real fields on PacketStore in store.go. These
// helpers operate on those fields.

// SubpathIndexReady reports whether the background subpath index build
// kicked off by Load() has completed (#1008). Until this returns true,
// callers must NOT read s.spIndex / s.spTxIndex.
func (s *PacketStore) SubpathIndexReady() bool {
	return s.subpathReady.Load()
}

// PathHopIndexReady reports whether the background path-hop index build
// kicked off by Load() has completed (#1008). Until this returns true,
// callers must NOT read s.byPathHop.
func (s *PacketStore) PathHopIndexReady() bool {
	return s.pathHopReady.Load()
}

// startBackgroundIndexBuilds is called from Load() after s.loaded=true
// to populate the subpath + path-hop indexes off the critical path
// (#1008). It returns immediately; the work runs in a background
// goroutine that acquires s.mu.Lock() to install the maps and then
// sets the atomic ready flags.
//
// At Cascadia scale (~5M observations) this previously blocked HTTP
// readiness ~60s inside Load() under s.mu.
func (s *PacketStore) startBackgroundIndexBuilds() {
	go func() {
		t0 := time.Now()
		s.mu.Lock()
		s.buildSubpathIndex()
		s.mu.Unlock()
		s.subpathReady.Store(true)
		subElapsed := time.Since(t0)

		t1 := time.Now()
		s.mu.Lock()
		s.buildPathHopIndex()
		s.mu.Unlock()
		s.pathHopReady.Store(true)
		phElapsed := time.Since(t1)

		log.Printf("[startup] index build complete: subpath (%s), pathHop (%s)",
			subElapsed.Round(time.Millisecond), phElapsed.Round(time.Millisecond))
	}()
}

// markIndexesReadySync is a helper for code paths (e.g. background
// chunk loader, tests) that build the indexes synchronously and want to
// flip the ready flags in one shot.
func (s *PacketStore) markIndexesReadySync() {
	s.subpathReady.Store(true)
	s.pathHopReady.Store(true)
}

// WaitIndexesReady blocks until both background indexes built by
// startBackgroundIndexBuilds() report ready, or the deadline expires.
// Returns true if both flipped in time. Intended for tests that read
// s.spIndex / s.spTxIndex / s.byPathHop directly after Load(); production
// code paths gate via SubpathIndexReady() / PathHopIndexReady() and
// respond 503 + Retry-After to clients instead of blocking.
func (s *PacketStore) WaitIndexesReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.SubpathIndexReady() && s.PathHopIndexReady() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return s.SubpathIndexReady() && s.PathHopIndexReady()
}

// Compile-time guarantee that sync/atomic is wired correctly for the
// ready flags on PacketStore.
var _ atomic.Bool
