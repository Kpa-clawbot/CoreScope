package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

var roleStatsDocumentedTypes = map[string]string{
	"role": "string", "nodeCount": "integer", "withSkew": "integer",
	"meanAbsSkewSec": "number", "medianAbsSkewSec": "number",
	"okCount": "integer", "warningCount": "integer", "criticalCount": "integer",
	"absurdCount": "integer", "noClockCount": "integer",
}

func TestOpenAPIAnalyticsRolesDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	path := asMap(t, asMap(t, spec["paths"], "paths")["/api/analytics/roles"], "roles path")
	op := asMap(t, path["get"], "roles GET")
	if summary, _ := op["summary"].(string); len(path) != 1 || summary == "" || summary == "/api/analytics/roles" {
		t.Errorf("expected a documented GET operation, got %v", path)
	}
	if !reflect.DeepEqual(op["tags"], []interface{}{"analytics"}) || op["security"] != nil || spec["security"] != nil {
		t.Errorf("expected public analytics operation, got %v", op)
	}
	if params, _ := op["parameters"].([]interface{}); len(params) != 0 {
		t.Errorf("roles handler reads no query parameters, got %v", params)
	}
	desc, _ := op["description"].(string)
	if strings.TrimSpace(desc) == "" {
		t.Error("roles operation must describe its response semantics")
	}
	if loadKnownGaps(t)["/api/analytics/roles"] {
		t.Error("documented roles route must leave the known-gap list")
	}
	response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
	media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
	if asMap(t, media["schema"], "response schema")["$ref"] != "#/components/schemas/RoleAnalyticsResponse" {
		t.Error("roles GET must reference a concrete RoleAnalyticsResponse schema")
	}
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	schema := asMap(t, schemas["RoleAnalyticsResponse"], "RoleAnalyticsResponse")
	props := asMap(t, schema["properties"], "response properties")
	totalNodes := asMap(t, props["totalNodes"], "totalNodes")
	required, _ := schema["required"].([]interface{})
	if schema["type"] != "object" || len(props) != 2 || len(required) != 2 ||
		!slices.Contains(required, interface{}("totalNodes")) || !slices.Contains(required, interface{}("roles")) ||
		totalNodes["type"] != "integer" || totalNodes["nullable"] == true {
		t.Errorf("expected required non-null totalNodes integer and roles array: %v", schema)
	}
	roles := asMap(t, props["roles"], "roles")
	if roles["type"] != "array" || roles["nullable"] == true {
		t.Errorf("roles must be a non-null array, including for an empty roster: %v", roles)
	}
	items := asMap(t, roles["items"], "role item")
	fields := asMap(t, items["properties"], "role properties")
	required, _ = items["required"].([]interface{})
	if items["type"] != "object" || len(fields) != len(roleStatsDocumentedTypes) || len(required) != len(roleStatsDocumentedTypes) {
		t.Errorf("expected all RoleStats fields: %v", items)
	}
	for name, typ := range roleStatsDocumentedTypes {
		field := asMap(t, fields[name], name)
		if field["type"] != typ || field["nullable"] == true || !slices.Contains(required, interface{}(name)) {
			t.Errorf("expected required non-null %s %s: %v", typ, name, field)
		}
		if description, _ := field["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("missing description for %s", name)
		}
	}
	if asMap(t, fields["role"], "role")["enum"] != nil {
		t.Error("normalized role strings are not a closed enum")
	}
}

func TestAnalyticsRolesDocumentedContract(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	cfg := &Config{APIKey: "test-roles-key", Areas: map[string]AreaEntry{
		"inside": {Polygon: [][2]float64{{0, 0}, {0, 2}, {2, 2}, {2, 0}}},
	}}
	srv := NewServer(db, cfg, NewHub())
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	get := func(t *testing.T, query string, want RoleAnalyticsResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/analytics/roles"+query, nil))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("roles response: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
		}
		var got RoleAnalyticsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("roles response = %+v, want %+v", got, want)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
			t.Fatal(err)
		}
		if count, ok := wire["totalNodes"]; !ok || string(count) == "null" {
			t.Errorf("totalNodes must be present with an exact key and a non-null integer: %s", w.Body.String())
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(wire["roles"], &rows); err != nil || rows == nil {
			t.Fatalf("roles must be present with an exact key and a non-null array: %s (decode error: %v)", w.Body.String(), err)
		}
		for _, row := range rows {
			for field := range roleStatsDocumentedTypes {
				if value, ok := row[field]; !ok || string(value) == "null" {
					t.Errorf("RoleStats must emit non-null %s, including zero values: %v", field, row)
				}
			}
		}
	}
	t.Run("no store", func(t *testing.T) {
		get(t, "", RoleAnalyticsResponse{Roles: []RoleStats{}})
	})
	srv.store = NewPacketStore(db, nil)
	t.Run("empty roster", func(t *testing.T) {
		get(t, "", RoleAnalyticsResponse{Roles: []RoleStats{}})
	})
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES
		('aaaa', 'Inside', 'repeater', 1, 1), ('bbbb', 'Outside', 'repeater', 10, 10),
		('cccc', 'Room', 'room', 10, 10), ('dddd', 'Unknown', '', 20, 20)`)
	srv.store.InvalidateNodeCache()
	want := RoleAnalyticsResponse{TotalNodes: 4, Roles: []RoleStats{
		{Role: "repeater", NodeCount: 2}, {Role: "room", NodeCount: 1}, {Role: "unknown", NodeCount: 1},
	}}
	t.Run("populated roster", func(t *testing.T) { get(t, "", want) })
	t.Run("query filters ignored", func(t *testing.T) {
		get(t, "?area=inside&region=missing&role=missing&hours=1&observer=missing", want)
	})
}
