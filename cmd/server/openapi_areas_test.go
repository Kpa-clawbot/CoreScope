package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Inspect JSON served by /api/spec, independently of the production schema type.
func TestOpenAPIConfigAreasDocumentation(t *testing.T) {
	spec := fetchSpec(t)
	paths := asMap(t, spec["paths"], "paths")
	schemas := asMap(t, asMap(t, spec["components"], "components")["schemas"], "schemas")
	if spec["security"] != nil {
		t.Fatal("area routes must not inherit a global authentication requirement")
	}
	for _, tc := range []struct {
		path, schema string
		geometry     bool
		terms        []string
	}{
		{"/api/config/areas", "ConfigAreasResponse", false, []string{"non-empty", "label"}},
		{"/api/config/areas/polygons", "ConfigAreaPolygonsResponse", true, []string{"unlabeled", "omitted", "zero"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := asMap(t, paths[tc.path], tc.path)
			op := asMap(t, path["get"], "get")
			if len(path) != 1 || op["summary"] == "" || op["summary"] == tc.path {
				t.Errorf("expected a documented GET operation: %v", path)
			}
			if op["security"] != nil || !reflect.DeepEqual(op["tags"], []interface{}{"config"}) {
				t.Errorf("expected a public config operation: %v", op)
			}
			if params, ok := op["parameters"]; ok && !reflect.DeepEqual(params, []interface{}{}) {
				t.Errorf("area configuration endpoints have no parameters: %v", params)
			}
			description, _ := op["description"].(string)
			for _, term := range append(tc.terms, "empty array", "order", "query parameters") {
				if !strings.Contains(description, term) {
					t.Errorf("description must explain %q: %q", term, description)
				}
			}
			response := asMap(t, asMap(t, op["responses"], "responses")["200"], "200")
			media := asMap(t, asMap(t, response["content"], "content")["application/json"], "JSON")
			if asMap(t, media["schema"], "response schema")["$ref"] != "#/components/schemas/"+tc.schema {
				t.Errorf("expected concrete %s response schema", tc.schema)
			}
			schema := asMap(t, schemas[tc.schema], tc.schema)
			if schema["type"] != "array" || schema["nullable"] == true {
				t.Errorf("expected a non-null array response: %v", schema)
			}
			item := asMap(t, schema["items"], "area entry")
			props := asMap(t, item["properties"], "area properties")
			required, _ := item["required"].([]interface{})
			if item["type"] != "object" || len(required) != 2 || !slices.Contains(required, interface{}("key")) || !slices.Contains(required, interface{}("label")) {
				t.Errorf("area entry must require key and label: %v", item)
			}
			fields := map[string]string{"key": "string", "label": "string"}
			if tc.geometry {
				fields["polygon"] = "array"
				for _, name := range []string{"latMin", "latMax", "lonMin", "lonMax"} {
					fields[name] = "number"
				}
			}
			if len(props) != len(fields) {
				t.Errorf("unexpected area properties: %v", props)
			}
			for name, kind := range fields {
				property := asMap(t, props[name], name)
				if property["type"] != kind || property["nullable"] == true {
					t.Errorf("wrong type or nullability for %s: %v", name, property)
				}
			}
			if tc.geometry {
				polygon := asMap(t, props["polygon"], "polygon")
				if minimum, _ := polygon["minItems"].(float64); minimum > 1 {
					t.Error("the geometry endpoint returns short configured polygons without validation")
				}
				pair := asMap(t, polygon["items"], "coordinate pair")
				if pair["type"] != "array" || pair["minItems"] != float64(2) || pair["maxItems"] != float64(2) || asMap(t, pair["items"], "coordinate")["type"] != "number" {
					t.Errorf("polygon coordinates must be pairs of exactly two numbers: %v", pair)
				}
				if desc, _ := pair["description"].(string); !strings.Contains(desc, "[latitude, longitude]") {
					t.Errorf("coordinate order must be explicit: %q", desc)
				}
			}
			if loadKnownGaps(t)[tc.path] {
				t.Error("documented area route must leave the known-gap list")
			}
		})
	}
}

func TestConfigAreasDocumentedResponses(t *testing.T) {
	srv, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")
	for _, tc := range []struct {
		name, config, areas, polygons string
	}{
		{"absent", `{}`, `{}`, `{}`},
		{"empty", `{"areas":{}}`, `{}`, `{}`},
		{
			"configured",
			`{"areas":{
				"polygon":{"label":"Polygon fixture","polygon":[[10,20],[11,20],[10,21]]},
				"box":{"label":"Box fixture","latMin":0,"latMax":2,"lonMin":0,"lonMax":3},
				"partial":{"label":"Partial fixture","latMax":0},
				"both":{"label":"Both fixture","polygon":[[10,20]],"lonMin":0},
				"unlabeled":{"label":"","polygon":[[1,2],[3,4]]},
				"_named":{"label":"Underscore fixture"},
				"_comment":{},
				"empty-polygon":{"label":"Empty polygon fixture","polygon":[]},
				"whitespace":{"label":" "}
			}}`,
			`{
				"polygon":{"key":"polygon","label":"Polygon fixture"},
				"box":{"key":"box","label":"Box fixture"},
				"partial":{"key":"partial","label":"Partial fixture"},
				"both":{"key":"both","label":"Both fixture"},
				"_named":{"key":"_named","label":"Underscore fixture"},
				"empty-polygon":{"key":"empty-polygon","label":"Empty polygon fixture"},
				"whitespace":{"key":"whitespace","label":" "}
			}`,
			`{
				"polygon":{"key":"polygon","label":"Polygon fixture","polygon":[[10,20],[11,20],[10,21]]},
				"box":{"key":"box","label":"Box fixture","latMin":0,"latMax":2,"lonMin":0,"lonMax":3},
				"partial":{"key":"partial","label":"Partial fixture","latMax":0},
				"both":{"key":"both","label":"Both fixture","polygon":[[10,20]],"lonMin":0},
				"unlabeled":{"key":"unlabeled","label":"","polygon":[[1,2],[3,4]]},
				"_named":{"key":"_named","label":"Underscore fixture"},
				"_comment":{"key":"_comment","label":""},
				"empty-polygon":{"key":"empty-polygon","label":"Empty polygon fixture"},
				"whitespace":{"key":"whitespace","label":" "}
			}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.config), &cfg); err != nil {
				t.Fatal(err)
			}
			srv.cfg.Areas = cfg.Areas
			for _, endpoint := range []struct{ path, want string }{
				{"/api/config/areas", tc.areas},
				{"/api/config/areas/polygons", tc.polygons},
			} {
				t.Run(endpoint.path, func(t *testing.T) {
					// No credentials; unrelated query parameters must not filter config.
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, endpoint.path+"?area=unknown", nil))
					if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
						t.Fatalf("expected public JSON response, got %d %s: %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
					}
					var rows []map[string]interface{}
					if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
						t.Fatal(err)
					}
					if rows == nil {
						t.Fatal("expected an array, not null")
					}
					// Map iteration order is not part of either endpoint's contract.
					got := make(map[string]map[string]interface{}, len(rows))
					for _, row := range rows {
						key, ok := row["key"].(string)
						if !ok || got[key] != nil {
							t.Fatalf("missing or duplicate key: %v", row)
						}
						got[key] = row
					}
					var want map[string]map[string]interface{}
					if err := json.Unmarshal([]byte(endpoint.want), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("response differs from the documented fields and omission rules:\ngot %v\nwant %v", got, want)
					}
				})
			}
		})
	}
}
