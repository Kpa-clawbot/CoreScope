package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

const clockSkewSampleLimitTestPK = "SKEWNODE001"

// setupClockSkewSampleLimitFixture builds a *Server wired to a PacketStore
// with 3 chronologically-ordered clock-skew samples for one node --
// enough to exercise sample_limit=0/1/2/len/over meaningfully. Mirrors
// TestGetNodeClockSkew_Integration's construction (NewPacketStore(nil,
// nil), manual byNode/byPayloadType population, computeInterval=0 to force
// a fresh Recompute on every call) since GetNodeClockSkew never touches
// the DB at all.
func setupClockSkewSampleLimitFixture(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	pt := 4 // ADVERT
	mkTx := func(hash string, advertTS, obsTS int64) *StoreTx {
		return &StoreTx{
			Hash:        hash,
			PayloadType: &pt,
			DecodedJSON: fmt.Sprintf(`{"payload":{"timestamp":%d}}`, advertTS),
			Observations: []*StoreObs{
				{ObserverID: "obs1", Timestamp: time.Unix(obsTS, 0).UTC().Format(time.RFC3339)},
			},
		}
	}
	// Node clock is a constant 100s ahead across all three transmissions --
	// only their chronological order (oldest -> newest) matters here.
	tx1 := mkTx("skew-hash-1", 1700000100, 1700000000)
	tx2 := mkTx("skew-hash-2", 1700003700, 1700003600)
	tx3 := mkTx("skew-hash-3", 1700007300, 1700007200)

	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()
	ps.byNode[clockSkewSampleLimitTestPK] = []*StoreTx{tx1, tx2, tx3}
	ps.byPayloadType[pt] = []*StoreTx{tx1, tx2, tx3}
	ps.clockSkew.computeInterval = 0
	ps.mu.Unlock()

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(nil, cfg, hub)
	srv.store = ps
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

func getClockSkew(t *testing.T, router *mux.Router, query string) (*httptest.ResponseRecorder, NodeClockSkew) {
	t.Helper()
	url := "/api/nodes/" + clockSkewSampleLimitTestPK + "/clock-skew"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var resp NodeClockSkew
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body=%s)", err, w.Body.String())
		}
	}
	return w, resp
}

// 1. No sample_limit parameter -> unchanged legacy response, all samples.
func TestNodeClockSkewSampleLimit_MissingReturnsAll(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	w, resp := getClockSkew(t, router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(resp.Samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(resp.Samples))
	}
}

// 2. Invalid (non-numeric) sample_limit -> unchanged, all samples.
func TestNodeClockSkewSampleLimit_InvalidReturnsAll(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	w, resp := getClockSkew(t, router, "sample_limit=notanumber")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(resp.Samples) != 3 {
		t.Fatalf("expected 3 samples (invalid limit ignored), got %d", len(resp.Samples))
	}
}

// 3. Negative sample_limit -> unchanged, all samples.
func TestNodeClockSkewSampleLimit_NegativeReturnsAll(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	w, resp := getClockSkew(t, router, "sample_limit=-1")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(resp.Samples) != 3 {
		t.Fatalf("expected 3 samples (negative limit ignored), got %d", len(resp.Samples))
	}
}

// 4. sample_limit=0: "samples" key entirely absent from raw JSON (decoding
// to a struct alone can't distinguish "missing key" from "empty/nil
// slice"), sampleCount unchanged, and every other field unchanged.
func TestNodeClockSkewSampleLimit_ZeroOmitsSamplesKey(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)

	wFull, respFull := getClockSkew(t, router, "")
	if wFull.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wFull.Code)
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+clockSkewSampleLimitTestPK+"/clock-skew?sample_limit=0", nil)
	wZero := httptest.NewRecorder()
	router.ServeHTTP(wZero, req)
	if wZero.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wZero.Code)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(wZero.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, present := raw["samples"]; present {
		t.Error(`expected "samples" key to be entirely absent from raw JSON at sample_limit=0`)
	}

	var respZero NodeClockSkew
	if err := json.Unmarshal(wZero.Body.Bytes(), &respZero); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if respZero.SampleCount != respFull.SampleCount {
		t.Errorf("sampleCount changed: full=%d zero=%d", respFull.SampleCount, respZero.SampleCount)
	}
	// Neutralize the one field sample_limit is allowed to change, then
	// require everything else to match exactly.
	respZero.Samples = respFull.Samples
	if !reflect.DeepEqual(respFull, respZero) {
		t.Errorf("fields other than samples changed:\nfull: %+v\nzero: %+v", respFull, respZero)
	}
}

// 5. sample_limit=1 returns exactly the single most-recent sample.
func TestNodeClockSkewSampleLimit_OneReturnsLatestOnly(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	_, respFull := getClockSkew(t, router, "")
	w, resp := getClockSkew(t, router, "sample_limit=1")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(resp.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(resp.Samples))
	}
	want := respFull.Samples[len(respFull.Samples)-1]
	if resp.Samples[0] != want {
		t.Errorf("sample = %+v, want latest %+v", resp.Samples[0], want)
	}
}

