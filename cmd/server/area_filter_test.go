package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func mustExecDB(t *testing.T, db *DB, q string) {
	t.Helper()
	if _, err := db.conn.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestAreaEntryParsing(t *testing.T) {
	raw := `{
		"port": 3000,
		"areas": {
			"BEL": {
				"label": "Belgium",
				"polygon": [[50.0, 2.5], [51.5, 2.5], [51.5, 6.4], [50.0, 6.4]]
			},
			"BOX": {
				"label": "Bounding Box Area",
				"latMin": 50.0, "latMax": 51.5, "lonMin": 2.5, "lonMax": 6.4
			}
		}
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Areas) != 2 {
		t.Fatalf("want 2 areas, got %d", len(cfg.Areas))
	}
	bel := cfg.Areas["BEL"]
	if bel.Label != "Belgium" {
		t.Errorf("label: want Belgium, got %q", bel.Label)
	}
	if len(bel.Polygon) != 4 {
		t.Errorf("polygon: want 4 points, got %d", len(bel.Polygon))
	}
	box := cfg.Areas["BOX"]
	if box.LatMin == nil || *box.LatMin != 50.0 {
		t.Error("LatMin not parsed")
	}
}

func TestGetNodePubkeysInArea_Polygon(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk-inside', 50.85, 4.35)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk-outside', 48.0, 4.35)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk-nogps', NULL, NULL)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk-zero', 0.0, 0.0)`)

	entry := AreaEntry{
		Label:   "Belgium",
		Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}},
	}
	pks, err := db.GetNodePubkeysInArea(entry)
	if err != nil {
		t.Fatalf("GetNodePubkeysInArea: %v", err)
	}
	if len(pks) != 1 || pks[0] != "pk-inside" {
		t.Errorf("want [pk-inside], got %v", pks)
	}
}

// newTestStoreWithDB builds a minimal PacketStore wired to the given DB and config.
func newTestStoreWithDB(t *testing.T, db *DB, cfg *Config) *PacketStore {
	t.Helper()
	return &PacketStore{
		db:             db,
		config:         cfg,
		byNode:         make(map[string][]*StoreTx),
		byTxID:         make(map[int]*StoreTx),
		byObsID:        make(map[int]*StoreObs),
		byObserver:     make(map[string][]*StoreObs),
		byHash:         make(map[string]*StoreTx),
		byPayloadType:  make(map[int][]*StoreTx),
		nodeHashes:     make(map[string]map[string]bool),
		byPathHop:      make(map[string][]*StoreTx),
		advertPubkeys:  make(map[string]int),
		rfCache:        make(map[string]*cachedResult),
		topoCache:      make(map[string]*cachedResult),
		hashCache:      make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		regionObsCache: make(map[string]map[string]bool),
		areaNodeCache:  make(map[string]map[string]bool),
		rfCacheTTL:     15 * time.Second,
	}
}

func TestResolveAreaNodes_UnknownKey(t *testing.T) {
	db := setupTestDBv2(t)
	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)
	result := s.resolveAreaNodes("UNKNOWN")
	if result != nil {
		t.Errorf("want nil for unknown area, got %v", result)
	}
}

func TestResolveAreaNodes_CacheHit(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('pk1', 50.85, 4.35)`)

	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)

	r1 := s.resolveAreaNodes("BEL")
	if !r1["pk1"] {
		t.Fatal("pk1 should be in area BEL")
	}

	r2 := s.resolveAreaNodes("BEL")
	if !r2["pk1"] {
		t.Fatal("cache hit should still contain pk1")
	}
}

// ingestAdvert adds a synthetic ADVERT packet to the store's in-memory packet list.
func ingestAdvert(t *testing.T, s *PacketStore, hash, decodedJSON string) {
	t.Helper()
	pt := PayloadADVERT
	tx := &StoreTx{
		Hash:        hash,
		FirstSeen:   "2026-01-01T00:00:00Z",
		PayloadType: &pt,
		DecodedJSON: decodedJSON,
	}
	s.mu.Lock()
	s.packets = append(s.packets, tx)
	s.byHash[hash] = tx
	s.byPayloadType[PayloadADVERT] = append(s.byPayloadType[PayloadADVERT], tx)
	s.mu.Unlock()
}

func TestFilterPacketsByArea(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('inside-node', 50.85, 4.35)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('outside-node', 48.0, 4.35)`)

	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)

	ingestAdvert(t, s, "hash-in", `{"public_key":"inside-node","name":"Inside"}`)
	ingestAdvert(t, s, "hash-out", `{"public_key":"outside-node","name":"Outside"}`)

	result := s.QueryPackets(PacketQuery{Limit: 50, Area: "BEL"})
	if result.Total != 1 {
		t.Fatalf("want 1 packet in area BEL, got %d (packets: %v)", result.Total, result.Packets)
	}
}

func TestAnalyticsRFAreaFilter(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('inside-node', 50.85, 4.35)`)

	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)

	result := s.GetAnalyticsRF("", "BEL")
	if result == nil {
		t.Fatal("GetAnalyticsRF returned nil")
	}
}

func TestAnalyticsChannelsAreaFilter(t *testing.T) {
	db := setupTestDBv2(t)
	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
	}}
	s := newTestStoreWithDB(t, db, cfg)
	result := s.GetAnalyticsChannels("", "BEL")
	if result == nil {
		t.Fatal("GetAnalyticsChannels returned nil")
	}
}

func TestGetNodePubkeysInArea_BoundingBox(t *testing.T) {
	db := setupTestDBv2(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('in', 50.5, 5.0)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('out', 52.0, 5.0)`)

	minLat, maxLat, minLon, maxLon := 50.0, 51.5, 2.5, 6.4
	entry := AreaEntry{LatMin: &minLat, LatMax: &maxLat, LonMin: &minLon, LonMax: &maxLon}
	pks, err := db.GetNodePubkeysInArea(entry)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(pks) != 1 || pks[0] != "in" {
		t.Errorf("want [in], got %v", pks)
	}
}

func TestHandleConfigAreas(t *testing.T) {
	db := setupTestDBv2(t)
	cfg := &Config{Areas: map[string]AreaEntry{
		"BEL": {Label: "Belgium", Polygon: [][2]float64{{50.0, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50.0, 6.4}}},
		"MST": {Label: "Maastricht"},
	}}

	r := mux.NewRouter()
	srv := &Server{db: db, cfg: cfg}
	r.HandleFunc("/api/config/areas", srv.handleConfigAreas).Methods("GET")

	req := httptest.NewRequest(http.MethodGet, "/api/config/areas", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var result []map[string]string
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 areas, got %d", len(result))
	}
	keys := map[string]bool{}
	for _, entry := range result {
		keys[entry["key"]] = true
		if entry["label"] == "" {
			t.Errorf("missing label for key %q", entry["key"])
		}
	}
	if !keys["BEL"] || !keys["MST"] {
		t.Errorf("expected BEL and MST, got %v", keys)
	}
}

func TestHandleConfigAreasEmpty(t *testing.T) {
	db := setupTestDBv2(t)
	cfg := &Config{}

	r := mux.NewRouter()
	srv := &Server{db: db, cfg: cfg}
	r.HandleFunc("/api/config/areas", srv.handleConfigAreas).Methods("GET")

	req := httptest.NewRequest(http.MethodGet, "/api/config/areas", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var result []interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("want empty array, got %v", result)
	}
}
