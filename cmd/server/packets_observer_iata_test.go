// Test (#1188): /api/packets response must include observer_iata per packet
// so the frontend can render the IATA inline without per-row observer lookups.
package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestPacketsEndpointIncludesObserverIATA asserts the ungrouped packets endpoint
// surfaces the joined observer's IATA on each packet row.
func TestPacketsEndpointIncludesObserverIATA(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	packets, ok := body["packets"].([]interface{})
	if !ok || len(packets) == 0 {
		t.Fatal("expected non-empty packets array")
	}

	// Seeded observers: obs1 → SJC, obs2 → SFO. At least one packet row
	// must carry a non-empty observer_iata string.
	gotIATA := false
	for _, p := range packets {
		m, _ := p.(map[string]interface{})
		if m == nil {
			continue
		}
		if _, present := m["observer_iata"]; !present {
			t.Fatalf("packet missing observer_iata field; got keys: %v", keysOfMap(m))
		}
		if s, _ := m["observer_iata"].(string); s != "" {
			gotIATA = true
		}
	}
	if !gotIATA {
		t.Fatalf("expected at least one packet with non-empty observer_iata (seed has SJC/SFO)")
	}
}

// TestPacketsGroupedIncludesObserverIATA asserts the grouped (groupByHash)
// view also surfaces observer_iata for the header row.
func TestPacketsGroupedIncludesObserverIATA(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/packets?groupByHash=true&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	packets, _ := body["packets"].([]interface{})
	if len(packets) == 0 {
		t.Fatal("expected non-empty grouped packets")
	}
	gotIATA := false
	for _, p := range packets {
		m, _ := p.(map[string]interface{})
		if _, present := m["observer_iata"]; !present {
			t.Fatalf("grouped packet missing observer_iata field; got keys: %v", keysOfMap(m))
		}
		if s, _ := m["observer_iata"].(string); s != "" {
			gotIATA = true
		}
	}
	if !gotIATA {
		t.Fatalf("expected at least one grouped packet with non-empty observer_iata")
	}
}

// TestPacketDetailObservationsIncludeIATA asserts /api/packets/{id} returns
// per-observation observer_iata so the detail pane can render it.
func TestPacketDetailObservationsIncludeIATA(t *testing.T) {
	_, router := setupTestServer(t)
	// transmission_id 1 has two observations (obs1 SJC, obs2 SFO) from seedTestData
	req := httptest.NewRequest("GET", "/api/packets/1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	obs, _ := body["observations"].([]interface{})
	if len(obs) == 0 {
		t.Fatalf("expected observations in detail response; body: %s", w.Body.String())
	}
	gotIATA := false
	for _, o := range obs {
		m, _ := o.(map[string]interface{})
		if _, present := m["observer_iata"]; !present {
			t.Fatalf("observation missing observer_iata field; got keys: %v", keysOfMap(m))
		}
		if s, _ := m["observer_iata"].(string); s != "" {
			gotIATA = true
		}
	}
	if !gotIATA {
		t.Fatalf("expected at least one observation with non-empty observer_iata")
	}
}

func keysOfMap(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
