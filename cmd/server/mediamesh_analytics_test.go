package main

import (
	"testing"
	"time"
)

// MediaMesh analytics (/api/analytics/mediamesh) and the radio2 split of the
// relay-airtime share. Seed data (db_test.go): tx 1 (ADVERT, observations 1
// and 2), tx 2 (CHAN, observation 3), tx 3 (ADVERT, observation 4).

func TestMediaMeshAnalytics(t *testing.T) {
	srv, router := setupTestServer(t)
	seedMediaMesh(t, srv,
		`INSERT INTO observation_mediamesh (observation_id) VALUES (2)`,
		`INSERT INTO node_mediamesh VALUES ('aabbccdd11223344', 'rxtx', '911.15,250,7,5', 'status', '`+mmRecentTS()+`')`,
	)

	var got MediaMeshAnalytics
	getJSON(t, router, "/api/analytics/mediamesh", &got)

	if got.TotalTransmissions != 3 || got.MediaMeshTransmissions != 1 {
		t.Fatalf("transmissions: total %d, mediamesh %d; want 3, 1", got.TotalTransmissions, got.MediaMeshTransmissions)
	}
	if got.SharePct < 33.3 || got.SharePct > 33.4 {
		t.Errorf("share = %v, want 33.3", got.SharePct)
	}
	byType := map[string]MediaMeshTypeCount{}
	for _, c := range got.ByType {
		byType[c.Name] = c
	}
	if a := byType["ADVERT"]; a.Count != 2 || a.MediaMesh != 1 {
		t.Errorf("ADVERT = %+v, want 2 with 1 on radio2", a)
	}
	if c := byType["GRP_TXT"]; c.MediaMesh != 0 {
		t.Errorf("GRP_TXT on radio2 = %d, want 0", c.MediaMesh)
	}
	if len(got.PerHour) != 1 || got.PerHour[0].Count != 1 {
		t.Errorf("per hour = %+v, want one hour with 1", got.PerHour)
	}
	if len(got.Observers) != 1 || got.Observers[0].MediaMeshObs != 1 {
		t.Fatalf("observers = %+v, want the one observer that heard radio2", got.Observers)
	}
	if got.Observers[0].TotalObs < 1 || got.Observers[0].SharePct <= 0 || got.Observers[0].LastHeard == "" {
		t.Errorf("observer row = %+v", got.Observers[0])
	}
	if len(got.Repeaters) != 1 || got.Repeaters[0].PublicKey != "aabbccdd11223344" ||
		got.Repeaters[0].Name != "TestRepeater" || got.Repeaters[0].Mode != "rxtx" || got.Repeaters[0].Radio != "911.15,250,7,5" {
		t.Errorf("repeaters = %+v", got.Repeaters)
	}
}

// A mesh without MediaMesh gets zeros and empty lists, never nulls.
func TestMediaMeshAnalyticsEmpty(t *testing.T) {
	srv, router := setupTestServer(t)
	withMediaMesh(t)
	refreshMediaMesh(srv.db.conn, true)
	var raw map[string]interface{}
	getJSON(t, router, "/api/analytics/mediamesh", &raw)
	if raw["mediamesh_transmissions"].(float64) != 0 || raw["total_transmissions"].(float64) != 3 {
		t.Errorf("counts = %v", raw)
	}
	for _, k := range []string{"by_type", "per_hour", "observers", "repeaters"} {
		if _, ok := raw[k].([]interface{}); !ok {
			t.Errorf("%s = %v, want a list", k, raw[k])
		}
	}
}

// Relay airtime share: rows with radio2 traffic gain mediamesh_count and
// mediamesh_airtime_pct; the others are unchanged.
func TestRelayAirtimeShareMediaMeshSplit(t *testing.T) {
	srv, _ := setupTestServer(t)
	seedMediaMesh(t, srv, `INSERT INTO observation_mediamesh (observation_id) VALUES (2)`)
	res := srv.store.computeRelayAirtimeShare(TimeWindow{})
	rows, _ := res["rows"].([]map[string]interface{})
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	tagged := 0
	for _, r := range rows {
		c, has := r["mediamesh_count"]
		if !has {
			if _, pct := r["mediamesh_airtime_pct"]; pct {
				t.Errorf("row %v has a radio2 share without a count", r["payload_type"])
			}
			continue
		}
		tagged++
		if r["payload_type"] != "ADVERT" || c.(int) != 1 {
			t.Errorf("tagged row %v count %v, want ADVERT 1", r["payload_type"], c)
		}
	}
	if tagged != 1 {
		t.Errorf("%d rows tagged, want 1", tagged)
	}
	if _, ok := res["mediamesh_score"].(int64); !ok {
		t.Errorf("mediamesh_score = %T", res["mediamesh_score"])
	}
}

// Rule 0: one pass over the store with a set lookup per observation.
func BenchmarkComputeMediaMeshAnalytics(b *testing.B) {
	store := &PacketStore{rfCache: map[string]*cachedResult{}}
	for i := 0; i < 30000; i++ {
		pt := i % 10
		tx := &StoreTx{ID: i, Hash: string(rune('a'+i%26)) + time.Duration(i).String(), PayloadType: &pt, FirstSeen: "2026-10-30T14:00:00Z"}
		for j := 0; j < 3; j++ {
			tx.Observations = append(tx.Observations, &StoreObs{ID: i*3 + j, ObserverID: "obs" + string(rune('a'+j)), Timestamp: "2026-10-30T14:00:01Z"})
		}
		store.packets = append(store.packets, tx)
	}
	withMediaMesh(b)
	mediaMeshMu.Lock()
	for i := 0; i < 90000; i += 50 {
		mediaMeshObs[i] = struct{}{}
	}
	mediaMeshMu.Unlock()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.computeMediaMeshAnalytics("", TimeWindow{})
	}
}
