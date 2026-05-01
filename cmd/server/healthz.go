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

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ready":     true,
		"loadedTx":  loadedTx,
		"loadedObs": loadedObs,
	})
}
