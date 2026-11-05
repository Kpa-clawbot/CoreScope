package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// This is a two-route #1670 backfill. Check the served spec, including the
// concrete success schemas; errors remain described in prose by this generator.
func TestOpenAPINodeResolveAndBatteryDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	paths := asMap(t, spec["paths"], "paths")
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	gaps := loadKnownGaps(t)
	for _, tc := range []struct {
		path, schema string
		terms        []string
		params       map[string]string
		fields       map[string]string
	}{
		{"/api/nodes/resolve", "NodeResolveResponse",
			[]string{"client_node_names", "ambiguous", "blacklisted", "hidden", "400", "500", "503", "text/plain"},
			map[string]string{"query:prefix": "string"},
			map[string]string{"prefix": "string", "pubkey": "string", "name": "string", "ambiguous": "boolean"}},
		{"/api/nodes/{pubkey}/battery", "NodeBatteryResponse",
			[]string{"exact", "pubkey", "observer_metrics", "404", "500", "application/json", "error", "null"},
			map[string]string{"path:pubkey": "string", "query:days": "integer"},
			map[string]string{"public_key": "string", "days": "integer", "samples": "array", "latest_mv": "integer", "latest_ts": "string", "status": "string", "thresholds": "object"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := asMap(t, paths[tc.path], tc.path)
			op := asMap(t, path["get"], "get")
			if len(path) != 1 || op["summary"] == tc.path || op["summary"] == "" {
				t.Errorf("expected a documented GET, got %v", path)
			}
			if !reflect.DeepEqual(op["tags"], []interface{}{"nodes"}) || op["security"] != nil {
				t.Errorf("expected public nodes operation, got %v", op)
			}
			desc, _ := op["description"].(string)
			for _, term := range tc.terms {
				if !strings.Contains(desc, term) {
					t.Errorf("description must explain %q: %q", term, desc)
				}
			}
			params, _ := op["parameters"].([]interface{})
			if len(params) != len(tc.params) {
				t.Errorf("expected %d parameters, got %v", len(tc.params), params)
			}
			seen := make(map[string]bool)
			for _, raw := range params {
				p := asMap(t, raw, "parameter")
				key := fmt.Sprint(p["in"], ":", p["name"])
				if seen[key] || asMap(t, p["schema"], "parameter schema")["type"] != tc.params[key] || p["required"] != (key != "query:days") {
					t.Errorf("unexpected parameter: %v", p)
				}
				seen[key] = true
				terms := map[string][]string{
					"query:prefix": {"4", "64", "trim", "lowercas", "Odd"},
					"query:days":   {"1", "365", "7", "invalid"},
				}[key]
				paramDesc, _ := p["description"].(string)
				for _, term := range terms {
					if !strings.Contains(paramDesc, term) {
						t.Errorf("%s must explain %q: %q", key, term, paramDesc)
					}
				}
			}
			response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
			media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
			if asMap(t, media["schema"], "response schema")["$ref"] != "#/components/schemas/"+tc.schema {
				t.Errorf("expected concrete %s response schema", tc.schema)
			}
			schema := asMap(t, schemas[tc.schema], tc.schema)
			props := asMap(t, schema["properties"], "properties")
			required, _ := schema["required"].([]interface{})
			if schema["type"] != "object" || len(props) != len(tc.fields) {
				t.Errorf("unexpected response shape: %v", schema)
			}
			for field, typ := range tc.fields {
				p := asMap(t, props[field], field)
				optional := field == "pubkey" || field == "name"
				nullable := field == "latest_mv" || field == "latest_ts"
				if p["type"] != typ || slices.Contains(required, interface{}(field)) == optional || (p["nullable"] == true) != nullable {
					t.Errorf("wrong type/required/nullable for %s: %v (required %v)", field, p, required)
				}
			}
			if tc.schema == "NodeBatteryResponse" {
				status := asMap(t, props["status"], "status")
				if !reflect.DeepEqual(status["enum"], []interface{}{"critical", "low", "ok", "unknown"}) {
					t.Errorf("wrong battery status enum: %v", status)
				}
				items := asMap(t, asMap(t, props["samples"], "samples")["items"], "sample")
				for _, nested := range []struct {
					schema map[string]interface{}
					fields map[string]string
				}{
					{items, map[string]string{"timestamp": "string", "battery_mv": "integer"}},
					{asMap(t, props["thresholds"], "thresholds"), map[string]string{"low_mv": "integer", "critical_mv": "integer"}},
				} {
					properties := asMap(t, nested.schema["properties"], "nested properties")
					required, _ := nested.schema["required"].([]interface{})
					if nested.schema["type"] != "object" || len(properties) != len(nested.fields) || len(required) != len(nested.fields) {
						t.Errorf("unexpected nested shape: %v", nested.schema)
					}
					for field, typ := range nested.fields {
						if asMap(t, properties[field], field)["type"] != typ || !slices.Contains(required, interface{}(field)) {
							t.Errorf("missing required %s %s: %v", typ, field, nested.schema)
						}
					}
				}
			}
			if gaps[tc.path] {
				t.Error("documented route must leave the known-gap list")
			}
		})
	}
}

