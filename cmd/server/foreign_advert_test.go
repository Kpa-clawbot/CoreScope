package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleNodes_ExposesForeignAdvertField asserts the /api/nodes response
// surfaces the foreign_advert column as a boolean `foreign` field on each
// node, so operators can see bridged/leaked nodes (#730).
func TestHandleNodes_ExposesForeignAdvertField(t *testing.T) {
	srv, router := setupTestServer(t)
	conn := srv.db.conn

	if _, err := conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, foreign_advert)
		VALUES
		('PK_LOCAL', 'local-node', 'companion', 37.0, -122.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 0),
		('PK_FOREIGN', 'foreign-node', 'companion', 50.0, 10.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 1)`,
	); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes?limit=100", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, n := range resp.Nodes {
		pk, _ := n["public_key"].(string)
		f, ok := n["foreign"].(bool)
		if !ok {
			t.Errorf("node %s: missing/non-bool 'foreign' field, got %T %v", pk, n["foreign"], n["foreign"])
			continue
		}
		got[pk] = f
	}
	if !got["PK_LOCAL"] == false || got["PK_LOCAL"] != false {
		t.Errorf("PK_LOCAL foreign=%v, want false", got["PK_LOCAL"])
	}
	if got["PK_FOREIGN"] != true {
		t.Errorf("PK_FOREIGN foreign=%v, want true", got["PK_FOREIGN"])
	}
}
