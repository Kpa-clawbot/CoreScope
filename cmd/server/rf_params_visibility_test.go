package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRfParamsVisibility(t *testing.T) {
	for _, policy := range []string{"blacklist", "hidden-name"} {
		for _, initiallyHidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/initially-hidden=%t", policy, initiallyHidden), func(t *testing.T) {
				srv, router := setupTestServer(t)
				t.Cleanup(func() {
					if err := srv.db.Close(); err != nil {
						t.Errorf("close test database: %v", err)
					}
				})
				target := strings.Repeat("ab", 32)
				control := strings.Repeat("cd", 32)
				const targetName = "RF-hidden-fixture"
				pks := []string{target, control, strings.Repeat("01", 32), strings.Repeat("02", 32), strings.Repeat("03", 32)}
				now := time.Now().UTC()
				snap := &rfParamsSnapshot{FetchedAt: now, Source: "synthetic-only", Nodes: map[string]RfParams{}}
				for i, pk := range pks {
					name := fmt.Sprintf("RF-visible-fixture-%d", i)
					if pk == target {
						name = targetName
					}
					if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen)
						VALUES (?, ?, 'repeater', 1, 1, ?)`, pk, name, now.Format(time.RFC3339)); err != nil {
						t.Fatal(err)
					}
					sf := 8
					if i < 2 {
						sf = 7 // Both target and visible control deviate from three nearby peers.
					}
					snap.Nodes[pk] = RfParams{Freq: 869.618, SF: sf, BW: 62.5, CR: 8, LastAdvert: now.Format(time.RFC3339)}
				}
				srv.store.InvalidateNodeCache()
				cache := newRfParamsCache(&RfParamsConfig{NodesURL: "http://unused.invalid"})
				cache.ptr.Store(snap)
				srv.rfParams = cache

				request := func(path string) *httptest.ResponseRecorder {
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
					return w
				}
				setHidden := func(hidden bool) {
					var entries []string
					if hidden {
						entries = []string{"RF-hidden-"}
						if policy == "blacklist" {
							entries = []string{" " + strings.ToUpper(target) + " "}
						}
					}
					if policy == "blacklist" {
						srv.cfg.SetNodeBlacklist(entries)
					} else {
						srv.cfg.SetHiddenNamePrefixes(entries)
					}
				}
				check := func(stage string, hidden bool) {
					t.Helper()
					wantStatus, wantNodes, wantDeviations := http.StatusOK, 5, 2
					if hidden {
						wantStatus, wantNodes, wantDeviations = http.StatusNotFound, 4, 1
					}
					if w := request("/api/nodes/" + target); w.Code != wantStatus {
						t.Fatalf("%s: node detail status = %d, want %d", stage, w.Code, wantStatus)
					}
					w := request("/api/rf-params")
					if w.Code != http.StatusOK {
						t.Fatalf("%s: RF status = %d, want 200", stage, w.Code)
					}
					var resp RfParamsResponse
					if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
						t.Fatal(err)
					}
					if !resp.Enabled || resp.FetchedAt == nil || resp.Source != snap.Source {
						t.Errorf("%s: RF snapshot metadata missing", stage)
					}
					if _, present := resp.Nodes[target]; present == hidden || len(resp.Nodes) != wantNodes {
						t.Errorf("%s: target in nodes = %t, nodes = %d; want %t, %d", stage, present, len(resp.Nodes), !hidden, wantNodes)
					}
					if _, present := resp.Deviations[target]; present == hidden || len(resp.Deviations) != wantDeviations {
						t.Errorf("%s: target in deviations = %t, deviations = %d; want %t, %d", stage, present, len(resp.Deviations), !hidden, wantDeviations)
					}
					if p, present := resp.Nodes[control]; !present || p != snap.Nodes[control] {
						t.Errorf("%s: visible control RF metadata lost", stage)
					}
					if d, present := resp.Deviations[control]; !present || d.Majority.SF != 8 || d.Agree != 3 {
						t.Errorf("%s: visible control deviation lost: %+v", stage, d)
					}
				}

				if w := request("/api/nodes/" + target); w.Code != http.StatusOK {
					t.Fatalf("visible node control = %d, want 200", w.Code)
				}
				setHidden(initiallyHidden)
				check("first request", initiallyHidden)
				cachedAt := cache.deviations.at
				setHidden(true)
				check("hidden with warm caches", true)
				setHidden(false)
				check("unhidden with warm caches", false)
				if !cache.deviations.at.Equal(cachedAt) {
					t.Error("visibility changes recomputed the deviations cache")
				}
				if len(snap.Nodes) != 5 || len(cache.deviations.res) != 2 {
					t.Error("visibility filtering mutated a shared RF snapshot or deviations cache")
				}
			})
		}
	}
}
