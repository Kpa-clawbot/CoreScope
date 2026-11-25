package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestNodeHealthPathHops(t *testing.T) {
	srv, router := setupTestServer(t)
	const key = "aabbccdd11223344" // seeded node
	type entry struct {
		PublicKey string `json:"public_key"`
		Stats     struct {
			AvgHops  *int `json:"avgHops"`
			PathHops *struct {
				Median float64 `json:"median"`
				P90    int     `json:"p90"`
			} `json:"pathHops"`
		} `json:"stats"`
	}
	get := func(path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		lengths []int
		avg     int
		median  float64
		p90     int
	}{
		{"no transmissions", nil, 0, 0, 0},
		{"empty paths", []int{0, 0}, 0, 0, 0},
		{"single path", []int{0, 5}, 5, 5, 5},
		{"odd sample", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20}, 7, 6, 10},
		// Ten nonempty paths. The ninth is 8 and the maximum is 30.
		{"mixed paths", []int{30, 0, 1, 4, 1, 1, 8, 4, 1, 4, 1}, 6, 2.5, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var transmissions []*StoreTx
			for _, n := range tc.lengths {
				path, err := json.Marshal(slices.Repeat([]string{"ab"}, n))
				if err != nil {
					t.Fatal(err)
				}
				tx := &StoreTx{Observations: []*StoreObs{{PathJSON: string(path)}}}
				if n > 1 {
					// A shorter copy of the same transmission is not a second sample.
					tx.Observations = append(tx.Observations, &StoreObs{PathJSON: `["ab"]`})
				}
				tx.ObservationCount = len(tx.Observations)
				pickBestObservation(tx)
				transmissions = append(transmissions, tx)
			}
			srv.store.mu.Lock()
			srv.store.byNode[key] = transmissions
			srv.store.mu.Unlock()

			var node entry
			get("/api/nodes/"+key+"/health", &node)
			if node.Stats.AvgHops == nil || *node.Stats.AvgHops != tc.avg {
				t.Errorf("legacy avgHops must be present and equal %d", tc.avg)
			}
			hops := node.Stats.PathHops
			if tc.p90 == 0 {
				if hops != nil {
					t.Errorf("want no pathHops for an empty sample, got %+v", hops)
				}
			} else if hops == nil || hops.Median != tc.median || hops.P90 != tc.p90 {
				t.Errorf("pathHops = %+v, want median %g and p90 %d", hops, tc.median, tc.p90)
			}
		})
	}
	var bulk []entry
	get("/api/nodes/bulk-health", &bulk)
	if !slices.ContainsFunc(bulk, func(node entry) bool { return node.PublicKey == key }) {
		t.Fatal("expected the sampled node in bulk health")
	}
	for _, node := range bulk {
		if node.Stats.AvgHops != nil || node.Stats.PathHops != nil {
			t.Errorf("bulk health unexpectedly includes hop statistics for %s", node.PublicKey)
		}
	}
}
