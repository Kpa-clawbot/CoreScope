package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIKnownChannelsDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	path := asMap(t, asMap(t, spec["paths"], "paths")["/api/known-channels"], "known channels")
	op := asMap(t, path["get"], "get")
	if len(path) != 1 || op["summary"] == "" || op["summary"] == "/api/known-channels" {
		t.Errorf("expected a documented GET operation: %v", path)
	}
	if spec["security"] != nil || op["security"] != nil || !reflect.DeepEqual(op["tags"], []interface{}{"channels"}) {
		t.Errorf("expected a public channels operation: %v", op)
	}
	description, _ := op["description"].(string)
	for _, term := range []string{"catalogue", "cache", "empty", "order", "max-age=30", "max-age=300"} {
		if !strings.Contains(description, term) {
			t.Errorf("description must explain %q: %q", term, description)
		}
	}
	params, _ := op["parameters"].([]interface{})
	if len(params) != 1 {
		t.Errorf("expected only the region query parameter: %v", params)
	}
	for _, raw := range params {
		p := asMap(t, raw, "parameter")
		if p["name"] != "region" || p["in"] != "query" || p["required"] != false || !reflect.DeepEqual(p["schema"], map[string]interface{}{"type": "string"}) {
			t.Errorf("expected an optional string region parameter: %v", p)
		}
		desc, _ := p["description"].(string)
		for _, term := range []string{"catalogue", "case-insensitive", "trim", "blank", "all", "Unknown", "empty", "IATA"} {
			if !strings.Contains(desc, term) {
				t.Errorf("region description must explain %q: %q", term, desc)
			}
		}
	}
	if loadKnownGaps(t)["/api/known-channels"] {
		t.Error("documented known-channels route must leave the known-gap list")
	}
	response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
	media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
	if asMap(t, media["schema"], "response schema")["$ref"] != "#/components/schemas/KnownChannelsResponse" {
		t.Error("known channels must use its concrete response schema")
	}
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	snapshot := asMap(t, schemas["KnownChannelsResponse"], "known-channels response schema")
	props := asMap(t, snapshot["properties"], "snapshot properties")
	entries := asMap(t, props["entries"], "entries")
	if entries["type"] != "array" || entries["nullable"] == true {
		t.Errorf("entries must be a non-null array: %v", entries)
	}
	item := asMap(t, entries["items"], "catalogue entry")
	for _, tc := range []struct {
		name     string
		schema   map[string]interface{}
		fields   map[string]string
		required []string
	}{
		{"snapshot", snapshot, map[string]string{"fetchedAt": "string", "source": "string", "entries": "array", "generatedAt": "string", "license": "string"}, []string{"fetchedAt", "source", "entries"}},
		{"entry", item, map[string]string{"channel": "string", "region": "string", "description": "string", "key": "string", "regionName": "string"}, []string{"channel", "region"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			properties := asMap(t, tc.schema["properties"], tc.name+" properties")
			required, _ := tc.schema["required"].([]interface{})
			if tc.schema["type"] != "object" || tc.schema["nullable"] == true || len(properties) != len(tc.fields) || len(required) != len(tc.required) {
				t.Errorf("unexpected %s shape: %v", tc.name, tc.schema)
			}
			for name, kind := range tc.fields {
				property := asMap(t, properties[name], name)
				if property["type"] != kind || property["nullable"] == true || slices.Contains(required, interface{}(name)) != slices.Contains(tc.required, name) {
					t.Errorf("wrong type, nullability or optionality for %s: %v", name, property)
				}
				desc, _ := property["description"].(string)
				if desc == "" || (!slices.Contains(tc.required, name) && !strings.Contains(desc, "omitted")) {
					t.Errorf("%s must describe its value and omission rule: %q", name, desc)
				}
				// Metadata is unvalidated, and region labels may be arbitrary or empty.
				if name == "generatedAt" || name == "key" || name == "region" {
					for _, constraint := range []string{"format", "pattern", "enum", "minLength", "maxLength"} {
						if _, ok := property[constraint]; ok {
							t.Errorf("%s must not invent a %s constraint: %v", name, constraint, property)
						}
					}
				}
			}
		})
	}
	if desc, _ := asMap(t, props["fetchedAt"], "fetchedAt")["description"].(string); !strings.Contains(desc, "0001-01-01T00:00:00Z") {
		t.Errorf("fetchedAt must document the unavailable-cache sentinel: %q", desc)
	}
	if desc, _ := asMap(t, props["source"], "source")["description"].(string); !strings.Contains(desc, "empty") {
		t.Errorf("source must document the empty unavailable-cache value: %q", desc)
	}
}

