package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleNodes_PostFilterDoesNotTruncatePagination is a regression test
// for a real bug found on stg.meshview.dk: geo_filter, node blacklist, and
// hidden-name-prefix are all applied to a page AFTER the SQL LIMIT already
// fixed its size, so a page that was genuinely full at the DB layer could
// come back shorter than requested. The frontend's pagination loop
// (fetchAllNodes, public/app.js) treats "page shorter than requested" as
// "this was the last page" and stops fetching — so a single hidden-prefix
// node anywhere within a page used to silently truncate every node after
// it. On stg this reduced the live map from ~1300 GPS-valid nodes to ~450.
//
// This seeds 5 real nodes with one hidden-prefix node positioned inside
// the first page (limit=3), and asserts a limit=3 request still returns 3
// real nodes (not 2), and that walking the full "keep fetching while the
// page came back exactly `limit` long" pagination loop reaches every real
// node.
func TestHandleNodes_PostFilterDoesNotTruncatePagination(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM nodes`); err != nil {
		t.Fatalf("clear nodes: %v", err)
	}

	// 6 real nodes + 1 hidden-prefix node, each with a distinct last_seen
	// so GetNodes' default order (last_seen DESC) is deterministic. The
	// hidden node is 3rd-most-recent, landing inside the first limit=3
	// page (offset=0) — exactly the scenario that used to truncate.
	insert := func(pk, name, lastSeen string) {
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, 'repeater', 55.0, 10.0, ?, '2026-06-01T00:00:00Z', 1)`,
			pk, name, lastSeen); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	insert("pk0000000000000a", "Node-A", "2026-06-07T00:00:00Z")
	insert("pk0000000000000b", "Node-B", "2026-06-06T00:00:00Z")
	insert("pk0000000000000h", "🚫 Hidden", "2026-06-05T00:00:00Z") // 3rd-most-recent
	insert("pk0000000000000c", "Node-C", "2026-06-04T00:00:00Z")
	insert("pk0000000000000d", "Node-D", "2026-06-03T00:00:00Z")
	insert("pk0000000000000e", "Node-E", "2026-06-02T00:00:00Z")
	insert("pk0000000000000f", "Node-F", "2026-06-01T00:00:00Z")

	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	fetchPage := func(limit, offset int) []map[string]interface{} {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/nodes?limit=%d&offset=%d", limit, offset), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Nodes []map[string]interface{} `json:"nodes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v body=%s", err, w.Body.String())
		}
		return resp.Nodes
	}

	// The bug: a naive single-DB-page fetch of limit=3 starting at offset=0
	// pulls Node-A, Node-B, 🚫Hidden — filters Hidden out post-LIMIT — and
	// used to return only 2 rows even though Node-C exists right behind it.
	page1 := fetchPage(3, 0)
	if len(page1) != 3 {
		names := make([]string, len(page1))
		for i, n := range page1 {
			names[i], _ = n["name"].(string)
		}
		t.Fatalf("expected 3 real nodes in the first page (compensating for the hidden node), got %d: %v", len(page1), names)
	}
	for _, n := range page1 {
		name, _ := n["name"].(string)
		if name == "🚫 Hidden" {
			t.Fatalf("hidden-prefix node leaked into the response: %+v", n)
		}
	}

	// Walk the client's actual pagination contract (fetchAllNodes,
	// public/app.js): keep requesting while the page came back exactly
	// `limit` long; stop on a short page. Must reach every real node.
	const limit = 3
	seen := map[string]bool{}
	offset := 0
	for i := 0; i < 10; i++ { // safety bound
		page := fetchPage(limit, offset)
		for _, n := range page {
			pk, _ := n["public_key"].(string)
			seen[pk] = true
		}
		if len(page) < limit {
			break
		}
		offset += limit
	}
	wantKeys := []string{
		"pk0000000000000a", "pk0000000000000b", "pk0000000000000c",
		"pk0000000000000d", "pk0000000000000e", "pk0000000000000f",
	}
	for _, k := range wantKeys {
		if !seen[k] {
			t.Errorf("pagination never reached node %s — truncated by the hidden-prefix post-filter bug", k)
		}
	}
	if seen["pk0000000000000h"] {
		t.Error("hidden-prefix node's pubkey should never appear across any page")
	}
}

// TestHandleNodes_PostFilterCompensationLoopLogsOnExhaustion covers the
// bounded worst case of the compensation loop added above: if a post-filter
// drops an extreme, sustained run of consecutive rows (more than
// maxIterations*limit), the loop gives up rather than scanning forever —
// but must leave a log breadcrumb so a short response is diagnosable
// instead of silently under-filling the page (bot review MINOR, PR #1852).
func TestHandleNodes_PostFilterCompensationLoopLogsOnExhaustion(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM nodes`); err != nil {
		t.Fatalf("clear nodes: %v", err)
	}

	// With limit=1, maxIterations=50 caps the loop at 50 DB pages. Seed 51
	// consecutive hidden nodes (so every page in that window filters to
	// zero) followed by one real node the loop should never reach.
	for i := 0; i < 51; i++ {
		pk := fmt.Sprintf("pkhidden%056d", i)
		lastSeen := fmt.Sprintf("2026-06-01T%02d:00:00Z", 23-(i%24))
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, '🚫 Hidden', 'repeater', 55.0, 10.0, ?, '2026-05-01T00:00:00Z', 1)`,
			pk, lastSeen); err != nil {
			t.Fatalf("insert hidden %d: %v", i, err)
		}
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('pkreal0000000000000000000000000000000000000000000000000001', 'RealNode', 'repeater', 55.0, 10.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	); err != nil {
		t.Fatalf("insert real node: %v", err)
	}
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	req := httptest.NewRequest("GET", "/api/nodes?limit=1&offset=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(resp.Nodes) != 0 {
		t.Errorf("expected 0 nodes (RealNode is beyond the 50-iteration cap), got %d: %+v", len(resp.Nodes), resp.Nodes)
	}
	if !strings.Contains(buf.String(), "maxIterations") {
		t.Errorf("expected a log breadcrumb mentioning maxIterations when the compensation loop is exhausted, got log output: %q", buf.String())
	}
}
