package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

// Partial #1670 backfill: pin the served spec, not only the metadata map.
func TestOpenAPIDiagnosticsDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	paths := asMap(t, spec["paths"], "paths")
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	gaps := loadKnownGaps(t)
	for _, tc := range []struct {
		path, tag, shape string
		terms            []string
		params           map[string]string
	}{
		{"/api/perf/io", "admin", "object", []string{"/proc/self/io", "ingestor"}, nil},
		{"/api/perf/sqlite", "admin", "object", []string{"cacheHitRate", "PacketStore", "zero"}, nil},
		{"/api/perf/write-sources", "admin", "object", []string{"sources", "sampleAt", "writer_perf", "unreadable"}, nil},
		{"/api/nodes/clock-skew", "nodes", "array", []string{"samples", "recentHashEvidence", "calibrationSummary"}, map[string]string{"query:area": "string"}},
		{"/api/nodes/{pubkey}/clock-skew", "nodes", "object", []string{"pubkey", "404"}, map[string]string{"path:pubkey": "string"}},
		{"/api/observers/clock-skew", "observers", "array", []string{"observerID", "offsetSec", "samples"}, nil},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := asMap(t, paths[tc.path], tc.path)
			if len(path) != 1 {
				t.Errorf("expected only GET, got %v", path)
			}
			op := asMap(t, path["get"], "get")
			if summary, _ := op["summary"].(string); summary == "" || summary == tc.path {
				t.Errorf("expected a descriptive summary, got %q", summary)
			}
			desc, _ := op["description"].(string)
			for _, term := range tc.terms {
				if !strings.Contains(desc, term) {
					t.Errorf("description should explain %q, got %q", term, desc)
				}
			}
			tags, _ := op["tags"].([]interface{})
			if len(tags) != 1 || tags[0] != tc.tag {
				t.Errorf("expected tag %q, got %v", tc.tag, tags)
			}
			if _, protected := op["security"]; protected {
				t.Error("read-only diagnostics must not advertise required credentials")
			}
			params, _ := op["parameters"].([]interface{})
			if len(params) != len(tc.params) {
				t.Errorf("expected %d parameters, got %v", len(tc.params), params)
			}
			seen := make(map[string]bool)
			for _, raw := range params {
				p := asMap(t, raw, "parameter")
				key := fmt.Sprint(p["in"], ":", p["name"])
				wantType, exists := tc.params[key]
				if !exists || seen[key] {
					t.Errorf("unexpected or repeated parameter %q", key)
				}
				seen[key] = true
				if p["required"] != (p["in"] == "path") || asMap(t, p["schema"], "parameter schema")["type"] != wantType {
					t.Errorf("wrong required/type for %q: %v", key, p)
				}
				if key == "query:area" {
					areaDesc, _ := p["description"].(string)
					for _, term := range []string{"unknown", "empty", "unfiltered"} {
						if !strings.Contains(areaDesc, term) {
							t.Errorf("area description must explain %q: %q", term, areaDesc)
						}
					}
				}
			}
			response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
			media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
			schema := asMap(t, media["schema"], "response schema")
			if ref, ok := schema["$ref"].(string); ok {
				schema = asMap(t, schemas[strings.TrimPrefix(ref, "#/components/schemas/")], ref)
			}
			if schema["type"] != tc.shape {
				t.Errorf("expected %s response, got %v", tc.shape, schema)
			}
			if tc.shape == "array" && schema["type"] == "array" && asMap(t, schema["items"], "items")["type"] != "object" {
				t.Error("diagnostics array items must be objects")
			}
			if gaps[tc.path] {
				t.Error("documented route must be removed from the known-gap list")
			}
		})
	}
}

// Configuring an admin key must not protect these GETs. Missing data is a
// successful empty diagnostic response, except the single-node lookup (404).
func TestDiagnosticsRoutesWithoutData(t *testing.T) {
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "missing.json"))
	s := NewServer(nil, &Config{APIKey: "test-diagnostics-key"}, NewHub())
	router := mux.NewRouter()
	s.RegisterRoutes(router)
	for _, tc := range []struct {
		path   string
		status int
		fields []string
	}{
		{"/api/perf/io", 200, []string{"readBytesPerSec", "writeBytesPerSec", "cancelledWriteBytesPerSec", "syscallsRead", "syscallsWrite"}},
		{"/api/perf/sqlite", 200, []string{"walSize", "walSizeMB", "pageCount", "pageSize", "cacheSize", "cacheHitRate"}},
		{"/api/perf/write-sources", 200, []string{"sources", "sampleAt"}},
		{"/api/nodes/clock-skew", 200, nil},
		{"/api/nodes/missing/clock-skew", 404, []string{"error"}},
		{"/api/observers/clock-skew", 200, nil},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.fields == nil {
				if strings.TrimSpace(w.Body.String()) != "[]" {
					t.Errorf("expected empty array, got %s", w.Body.String())
				}
				return
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != len(tc.fields) {
				t.Errorf("unexpected fields: %s", w.Body.String())
			}
			for _, field := range tc.fields {
				if _, ok := body[field]; !ok {
					t.Errorf("missing field %q", field)
				}
			}
			if tc.path == "/api/perf/sqlite" {
				var got PerfSqliteResponse
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != (PerfSqliteResponse{}) {
					t.Errorf("unavailable database should report zero metrics: %s", w.Body.String())
				}
			}
			if tc.path == "/api/perf/write-sources" && (string(body["sources"]) != "{}" || string(body["sampleAt"]) != `""`) {
				t.Errorf("missing file should report empty sources and timestamp: %s", w.Body.String())
			}
		})
	}
}

