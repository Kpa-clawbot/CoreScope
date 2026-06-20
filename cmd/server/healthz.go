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

	// #1724: surface async migration progress so the warm-up banner can
	// stay visible while a long-running backfill is still consuming the
	// single writer. anyAsyncMigrationRunning intentionally drops to
	// false on "failed" status — operator should see warm-up complete
	// + alert, not an endless banner.
	//
	// #1735 finding #1 (Group A): on readAsyncMigrations error, surface
	// the error AND keep async_migrations_running=true so the banner
	// stays visible under uncertainty. We fail CLOSED for warm-up: if
	// we cannot read the bookkeeping table, we treat the system as
	// possibly still warming up rather than declaring "all clear".
	var asyncMigrations []AsyncMigrationInfo
	var asyncMigrationsErr string
	var asyncRunning bool
	if s.db != nil {
		infos, err := readAsyncMigrations(s.db.conn)
		if err != nil {
			asyncMigrationsErr = err.Error()
			asyncRunning = true // fail closed — keep banner up
		} else {
			asyncMigrations = infos
			asyncRunning = anyAsyncMigrationRunning(infos)
		}
	}
	if asyncMigrations == nil {
		asyncMigrations = []AsyncMigrationInfo{}
	}

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
		"async_migrations":         asyncMigrations,
		"async_migrations_running": asyncRunning,
	}
	if asyncMigrationsErr != "" {
		resp["async_migrations_error"] = asyncMigrationsErr
	}
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
