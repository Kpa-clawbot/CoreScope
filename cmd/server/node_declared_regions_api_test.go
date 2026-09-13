package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// declaredRegionsOf reads the declared_regions field back as a []string, and
// reports whether the field was present at all. Absent and present-but-empty
// are different answers (never asked vs answered with no named region), so
// the helper keeps them apart instead of collapsing both to nil.
func declaredRegionsOf(t *testing.T, node map[string]interface{}) ([]string, bool) {
	t.Helper()
	raw, present := node["declared_regions"]
	if !present {
		return nil, false
	}
	list, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("declared_regions is %T (%v), want a JSON array", raw, raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("declared_regions entry is %T (%v), want a string", v, v)
		}
		out = append(out, s)
	}
	return out, true
}

// TestHandleNodesExposesDeclaredRegions pins the field the map's region
// filter reads (#1862): the named regions a repeater declared, spelled the way
// the Scope Audit spells them ('#' stripped, the wildcard left out), so the
// map can answer "which repeaters handle #be" from the one bulk fetch it
// already makes.
func TestHandleNodesExposesDeclaredRegions(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES
		('PK_NAMED',     'named-rp',     'repeater',  51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, '#be,*,be-bru', '2026-01-01T00:00:00Z'),
		('PK_STARONLY',  'star-rp',      'repeater',  51.1, 4.1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, '*',            '2026-01-01T00:00:00Z'),
		('PK_NEVER',     'never-rp',     'repeater',  51.2, 4.2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL,           NULL),
		('PK_COMPANION', 'phone',        'companion', 51.3, 4.3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be,*',         '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	got, present := declaredRegionsOf(t, nodes["PK_NAMED"])
	if !present {
		t.Fatalf("declared repeater has no declared_regions field: %v", nodes["PK_NAMED"])
	}
	if want := []string{"be", "be-bru"}; !reflect.DeepEqual(got, want) {
		t.Errorf("declared_regions = %v, want %v ('#' stripped, '*' left out, list order kept)", got, want)
	}

	// Answered with only the wildcard: the answer exists and names no region.
	// That is an empty list, not an absent field.
	got, present = declaredRegionsOf(t, nodes["PK_STARONLY"])
	if !present {
		t.Errorf("wildcard-only repeater has no declared_regions field, want an empty list")
	} else if len(got) != 0 {
		t.Errorf("wildcard-only repeater declared_regions = %v, want []", got)
	}

	// Never asked: no answer to report.
	if v, present := nodes["PK_NEVER"]["declared_regions"]; present {
		t.Errorf("never-asked repeater carries declared_regions = %v, want the field absent", v)
	}

	// A companion does not forward, so a declared list on it is not something
	// the region filter should read, same rule as scope_config_state.
	if v, present := nodes["PK_COMPANION"]["declared_regions"]; present {
		t.Errorf("companion carries declared_regions = %v, want the field absent", v)
	}
}

// TestHandleNodeDetailExposesDeclaredRegions keeps the node endpoint and the
// list endpoint saying the same thing about one repeater.
func TestHandleNodeDetailExposesDeclaredRegions(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES ('PK_DETAIL_RGN', 'detail-rp', 'repeater', 51.3, 4.3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'nl,#be', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/PK_DETAIL_RGN", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node map[string]interface{} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, present := declaredRegionsOf(t, resp.Node)
	if !present {
		t.Fatalf("detail endpoint has no declared_regions field: %v", resp.Node)
	}
	if want := []string{"nl", "be"}; !reflect.DeepEqual(got, want) {
		t.Errorf("declared_regions = %v, want %v", got, want)
	}
}

// TestHandleNodesOmitsDeclaredRegionsWithoutASource: on a database that cannot
// hold a declared answer, an empty list would claim "answered, names nothing".
func TestHandleNodesOmitsDeclaredRegionsWithoutASource(t *testing.T) {
	srv, router := setupTestServer(t)
	if srv.db.hasConfiguredScope || srv.db.hasDeclaredRegionsTable {
		t.Fatal("fixture has a declared-regions source: this test would prove nothing")
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('PK_NOSOURCE_RGN', 'rp', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	if v, present := nodes["PK_NOSOURCE_RGN"]["declared_regions"]; present {
		t.Errorf("repeater carries declared_regions = %v on a schema with no declared-regions source, want the field absent", v)
	}
}

// TestScopeAuditAndNodesAgreeOnDeclaredRegions drives both real handlers over
// the same stored answers. The region filter on the map and the region search
// on the Scope Audit page must list a repeater under the same declared names,
// or searching "be" on one page and picking #be on the other give different
// repeaters.
func TestScopeAuditAndNodesAgreeOnDeclaredRegions(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)
	if _, err := srv.db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := srv.db.detectSchema(context.Background(), srv.db.conn); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"PK_RGN_HASHSTAR":  "#be,#*",
		"PK_RGN_BARESTAR":  "be,*,nl-li",
		"PK_RGN_STARONLY":  "#*",
		"PK_RGN_NAMEDONLY": "#be, de",
		"PK_RGN_EMPTY":     "",
	}
	for pk, csv := range cases {
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
			VALUES (?, ?, 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, ?, '2026-01-01T00:00:00Z')`,
			pk, "rp-"+pk, csv,
		); err != nil {
			t.Fatal(err)
		}
	}

	audit := getScopeAudit(t, router, "?window=24h")
	auditRegions := map[string][]string{}
	for _, row := range audit.Repeaters {
		auditRegions[strings.ToLower(row.PublicKey)] = row.DeclaredRegions
	}
	nodes := nodesByPubkey(t, router, "?limit=200")

	for pk, csv := range cases {
		want, ok := auditRegions[strings.ToLower(pk)]
		if !ok {
			t.Errorf("%s (%q): the scope audit does not list the repeater at all", pk, csv)
			continue
		}
		got, present := declaredRegionsOf(t, nodes[pk])
		if !present {
			t.Errorf("regions_csv %q: the scope audit lists %v, the map has no declared_regions field", csv, want)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("regions_csv %q: the scope audit lists %v, the map lists %v", csv, want, got)
		}
	}
}

// TestOpenAPINodeSchemaDocumentsDeclaredRegions keeps the served spec in step
// with the wire.
func TestOpenAPINodeSchemaDocumentsDeclaredRegions(t *testing.T) {
	spec := fetchSpec(t)
	components := asMap(t, spec["components"], "components")
	schemas := asMap(t, components["schemas"], "components.schemas")
	props := asMap(t, asMap(t, schemas["Node"], "Node")["properties"], "Node.properties")

	p, ok := props["declared_regions"]
	if !ok {
		t.Fatal("Node.properties.declared_regions missing")
	}
	pm := asMap(t, p, "declared_regions")
	if pm["type"] != "array" {
		t.Errorf("declared_regions: want type array, got %v", pm["type"])
	}
	items := asMap(t, pm["items"], "declared_regions.items")
	if items["type"] != "string" {
		t.Errorf("declared_regions.items: want type string, got %v", items["type"])
	}
}

// TestNodesCarryDeclaredRegionsTruncated: a repeater whose declared list was
// cut off returns a partial list, and the Scope Audit flags it as such. The
// node row must carry the same caveat, or the map popup shows a partial list
// (or "no named region") as the whole answer (#2022 review).
func TestNodesCarryDeclaredRegionsTruncated(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)
	createDeclaredRegionsTable(t, srv)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES
		('pk_trunc',       'trunc-rp',       'repeater',  51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL,    NULL),
		('pk_trunc_empty', 'trunc-empty-rp', 'room',      51.1, 4.1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL,    NULL),
		('pk_complete',    'complete-rp',    'repeater',  51.2, 4.2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL,    NULL),
		('pk_cfg_only',    'cfg-rp',         'repeater',  51.3, 4.3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be',    '2026-01-01T00:00:00Z'),
		('pk_newer_cfg',   'newer-rp',       'repeater',  51.4, 4.4, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be,nl', '2026-03-01T00:00:00Z'),
		('pk_companion',   'phone',          'companion', 51.5, 4.5, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL,    NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	seedSecondSource(t, srv, "pk_trunc", "2026-02-01T00:00:00Z", "#be", 1)
	seedSecondSource(t, srv, "pk_trunc_empty", "2026-02-01T00:00:00Z", "", 1)
	seedSecondSource(t, srv, "pk_complete", "2026-02-01T00:00:00Z", "#be", 0)
	// Older truncated answer, newer answer from the other source: the newest
	// answer decides, caveat included.
	seedSecondSource(t, srv, "pk_newer_cfg", "2026-02-01T00:00:00Z", "#be", 1)
	seedSecondSource(t, srv, "pk_companion", "2026-02-01T00:00:00Z", "#be", 1)

	nodes := nodesByPubkey(t, router, "?limit=200")

	for _, pk := range []string{"pk_trunc", "pk_trunc_empty"} {
		if _, present := declaredRegionsOf(t, nodes[pk]); !present {
			t.Errorf("%s: no declared_regions field", pk)
		}
		if v, present := nodes[pk]["declared_regions_truncated"]; !present || v != true {
			t.Errorf("%s: declared_regions_truncated = %v (present %v), want true", pk, v, present)
		}
	}
	for _, pk := range []string{"pk_complete", "pk_cfg_only", "pk_newer_cfg", "pk_companion"} {
		if v, present := nodes[pk]["declared_regions_truncated"]; present {
			t.Errorf("%s: declared_regions_truncated = %v, want the field absent", pk, v)
		}
	}

	req := httptest.NewRequest("GET", "/api/nodes/pk_trunc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node map[string]interface{} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v := resp.Node["declared_regions_truncated"]; v != true {
		t.Errorf("detail endpoint declared_regions_truncated = %v, want true", v)
	}
}

func TestOpenAPINodeSchemaDocumentsDeclaredRegionsTruncated(t *testing.T) {
	spec := fetchSpec(t)
	components := asMap(t, spec["components"], "components")
	schemas := asMap(t, components["schemas"], "components.schemas")
	props := asMap(t, asMap(t, schemas["Node"], "Node")["properties"], "Node.properties")

	p, ok := props["declared_regions_truncated"]
	if !ok {
		t.Fatal("Node.properties.declared_regions_truncated missing")
	}
	if pm := asMap(t, p, "declared_regions_truncated"); pm["type"] != "boolean" {
		t.Errorf("declared_regions_truncated: want type boolean, got %v", pm["type"])
	}
}
