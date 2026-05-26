package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetNodeHashSizeInfoReturnsStaleWhileRefreshing(t *testing.T) {
	store := NewPacketStore(nil, nil)
	stale := map[string]*hashSizeNodeInfo{
		"old": {HashSize: 1, AllSizes: map[int]bool{1: true}},
	}
	fresh := map[string]*hashSizeNodeInfo{
		"new": {HashSize: 2, AllSizes: map[int]bool{2: true}},
	}
	block := make(chan struct{})
	orig := computeHashSizeInfoFn
	computeHashSizeInfoFn = func(*PacketStore) map[string]*hashSizeNodeInfo {
		<-block
		return fresh
	}
	defer func() {
		computeHashSizeInfoFn = orig
	}()

	store.hashSizeInfoMu.Lock()
	store.hashSizeInfoCache = stale
	store.hashSizeInfoAt = time.Now().Add(-time.Hour)
	store.hashSizeInfoMu.Unlock()

	got := store.GetNodeHashSizeInfo()
	if got["old"] == nil {
		t.Fatal("expected stale hash-size cache to be returned immediately")
	}
	close(block)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.hashSizeInfoMu.Lock()
		updated := store.hashSizeInfoCache["new"] != nil
		store.hashSizeInfoMu.Unlock()
		if updated {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background hash-size refresh did not update cache")
}

func TestStatsEndpointReturnsStaleWhileRefreshing(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.statsMu.Lock()
	srv.statsCache = &StatsResponse{TotalPackets: 123, Engine: "go"}
	srv.statsCachedAt = time.Now().Add(-time.Hour)
	srv.statsComputing = false
	srv.statsMu.Unlock()

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := int(body["totalPackets"].(float64)); got != 123 {
		t.Fatalf("totalPackets = %d, want stale cached 123", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.statsMu.Lock()
		computing := srv.statsComputing
		srv.statsMu.Unlock()
		if !computing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background stats refresh did not finish")
}
