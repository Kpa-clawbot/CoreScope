package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNodesHasMoreSurvivesPostLimitFiltering pins the contract that lets a
// client paginate /api/nodes safely.
//
// handleNodes applies the geo-filter, blacklist, hidden-prefix and area passes
// AFTER the SQL LIMIT/OFFSET, and rewrites Total to the filtered length. So a
// page that loses a row is short without being the last page, and neither the
// page length nor Total can tell a client whether to ask for another page.
// has_more is computed from the raw SQL page against the real COUNT(*), before
// those passes run, and must therefore stay true on a page that filtering
// shortened.
//
// Anti-tautology: move the hasMore assignment below the filter block (or
// compute it from the filtered slice) and the page-1 assertion fails — that is
// exactly the arrangement that stranded every node behind a filtered row.
func TestNodesHasMoreSurvivesPostLimitFiltering(t *testing.T) {
	srv, router := setupTestServer(t)

	// setupTestServer seeds its own fixture nodes; clear them so the page
	// boundaries below are exactly the ones this test sets up.
	if _, err := srv.db.conn.Exec(`DELETE FROM nodes`); err != nil {
		t.Fatalf("clear fixture nodes: %v", err)
	}

	// 7 nodes, newest first by last_seen so page order is deterministic.
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("visible-%d", i)
		if i == 1 {
			name = "🚫 hidden-1" // lands inside page 1 (offset 0, limit 3)
		}
		lastSeen := fmt.Sprintf("2026-06-0%dT00:00:00Z", 7-i)
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, 'repeater', 0, 0, ?, '2026-06-01T00:00:00Z', 1)`,
			fmt.Sprintf("deadbeef0000200%d", i), name, lastSeen); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	page := func(offset int) NodeListResponse {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/nodes?limit=3&offset=%d", offset), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("offset %d: status %d body=%s", offset, w.Code, w.Body.String())
		}
		var got NodeListResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("offset %d: decode: %v", offset, err)
		}
		return got
	}

	// Page 1 is a row short because the hidden node was dropped after the LIMIT.
	first := page(0)
	if len(first.Nodes) != 2 {
		t.Fatalf("page 1: expected 2 rows after filtering, got %d", len(first.Nodes))
	}
	if !first.HasMore {
		t.Fatalf("page 1: has_more must stay true on a page shortened by filtering " +
			"(4 more nodes are waiting) — a client stopping here strands them")
	}

	// Walking has_more reaches every visible node, including the last page.
	seen := map[string]bool{}
	for offset, more := 0, true; more && offset < 100; offset += 3 {
		p := page(offset)
		for _, n := range p.Nodes {
			pk, _ := n["public_key"].(string)
			seen[pk] = true
		}
		more = p.HasMore
	}
	if len(seen) != 6 {
		t.Fatalf("expected all 6 visible nodes across the walk, got %d", len(seen))
	}

	// The final page reports has_more=false rather than relying on a short page.
	if last := page(6); last.HasMore {
		t.Fatalf("final page: has_more must be false, got true")
	}
}
