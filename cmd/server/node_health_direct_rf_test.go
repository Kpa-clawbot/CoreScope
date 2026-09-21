package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// End-to-end guard for the reported bug: a 433 MHz repeater listed 13
// observers on its detail page, twelve of which run on 868 MHz only and
// cannot physically have heard it. They were credited because the node's
// one-byte pubkey prefix collides with ten other repeaters and
// resolveWithContext picks a winner instead of abstaining.
//
// /api/nodes/{pk}/health must now report only observers that received the
// node's own transmission off the air, and count the rest separately.
func TestNodeHealth_RelayedObserverIsNotHeardBy(t *testing.T) {
	srv, router := setupTestServer(t)
	const nodePubkey = "aabbccdd11223344" // seeded by seedTestData
	now := time.Now().UTC().Format(time.RFC3339)

	routeFlood := RouteFlood
	payload := PayloadTXT_MSG
	snr := 3.0
	rssi := -110.0

	// A long flood path that merely passes through the node. The observer
	// at the far end saw the packet; it never heard this node.
	relayed := &StoreTx{
		Hash:             "directrf-relayed",
		FirstSeen:        now,
		RouteType:        &routeFlood,
		PayloadType:      &payload,
		SNR:              &snr,
		RSSI:             &rssi,
		ObservationCount: 1,
		ObserverID:       "farawayobserver",
		ObserverName:     "FarAway",
		Observations: []*StoreObs{{
			ObserverID:   "farawayobserver",
			ObserverName: "FarAway",
			PathJSON:     `["AABB","1234","5678"]`,
			SNR:          &snr,
			RSSI:         &rssi,
		}},
	}

	srv.store.mu.Lock()
	if srv.store.byNode == nil {
		srv.store.byNode = make(map[string][]*StoreTx)
	}
	srv.store.byNode[nodePubkey] = append(srv.store.byNode[nodePubkey], relayed)
	srv.store.packets = append(srv.store.packets, relayed)
	srv.store.mu.Unlock()
	srv.store.publishDirectHeard(srv.store.computeDirectHeard())

	req := httptest.NewRequest(http.MethodGet, "/api/nodes/"+nodePubkey+"/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}

	obs, _ := body["observers"].([]interface{})
	for _, raw := range obs {
		row, ok := raw.(map[string]interface{})
		if ok && row["observer_id"] == "farawayobserver" {
			t.Fatalf("an observer that only saw relayed traffic was listed as having heard the node: %v", row)
		}
	}

	relayCount, ok := body["relayObserverCount"].(float64)
	if !ok {
		t.Fatalf("relayObserverCount missing or not a number: %T %v",
			body["relayObserverCount"], body["relayObserverCount"])
	}
	if relayCount < 1 {
		t.Fatalf("relayObserverCount = %v, want at least 1 (the relayed observer must still be counted)", relayCount)
	}
}

// The walk runs over every observation in the store on each recompute pass.
// The reference deployment holds 2.9M of them; this pins that a full pass
// stays well inside one recompute interval.
func BenchmarkBuildDirectHeardIndex(b *testing.B) {
	const (
		txCount  = 60000
		perTx    = 50 // 3M observations total
		hopCount = 8
	)
	nodes := make([]nodeInfo, 0, 64)
	for i := 0; i < 64; i++ {
		nodes = append(nodes, nodeInfo{
			Role:      "repeater",
			PublicKey: string([]byte{hexDigit(i / 16), hexDigit(i % 16)}) + "00112233445566778899aabbccddeeff00112233445566778899aabbccddee",
		})
	}
	pm := buildPrefixMap(nodes)

	routeFlood := RouteFlood
	payload := PayloadTXT_MSG
	path := `["AABB","1234","5678","9ABC","DEF0","0011","2233","4455"]`
	packets := make([]*StoreTx, 0, txCount)
	for i := 0; i < txCount; i++ {
		obsList := make([]*StoreObs, 0, perTx)
		for j := 0; j < perTx; j++ {
			obsList = append(obsList, &StoreObs{ObserverID: "obs", PathJSON: path})
		}
		packets = append(packets, &StoreTx{
			RouteType:    &routeFlood,
			PayloadType:  &payload,
			Observations: obsList,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildDirectHeardIndex(packets, pm)
	}
	_ = hopCount
}

func hexDigit(v int) byte {
	if v < 10 {
		return byte('0' + v)
	}
	return byte('a' + v - 10)
}
