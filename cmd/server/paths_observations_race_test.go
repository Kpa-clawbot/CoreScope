package main

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/mux"
)

// handleNodePaths reads each surviving candidate's canonical resolved_path
// after releasing s.mu. That lookup used to walk tx.Observations there, which
// ingest appends to under the write lock. Run with -race.
func TestNodePathsDoesNotReadObservationsUnlocked(t *testing.T) {
	store := newResolvedPathStore(t)
	srv := NewServer(store.db, &Config{Port: 3000}, NewHub())
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	store.mu.RLock()
	tx := store.byHash["testhash00000001"]
	store.mu.RUnlock()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // stands in for ingest adding observations to the tx
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			store.mu.Lock()
			tx.Observations = append(tx.Observations, &StoreObs{ID: 1_000_000 + i, TransmissionID: tx.ID, PathJSON: `["aa"]`})
			store.mu.Unlock()
		}
	}()

	for i := 0; i < 50; i++ {
		clearResolvedPathLRU(store)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/paths", nil))
		if w.Code != 200 {
			t.Fatalf("paths: status %d: %s", w.Code, w.Body.String())
		}
	}
	close(stop)
	wg.Wait()
}