func TestDiagnosticsClockSkewRoutes(t *testing.T) {
	db := setupTestDBv2(t)
	cfg := &Config{APIKey: "test-diagnostics-key", Areas: map[string]AreaEntry{
		"inside": {Polygon: [][2]float64{{0, 0}, {0, 2}, {2, 2}, {2, 0}}},
		"empty":  {Polygon: [][2]float64{{30, 30}, {30, 32}, {32, 32}, {32, 30}}},
	}}
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('aabb', 'Inside', 'repeater', 1, 1), ('ccdd', 'Outside', 'repeater', 10, 10)`)
	s := NewServer(db, cfg, NewHub())
	s.store = NewPacketStore(db, nil)
	s.store.config = cfg
	pt := PayloadADVERT
	for _, pubkey := range []string{"aabb", "ccdd"} {
		tx := &StoreTx{Hash: pubkey, PayloadType: &pt,
			DecodedJSON: fmt.Sprintf(`{"payload":{"timestamp":1700002320},"pubKey":%q}`, pubkey),
			Observations: []*StoreObs{
				{ObserverID: "observer-a", Timestamp: "2023-11-14T22:50:00Z"},
				{ObserverID: "observer-b", Timestamp: "2023-11-14T22:50:20Z"},
			},
		}
		s.store.byNode[pubkey] = []*StoreTx{tx}
		s.store.byPayloadType[pt] = append(s.store.byPayloadType[pt], tx)
	}
	router := mux.NewRouter()
	s.RegisterRoutes(router)
	for _, tc := range []struct {
		query string
		count int
	}{{"", 2}, {"?area=inside", 1}, {"?area=unknown", 2}, {"?area=empty", 2}} {
		t.Run("fleet"+tc.query, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/clock-skew"+tc.query, nil))
			var rows []NodeClockSkew
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != tc.count {
				t.Fatalf("fleet status/count = %d/%d, want 200/%d: %s", w.Code, len(rows), tc.count, w.Body.String())
			}
			if tc.count == 1 && rows[0].Pubkey != "aabb" {
				t.Errorf("area should select the inside node, got %q", rows[0].Pubkey)
			}
			for _, row := range rows {
				if row.NodeName == "" || row.NodeRole != "repeater" || row.Samples != nil || row.RecentHashEvidence != nil || row.CalibrationSummary != nil {
					t.Errorf("fleet should enrich identity and omit detail evidence: %+v", row)
				}
			}
		})
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/aabb/clock-skew", nil))
	var node NodeClockSkew
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &node) != nil || node.Pubkey != "aabb" || len(node.Samples) == 0 || len(node.RecentHashEvidence) == 0 || node.CalibrationSummary == nil {
		t.Fatalf("node detail must include clock evidence: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/missing/clock-skew", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("missing node status = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/observers/clock-skew", nil))
	var observers []ObserverCalibration
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &observers) != nil || len(observers) != 2 {
		t.Fatalf("observer calibrations must be an array: %d %s", w.Code, w.Body.String())
	}
	for _, observer := range observers {
		if observer.Samples != 2 ||
			(observer.ObserverID != "observer-a" && observer.ObserverID != "observer-b") ||
			(observer.ObserverID == "observer-a" && observer.OffsetSec >= 0) ||
			(observer.ObserverID == "observer-b" && observer.OffsetSec <= 0) {
			t.Errorf("unexpected observer calibration: %+v", observer)
		}
	}

	// SQLite reports the in-process row cache hit ratio, not a SQLite cache counter.
	s.store.cacheHits, s.store.cacheMisses = 3, 1
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/perf/sqlite", nil))
	var sqlite PerfSqliteResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &sqlite) != nil || sqlite.CacheHitRate != 0.75 || sqlite.PageSize <= 0 {
		t.Errorf("expected SQLite page stats and PacketStore cache ratio: %d %s", w.Code, w.Body.String())
	}
}

func TestDiagnosticsWriteSourcesFileResponses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", path)
	s := NewServer(nil, &Config{APIKey: "test-diagnostics-key"}, NewHub())
	router := mux.NewRouter()
	s.RegisterRoutes(router)
	for _, tc := range []struct {
		name, file, sampleAt string
		wantSources          int
		wantWriterPerf       bool
	}{
		{"malformed", "{invalid", "", 0, false},
		{"published", `{"sampledAt":"2023-01-01T00:00:00Z","tx_inserted":7,"backfillUpdates":{"example":3}}`, "2023-01-01T00:00:00Z", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/perf/write-sources", nil))
			var body struct {
				Sources    map[string]int64               `json:"sources"`
				SampleAt   string                         `json:"sampleAt"`
				WriterPerf map[string]WriterStatsSnapshot `json:"writer_perf"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Sources) != tc.wantSources || body.SampleAt != tc.sampleAt || (body.WriterPerf != nil) != tc.wantWriterPerf {
				t.Fatalf("unexpected write-source response: %d %s", w.Code, w.Body.String())
			}
			if tc.name == "published" && (body.Sources["tx_inserted"] != 7 || body.Sources["backfill_example"] != 3) {
				t.Errorf("published counters missing: %+v", body.Sources)
			}
		})
	}
}
