package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzNotReady(t *testing.T) {
	// Ensure readiness is 0 (not ready)
	readiness.Store(0)
	defer readiness.Store(0)

	store := &PacketStore{clockSkew: &ClockSkewEngine{}}
	store.signalStartupLoadDone()
	srv := &Server{store: store}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != false {
		t.Fatalf("expected ready=false, got %v", resp["ready"])
	}
	if resp["reason"] != "loading" {
		t.Fatalf("expected reason=loading, got %v", resp["reason"])
	}
}

func TestHealthzReady(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	store := &PacketStore{clockSkew: &ClockSkewEngine{}}
	store.signalStartupLoadDone()
	srv := &Server{store: store}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != true {
		t.Fatalf("expected ready=true, got %v", resp["ready"])
	}
	if _, ok := resp["loadedTx"]; !ok {
		t.Fatal("missing loadedTx field")
	}
	if _, ok := resp["loadedObs"]; !ok {
		t.Fatal("missing loadedObs field")
	}
}

func TestHealthzAntiTautology(t *testing.T) {
	// When readiness is 0, must NOT return 200
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("anti-tautology: handler returned 200 when readiness=0; gating is broken")
	}
}

// FirstChunkReady permits HTTP serving while either startup phase is still
// loading. Deployment readiness must wait for both phases and reject failures.
func TestHealthzWaitsForSuccessfulStartupLoad(t *testing.T) {
	oldReadiness := readiness.Swap(1)
	t.Cleanup(func() { readiness.Store(oldReadiness) })
	for _, phase := range []string{"hot_chunks", "background_fill"} {
		for _, fail := range []bool{false, true} {
			name := phase + "/success"
			if fail {
				name = phase + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				dbPath := createTestDBWithAgedPackets(t, 6, 6)
				db, err := openFixtureReader(t, dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.conn.Close()
				store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72, HotStartupHours: 1})
				srv := &Server{store: store}
				checkHealth := func(want int) {
					t.Helper()
					w := httptest.NewRecorder()
					srv.handleHealthz(w, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
					if w.Code != want {
						t.Errorf("health status = %d, want %d; body=%s", w.Code, want, w.Body.String())
					}
					var response struct {
						Ready bool `json:"ready"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if response.Ready != (want == http.StatusOK) {
						t.Errorf("ready=%t, want %t", response.Ready, want == http.StatusOK)
					}
				}
				hookCalled := false
				checkLoading := func() {
					if hookCalled {
						return
					}
					hookCalled = true
					if !isClosedForTest(store.FirstChunkReady()) {
						t.Error("HTTP startup must be unblocked before all chunks finish")
					}
					if isClosedForTest(store.StartupLoadDone()) {
						t.Error("startup unexpectedly complete inside loader")
					}
					if got, want := store.LoadComplete(), phase == "background_fill"; got != want {
						t.Errorf("LoadComplete=%t, want %t during %s", got, want, phase)
					}
					checkHealth(http.StatusServiceUnavailable)
					if fail {
						if err := db.conn.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				if phase == "hot_chunks" {
					store.OnChunkLoaded(func(_, _ int) { checkLoading() })
				} else {
					store.bgLoaderEntryHook = checkLoading
				}
				err = store.RunStartupLoad(2)
				if fail && phase == "hot_chunks" {
					if err == nil {
						t.Error("expected a late chunk read error")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !hookCalled || !isClosedForTest(store.StartupLoadDone()) {
					t.Fatal("startup did not exercise the hook and reach a terminal state")
				}
				if store.backgroundLoadFailed.Load() != fail {
					t.Fatalf("backgroundLoadFailed=%t, want %t", store.backgroundLoadFailed.Load(), fail)
				}
				want := http.StatusOK
				if fail {
					want = http.StatusServiceUnavailable
				}
				checkHealth(want)
			})
		}
	}
}
