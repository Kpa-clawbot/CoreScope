package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fetchSpec hits /api/spec and returns the decoded OpenAPI document.
func fetchSpec(t *testing.T) map[string]interface{} {
	t.Helper()
	_, r := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/spec", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/spec: expected 200, got %d", w.Code)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid spec JSON: %v", err)
	}
	return spec
}

func asMap(t *testing.T, v interface{}, what string) map[string]interface{} {
	t.Helper()
	m, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("expected object for %s, got %T", what, v)
	}
	return m
}

// TestOpenAPINodeSchema_Metrics pins issue #672 / E: the per-node usefulness
// metrics are documented in components/schemas.Node with bounded score ranges
// and an A–F grade enum — not just set on the wire.
func TestOpenAPINodeSchema_Metrics(t *testing.T) {
	spec := fetchSpec(t)
	components := asMap(t, spec["components"], "components")
	schemas := asMap(t, components["schemas"], "components.schemas")

	node, ok := schemas["Node"]
	if !ok {
		t.Fatal("components.schemas.Node missing")
	}
	props := asMap(t, asMap(t, node, "Node")["properties"], "Node.properties")

	// Each #672 score axis must be present, numeric, and bounded [0,1].
	for _, field := range []string{"traffic_share_score", "bridge_score", "coverage_score", "redundancy_score", "usefulness_score"} {
		p, ok := props[field]
		if !ok {
			t.Errorf("Node.properties.%s missing", field)
			continue
		}
		pm := asMap(t, p, field)
		if pm["type"] != "number" {
			t.Errorf("%s: want type number, got %v", field, pm["type"])
		}
		if pm["minimum"] != float64(0) || pm["maximum"] != float64(1) {
			t.Errorf("%s: want bounds [0,1], got [%v,%v]", field, pm["minimum"], pm["maximum"])
		}
		if d, _ := pm["description"].(string); !strings.Contains(d, "#672") {
			t.Errorf("%s: description should cite #672, got %q", field, d)
		}
	}

	// usefulness_grade is an A–F enum.
	grade := asMap(t, props["usefulness_grade"], "usefulness_grade")
	enum, ok := grade["enum"].([]interface{})
	if !ok || len(enum) != 5 || enum[0] != "A" || enum[4] != "F" {
		t.Errorf("usefulness_grade enum should be [A,B,C,D,F], got %v", grade["enum"])
	}

	// Relay-activity fields are documented too.
	for _, field := range []string{"relay_active", "relay_count_1h", "relay_count_24h", "unscoped_relay_count_24h", "last_relayed"} {
		if _, ok := props[field]; !ok {
			t.Errorf("Node.properties.%s missing", field)
		}
	}
}

// TestOpenAPINodeEndpoints_ReferenceSchemas verifies the three node endpoints
// advertise concrete response schemas (not the bare {"type":"object"}
// placeholder) that resolve to the Node schema.
func TestOpenAPINodeEndpoints_ReferenceSchemas(t *testing.T) {
	spec := fetchSpec(t)
	paths := asMap(t, spec["paths"], "paths")

	respRef := func(path string) string {
		p, ok := paths[path]
		if !ok {
			t.Fatalf("path %s missing", path)
		}
		get := asMap(t, asMap(t, p, path)["get"], path+".get")
		resp200 := asMap(t, asMap(t, get["responses"], path+".responses")["200"], path+".200")
		appjson := asMap(t, asMap(t, resp200["content"], path+".content")["application/json"], path+".application/json")
		schema := asMap(t, appjson["schema"], path+".schema")
		ref, _ := schema["$ref"].(string)
		return ref
	}

	cases := map[string]string{
		"/api/nodes":                    "NodeListResponse",
		"/api/nodes/{pubkey}":           "NodeDetailResponse",
		"/api/nodes/{pubkey}/neighbors": "NodeNeighborsResponse",
	}
	for path, want := range cases {
		ref := respRef(path)
		if !strings.HasSuffix(ref, "/"+want) {
			t.Errorf("%s: 200 schema should $ref %s, got %q", path, want, ref)
		}
	}

	// The list wrapper's items must resolve to the Node schema.
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	list := asMap(t, schemas["NodeListResponse"], "NodeListResponse")
	nodes := asMap(t, asMap(t, list["properties"], "props")["nodes"], "nodes")
	items := asMap(t, nodes["items"], "items")
	if ref, _ := items["$ref"].(string); !strings.HasSuffix(ref, "/Node") {
		t.Errorf("NodeListResponse.nodes.items should $ref Node, got %q", ref)
	}
}

// TestOpenAPISchema_FullCoverage pins the #1769-review fixes: the schemas must
// document every field the handlers actually emit — recentAdverts on node
// detail and counts_by_mode on neighbor entries — and the Node schema must
// allow the additional undocumented fields it carries.
func TestOpenAPISchema_FullCoverage(t *testing.T) {
	spec := fetchSpec(t)
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")

	// node detail also returns recentAdverts (array of NodeAdvert).
	detail := asMap(t, schemas["NodeDetailResponse"], "NodeDetailResponse")
	dprops := asMap(t, detail["properties"], "NodeDetailResponse.properties")
	ra, ok := dprops["recentAdverts"]
	if !ok {
		t.Fatal("NodeDetailResponse.recentAdverts missing (handler emits it)")
	}
	raItems := asMap(t, asMap(t, ra, "recentAdverts")["items"], "recentAdverts.items")
	if ref, _ := raItems["$ref"].(string); !strings.HasSuffix(ref, "/NodeAdvert") {
		t.Errorf("recentAdverts.items should $ref NodeAdvert, got %q", ref)
	}
	if _, ok := schemas["NodeAdvert"]; !ok {
		t.Error("components.schemas.NodeAdvert missing")
	}

	// neighbor entries also carry counts_by_mode (#1638).
	ne := asMap(t, schemas["NeighborEntry"], "NeighborEntry")
	nprops := asMap(t, ne["properties"], "NeighborEntry.properties")
	cbm, ok := nprops["counts_by_mode"]
	if !ok {
		t.Fatal("NeighborEntry.counts_by_mode missing (struct has CountsByMode)")
	}
	if asMap(t, cbm, "counts_by_mode")["type"] != "object" {
		t.Error("counts_by_mode should be type object with integer additionalProperties")
	}

	// Node tolerates the fields it emits but does not spell out.
	node := asMap(t, schemas["Node"], "Node")
	if node["additionalProperties"] != true {
		t.Errorf("Node should set additionalProperties:true, got %v", node["additionalProperties"])
	}
}
