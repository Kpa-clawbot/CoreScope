package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOpenAPIReadinessDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	op := asMap(t, asMap(t, asMap(t, spec["paths"], "paths")["/api/healthz"], "healthz")["get"], "get")
	if op["summary"] == "" || op["summary"] == "/api/healthz" || op["security"] != nil {
		t.Fatalf("readiness needs a public, described GET operation: %v", op)
	}
	if !reflect.DeepEqual(op["tags"], []interface{}{"admin"}) {
		t.Errorf("unexpected readiness tag: %v", op["tags"])
	}
	if params, _ := op["parameters"].([]interface{}); len(params) != 0 {
		t.Errorf("readiness has no parameters: %v", params)
	}
	description, _ := op["description"].(string)
	if !strings.Contains(description, "503") || !strings.Contains(description, "loading") {
		t.Error("readiness documentation must describe the loading response")
	}
	response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
	media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
	if asMap(t, media["schema"], "schema")["$ref"] != "#/components/schemas/ReadinessResponse" {
		t.Fatal("readiness must use its concrete response schema")
	}
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	schema := asMap(t, schemas["ReadinessResponse"], "readiness schema")
	properties := asMap(t, schema["properties"], "readiness properties")
	required, _ := schema["required"].([]interface{})
	for name, kind := range map[string]string{"ready": "boolean", "loadedTx": "integer", "loadedObs": "integer", "from_pubkey_backfill": "object", "ingest_liveness": "object"} {
		property := asMap(t, properties[name], name)
		if property["type"] != kind || slices.Contains(required, interface{}(name)) == (name == "ingest_liveness") {
			t.Errorf("wrong type or optionality for %s: %v", name, property)
		}
	}
	backfill := asMap(t, properties["from_pubkey_backfill"], "backfill")
	backfillProps := asMap(t, backfill["properties"], "backfill properties")
	for name, kind := range map[string]string{"total": "integer", "processed": "integer", "done": "boolean"} {
		if asMap(t, backfillProps[name], name)["type"] != kind {
			t.Errorf("wrong backfill type for %s", name)
		}
	}
	source := asMap(t, asMap(t, properties["ingest_liveness"], "liveness")["additionalProperties"], "source")
	for _, name := range []string{"lastReceiptUnix", "lastMessageUnix"} {
		if asMap(t, asMap(t, source["properties"], "source properties")[name], name)["type"] != "integer" {
			t.Errorf("wrong liveness timestamp type for %s", name)
		}
	}
	if loadKnownGaps(t)["/api/healthz"] {
		t.Error("documented readiness must leave the known-gap list")
	}
}

func TestReadinessDocumentedResponses(t *testing.T) {
	previous := readiness.Load()
	t.Cleanup(func() { readiness.Store(previous) })
	srv := &Server{store: &PacketStore{packets: []*StoreTx{{Observations: []*StoreObs{{}, {}}}}}}
	for _, withSources := range []bool{false, true} {
		statsPath := filepath.Join(t.TempDir(), "stats.json")
		if withSources {
			if err := os.WriteFile(statsPath, []byte(`{"source_liveness":{"fixture":{"lastReceiptUnix":100,"lastMessageUnix":0}}}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
		for _, ready := range []int32{0, 1} {
			readiness.Store(ready)
			w := httptest.NewRecorder()
			srv.handleHealthz(w, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
			if w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected content type: %s", w.Header().Get("Content-Type"))
			}
			var body map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if ready == 0 {
				if w.Code != http.StatusServiceUnavailable || !reflect.DeepEqual(body, map[string]interface{}{"ready": false, "reason": "loading"}) {
					t.Fatalf("unexpected loading response: status=%d body=%s", w.Code, w.Body.String())
				}
				continue
			}
			if w.Code != http.StatusOK || body["ready"] != true || body["loadedTx"] != float64(1) || body["loadedObs"] != float64(2) {
				t.Fatalf("unexpected loaded-store response: status=%d body=%s", w.Code, w.Body.String())
			}
			backfill := asMap(t, body["from_pubkey_backfill"], "backfill")
			if !reflect.DeepEqual(backfill, map[string]interface{}{"total": float64(0), "processed": float64(0), "done": true}) {
				t.Errorf("unexpected backfill compatibility values: %v", backfill)
			}
			if (body["ingest_liveness"] != nil) != withSources {
				t.Fatalf("liveness optionality does not match available stats: %s", w.Body.String())
			}
			if withSources {
				source := asMap(t, asMap(t, body["ingest_liveness"], "sources")["fixture"], "source")
				if !reflect.DeepEqual(source, map[string]interface{}{"lastReceiptUnix": float64(100), "lastMessageUnix": float64(0)}) {
					t.Errorf("unexpected liveness pair: %v", source)
				}
			}
		}
	}
}