// Exercise actual registered handlers separately from the documentation checks:
// public access, normalization, optional fields, defaults and error media types.
func TestNodeResolveAndBatteryDocumentedContracts(t *testing.T) {
	db := setupTestDB(t)
	srv := NewServer(db, &Config{APIKey: "test-node-docs-key", NodeBlacklist: []string{"deaf00"}, HiddenNamePrefixes: []string{"Hidden"}}, NewHub())
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	pubkey := strings.Repeat("ab", 32)
	for pk, name := range map[string]string{pubkey: "BatteryNode", "cafe00": "First", "cafe11": "Second", "deaf00": "Blocked", "beef00": "HiddenNode", "feed00": ""} {
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name) VALUES (?, ?)`, pk, name); err != nil {
			t.Fatal(err)
		}
	}
	get := func(t *testing.T, path string, code int, media string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != code || w.Header().Get("Content-Type") != media {
			t.Fatalf("GET %s: status=%d media=%q body=%s", path, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
		return w
	}
	t.Run("resolve", func(t *testing.T) {
		for _, tc := range []struct {
			prefix string
			want   ResolvePrefixResp
		}{
			{" ABABA ", ResolvePrefixResp{Prefix: "ababa", Pubkey: pubkey, Name: "BatteryNode"}},
			{"abab", ResolvePrefixResp{Prefix: "abab", Pubkey: pubkey, Name: "BatteryNode"}},
			{pubkey, ResolvePrefixResp{Prefix: pubkey, Pubkey: pubkey, Name: "BatteryNode"}},
			{"cafe", ResolvePrefixResp{Prefix: "cafe", Ambiguous: true}},
			{"dead", ResolvePrefixResp{Prefix: "dead"}},
			{"deaf", ResolvePrefixResp{Prefix: "deaf"}},
			{"beef", ResolvePrefixResp{Prefix: "beef"}},
			{"feed", ResolvePrefixResp{Prefix: "feed", Pubkey: "feed00"}},
		} {
			w := get(t, "/api/nodes/resolve?prefix="+url.QueryEscape(tc.prefix), 200, "application/json")
			var got ResolvePrefixResp
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if got != tc.want || (fields["pubkey"] == nil) != (tc.want.Pubkey == "") || (fields["name"] == nil) != (tc.want.Name == "") {
				t.Errorf("prefix %q: got %s, want %+v with empty optional fields omitted", tc.prefix, w.Body.String(), tc.want)
			}
		}
		for _, prefix := range []string{"", "xyz", "a", "aa", "abc", strings.Repeat("a", 65)} {
			w := get(t, "/api/nodes/resolve?prefix="+prefix, 400, "text/plain; charset=utf-8")
			want := "prefix must be hex\n"
			if prefix == "aa" || prefix == "abc" {
				want = "prefix must be at least 4 hex chars\n"
			}
			if w.Body.String() != want {
				t.Errorf("prefix %q: got %q, want %q", prefix, w.Body.String(), want)
			}
		}
		get(t, "/api/nodes/resolve", 400, "text/plain; charset=utf-8")
	})
	t.Run("battery", func(t *testing.T) {
		now := time.Now().UTC()
		for i, age := range []time.Duration{400 * 24 * time.Hour, 8 * 24 * time.Hour, 48 * time.Hour, time.Hour} {
			id := pubkey
			if i%2 == 1 {
				id = strings.ToUpper(id)
			}
			if _, err := db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`, id, now.Add(-age).Format(time.RFC3339), 3500-i*100); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp) VALUES (?, ?)`, pubkey, now.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			query         string
			days, samples int
		}{
			{"", 7, 2}, {"?days=1", 1, 1}, {"?days=365", 365, 3},
			{"?days=0", 7, 2}, {"?days=-1", 7, 2}, {"?days=366", 7, 2},
			{"?days=invalid", 7, 2}, {"?days=2.5", 7, 2},
		} {
			w := get(t, "/api/nodes/"+pubkey+"/battery"+tc.query, 200, "application/json")
			var body struct {
				PublicKey  string              `json:"public_key"`
				Days       int                 `json:"days"`
				Samples    []NodeBatterySample `json:"samples"`
				LatestMv   int                 `json:"latest_mv"`
				LatestTs   string              `json:"latest_ts"`
				Status     string              `json:"status"`
				Thresholds map[string]int      `json:"thresholds"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.PublicKey != pubkey || body.Days != tc.days || len(body.Samples) != tc.samples || body.LatestMv != 3200 || body.LatestTs != now.Add(-time.Hour).Format(time.RFC3339) || body.Status != "low" || !reflect.DeepEqual(body.Thresholds, map[string]int{"low_mv": 3300, "critical_mv": 3000}) {
				t.Errorf("%s: unexpected battery response %s", tc.query, w.Body.String())
			}
			if !slices.IsSortedFunc(body.Samples, func(a, b NodeBatterySample) int { return strings.Compare(a.Timestamp, b.Timestamp) }) {
				t.Errorf("samples must be oldest first: %v", body.Samples)
			}
		}
		w := get(t, "/api/nodes/feed00/battery", 200, "application/json")
		var empty map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"samples": "[]", "latest_mv": "null", "latest_ts": "null", "status": `"unknown"`} {
			if string(empty[key]) != want {
				t.Errorf("empty %s = %s, want %s", key, empty[key], want)
			}
		}
		for _, key := range []string{"not-hex", strings.ToUpper(pubkey), "abab"} {
			w := get(t, "/api/nodes/"+key+"/battery", 404, "application/json")
			if strings.TrimSpace(w.Body.String()) != `{"error":"node not found"}` {
				t.Errorf("unknown key %q: %s", key, w.Body.String())
			}
		}
	})
	t.Run("database errors", func(t *testing.T) {
		mustExecDB(t, db, `DROP TABLE observer_metrics`)
		checkJSONError := func(w *httptest.ResponseRecorder) {
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body["error"] == "" {
				t.Errorf("expected JSON error object: %s", w.Body.String())
			}
		}
		checkJSONError(get(t, "/api/nodes/"+pubkey+"/battery", 500, "application/json"))
		db.conn.Close()
		checkJSONError(get(t, "/api/nodes/"+pubkey+"/battery", 500, "application/json"))
		if w := get(t, "/api/nodes/resolve?prefix=abab", 500, "text/plain; charset=utf-8"); w.Body.String() != "query failed\n" {
			t.Errorf("unexpected query error: %s", w.Body.String())
		}
		for _, unavailable := range []*DB{nil, {}} {
			srv.db = unavailable
			if w := get(t, "/api/nodes/resolve?prefix=abab", 503, "text/plain; charset=utf-8"); w.Body.String() != "unavailable\n" {
				t.Errorf("unexpected unavailable error: %s", w.Body.String())
			}
		}
	})
}
