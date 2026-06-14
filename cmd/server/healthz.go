package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// readiness tracks whether background init goroutines have completed.
// Set to 1 once store.Load, pickBestObservation, and neighbor graph build are done.
var readiness atomic.Int32

// handleHealthz returns 200 when the server is ready to serve queries,
// or 503 while background initialization is still running.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if readiness.Load() == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ready":  false,
			"reason": "loading",
		})
		return
	}

	var loadedTx, loadedObs int
	if s.store != nil {
		s.store.mu.RLock()
		loadedTx = len(s.store.packets)
		for _, p := range s.store.packets {
			loadedObs += len(p.Observations)
		}
		s.store.mu.RUnlock()
	}

	// #1143 (M2): expose from_pubkey backfill progress so operators can
	// see whether the legacy ADVERT backfill is still running. NULL rows
	// produce empty attribution results during the in-flight window.
	// Cycle-3 m2c: snapshot all three fields under a single read lock so
	// /api/healthz never observes a torn state (e.g. done=true with
	// processed<total).
	bfTotal, bfProcessed, bfDone := fromPubkeyBackfillSnapshot()
	w.WriteHeader(http.StatusOK)
	resp := map[string]interface{}{
		"ready":     true,
		"loadedTx":  loadedTx,
		"loadedObs": loadedObs,
		"from_pubkey_backfill": map[string]interface{}{
			"total":     bfTotal,
			"processed": bfProcessed,
			"done":      bfDone,
		},
	}
	// #1724: surface ingestor-side async migration progress so the
	// warm-up banner (#1660) keeps showing while tx_last_seen_backfill_v1
	// runs and operators don't see "ready" while the writer is still
	// pinned. Embedded as a top-level field; banner JS treats any
	// running migration as a reason to keep the banner up.
	var asyncMigrations []AsyncMigrationInfo
	if s.db != nil {
		asyncMigrations = readAsyncMigrations(s.db.conn)
	}
	asyncRunning := anyAsyncMigrationRunning(asyncMigrations)
	if asyncMigrations != nil {
		resp["async_migrations"] = asyncMigrations
	}
	resp["async_migrations_running"] = asyncRunning
	// PR #1609 M1: surface per-MQTT-source receipt vs write-path
	// liveness so operators can distinguish "broker alive, write
	// path stuck" (lastReceiptUnix recent, lastMessageUnix stale)
	// from "everything stalled" (both stale). Additive — older
	// ingestor builds simply produce no entry and the field is
	// omitted. Schema-compatible with prior /healthz consumers.
	if liveness := readIngestorSourceLiveness(); len(liveness) > 0 {
		resp["ingest_liveness"] = liveness
	}
	json.NewEncoder(w).Encode(resp)
}