// 6. sample_limit=2 returns the two most recent, in original chronological order.
func TestNodeClockSkewSampleLimit_TwoReturnsLatestTwoInOrder(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	_, respFull := getClockSkew(t, router, "")
	w, resp := getClockSkew(t, router, "sample_limit=2")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	n := len(respFull.Samples)
	want := respFull.Samples[n-2:]
	if !reflect.DeepEqual(resp.Samples, want) {
		t.Errorf("Samples = %+v, want %+v (latest 2, chronological order)", resp.Samples, want)
	}
	if resp.Samples[0].Timestamp >= resp.Samples[1].Timestamp {
		t.Errorf("expected ascending chronological order, got %+v", resp.Samples)
	}
}

// 7. sample_limit == len(samples) returns all.
func TestNodeClockSkewSampleLimit_EqualToLengthReturnsAll(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	_, respFull := getClockSkew(t, router, "")
	w, resp := getClockSkew(t, router, fmt.Sprintf("sample_limit=%d", len(respFull.Samples)))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !reflect.DeepEqual(resp.Samples, respFull.Samples) {
		t.Errorf("Samples = %+v, want all %+v", resp.Samples, respFull.Samples)
	}
}

// 8. sample_limit > len(samples) returns all, no panic.
func TestNodeClockSkewSampleLimit_GreaterThanLengthReturnsAllNoPanic(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	_, respFull := getClockSkew(t, router, "")
	w, resp := getClockSkew(t, router, "sample_limit=9999")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !reflect.DeepEqual(resp.Samples, respFull.Samples) {
		t.Errorf("Samples = %+v, want all %+v", resp.Samples, respFull.Samples)
	}
}

// 9. sample_limit=0 followed by a default (no-param) call: the default
// call must still return all samples -- proving the projection mutated
// only the per-request response object, not any cached/shared state in
// ClockSkewEngine.
func TestNodeClockSkewSampleLimit_ZeroThenDefaultProvesNoSharedStateMutation(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)

	wZero, respZero := getClockSkew(t, router, "sample_limit=0")
	if wZero.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wZero.Code)
	}
	if len(respZero.Samples) != 0 {
		t.Fatalf("expected 0 samples at limit=0, got %d", len(respZero.Samples))
	}

	wDefault, respDefault := getClockSkew(t, router, "")
	if wDefault.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wDefault.Code)
	}
	if len(respDefault.Samples) != 3 {
		t.Fatalf("default call after sample_limit=0 returned %d samples, want 3 -- shared/cached state was mutated", len(respDefault.Samples))
	}
}

// 10. A node with no clock-skew data keeps the existing 404 and error text.
func TestNodeClockSkewSampleLimit_NoDataNode404Unchanged(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	req := httptest.NewRequest("GET", "/api/nodes/does-not-exist/clock-skew?sample_limit=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["error"] != "No clock skew data for this node" {
		t.Errorf("error = %q, want %q", body["error"], "No clock skew data for this node")
	}
}

// 11. recentBadSamples, recentHashEvidence, calibrationSummary, severity,
// and every other summary field are unaffected by sample_limit, across
// several limit values (not just 0 -- see test 4 for the limit=0-specific
// raw-JSON-key-absence check).
func TestNodeClockSkewSampleLimit_OtherFieldsUnaffectedAcrossLimits(t *testing.T) {
	_, router := setupClockSkewSampleLimitFixture(t)
	_, respFull := getClockSkew(t, router, "")

	for _, query := range []string{"sample_limit=0", "sample_limit=1", "sample_limit=2", "sample_limit=9999"} {
		_, resp := getClockSkew(t, router, query)
		if resp.Severity != respFull.Severity {
			t.Errorf("[%s] Severity = %v, want %v", query, resp.Severity, respFull.Severity)
		}
		if !reflect.DeepEqual(resp.RecentBadSamples, respFull.RecentBadSamples) {
			t.Errorf("[%s] RecentBadSamples = %+v, want %+v", query, resp.RecentBadSamples, respFull.RecentBadSamples)
		}
		if !reflect.DeepEqual(resp.RecentHashEvidence, respFull.RecentHashEvidence) {
			t.Errorf("[%s] RecentHashEvidence = %+v, want %+v", query, resp.RecentHashEvidence, respFull.RecentHashEvidence)
		}
		if !reflect.DeepEqual(resp.CalibrationSummary, respFull.CalibrationSummary) {
			t.Errorf("[%s] CalibrationSummary = %+v, want %+v", query, resp.CalibrationSummary, respFull.CalibrationSummary)
		}
		if resp.SampleCount != respFull.SampleCount {
			t.Errorf("[%s] SampleCount = %d, want %d", query, resp.SampleCount, respFull.SampleCount)
		}
		if resp.MeanSkewSec != respFull.MeanSkewSec ||
			resp.MedianSkewSec != respFull.MedianSkewSec ||
			resp.RecentMedianSkewSec != respFull.RecentMedianSkewSec ||
			resp.GoodFraction != respFull.GoodFraction ||
			resp.RecentSampleCount != respFull.RecentSampleCount ||
			resp.RecentBadSampleCount != respFull.RecentBadSampleCount {
			t.Errorf("[%s] a summary stat field changed:\ngot:  %+v\nwant: %+v", query, resp, respFull)
		}
	}
}