func TestKnownChannelsDocumentedResponses(t *testing.T) {
	srv, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")
	fetchedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const fixture = `{
		"generated_at":"upstream-generation-label",
		"license":"CC0-1.0",
		"countryNames":{" ZZ ":"Fixture region"},
		"countries":{
			" ZZ ":[
				{"channel":" #fixture-one ","description":"  Fixture description  ","key":"not-base64"},
				{"channel":"plain-fixture"},
				{"channel":"   "}
			],
			"custom-region":[{"channel":"#fixture-two"}],
			"  ":[{"channel":"#fixture-blank"}],
			"empty":[]
		}
	}`
	snap, err := parseKnownChannelsJSON([]byte(fixture), "fixture://catalogue", fetchedAt)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := parseKnownChannelsJSON([]byte(`{}`), "fixture://catalogue", fetchedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Index only these distinct fixture names; catalogue order is unspecified.
	allEntries := map[string]interface{}{
		"#fixture-one":   map[string]interface{}{"channel": "#fixture-one", "region": "zz", "regionName": "Fixture region", "description": "  Fixture description  ", "key": "not-base64"},
		"plain-fixture":  map[string]interface{}{"channel": "plain-fixture", "region": "zz", "regionName": "Fixture region"},
		"#fixture-two":   map[string]interface{}{"channel": "#fixture-two", "region": "custom-region"},
		"#fixture-blank": map[string]interface{}{"channel": "#fixture-blank", "region": ""},
	}
	for _, tc := range []struct {
		name, query string
		cache       bool
		snapshot    *KnownChannelsSnapshot
		channels    []string
	}{
		{name: "nil cache"},
		{name: "unpopulated cache", cache: true},
		{name: "populated empty", cache: true, snapshot: empty},
		{name: "all", cache: true, snapshot: snap, channels: []string{"#fixture-one", "plain-fixture", "#fixture-two", "#fixture-blank"}},
		{name: "empty filter", query: "?region=", cache: true, snapshot: snap, channels: []string{"#fixture-one", "plain-fixture", "#fixture-two", "#fixture-blank"}},
		{name: "blank filter", query: "?region=%20%09%20", cache: true, snapshot: snap, channels: []string{"#fixture-one", "plain-fixture", "#fixture-two", "#fixture-blank"}},
		{name: "case and whitespace", query: "?region=%20zZ%20", cache: true, snapshot: snap, channels: []string{"#fixture-one", "plain-fixture"}},
		{name: "catalogue region", query: "?region=CUSTOM-REGION", cache: true, snapshot: snap, channels: []string{"#fixture-two"}},
		{name: "unknown", query: "?region=unknown", cache: true, snapshot: snap},
		{name: "not a region list", query: "?region=zz,custom-region", cache: true, snapshot: snap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv.knownChannels = nil
			wantCacheControl := "public, max-age=30"
			want := map[string]interface{}{"fetchedAt": "0001-01-01T00:00:00Z", "source": ""}
			if tc.cache {
				srv.knownChannels = &knownChannelsCache{}
				srv.knownChannels.ptr.Store(tc.snapshot)
			}
			if tc.snapshot != nil {
				wantCacheControl = "public, max-age=300"
				want["fetchedAt"] = "2026-01-02T03:04:05Z"
				want["source"] = "fixture://catalogue"
				if tc.snapshot == snap {
					want["generatedAt"] = "upstream-generation-label"
					want["license"] = "CC0-1.0"
				}
			}
			// Exercise the registered route and middleware without credentials.
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/known-channels"+tc.query, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != wantCacheControl {
				t.Fatalf("unexpected public response: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			var got map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			entries, ok := got["entries"].([]interface{})
			if !ok || len(entries) != len(tc.channels) {
				t.Fatalf("expected %d entries in a non-null array, got %v", len(tc.channels), got["entries"])
			}
			indexed := make(map[string]interface{}, len(entries))
			for _, raw := range entries {
				entry := asMap(t, raw, "entry")
				channel, ok := entry["channel"].(string)
				if !ok || indexed[channel] != nil {
					t.Fatalf("missing or duplicate fixture channel: %v", entry)
				}
				indexed[channel] = entry
			}
			got["entries"] = indexed
			wantEntries := make(map[string]interface{}, len(tc.channels))
			for _, name := range tc.channels {
				wantEntries[name] = allEntries[name]
			}
			want["entries"] = wantEntries
			if !reflect.DeepEqual(got, want) {
				t.Errorf("response differs from documented fields and omission rules:\ngot %v\nwant %v", got, want)
			}
		})
	}
}
