package main

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/meshcore-analyzer/dbschema"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The packet store's SQL, and the other server queries outside db.go, proven
// to give the same answer on SQLite and Postgres: the same rows seeded into
// both (newDualTestDB), each function run on both (requireSame).

const (
	pgsNodeA   = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90" // repeater
	pgsNodeB   = "c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2" // companion, last_seen NULL
	pgsNodeC   = "e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" // repeater
	pgsNodeD   = "a1ff0718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" // room, same last_seen as A
	pgsInact   = "99990718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4"
	pgsRepX    = "7e7e0718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" // client-reported only
	pgsRepY    = "6d6d0718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4" // client-reported, observed_at tie
	pgsRx1     = "0101010101010101010101010101010101010101010101010101010101010101"
	pgsRx2     = "0202020202020202020202020202020202020202020202020202020202020202"
	pgsObsKey1 = "AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11AA11"
	pgsObsKey2 = "BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22BB22"
	pgsObsKey3 = "CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33CC33"
)

// pgsBase anchors every seeded time. Recent, so the advert-count window and
// the obs-count window see the rows; whole seconds, as the ingestor stores.
var pgsBase = time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

func pgsISO(d time.Duration) string  { return pgsBase.Add(d).Format("2006-01-02T15:04:05.000Z") }
func pgsEpoch(d time.Duration) int64 { return pgsBase.Add(d).Unix() }

// seedPgStore writes the shared dataset. Edge cases it carries on purpose:
// two transmissions first seen in the same second; a transmission with no
// observations sharing first_seen with one that has some (NULL o.timestamp
// ordering); two observations of one transmission in the same second; an
// observation with no observer; mixed-case hops; path_json that is invalid
// JSON, an object, a JSON null, an empty array, SQL NULL, and an array with a
// null and a number element; client names with several observed_at per
// target and an observed_at tie.
func seedPgStore(t *testing.T, d *dualDB) {
	t.Helper()
	for _, o := range []struct {
		idx      int
		id, name string
		iata     interface{}
	}{{1, pgsObsKey1, "Obs-One", "SJC"}, {2, pgsObsKey2, "Obs-Two", "sfo"}, {3, pgsObsKey3, "Obs-Three", nil}} {
		d.exec(t, `INSERT INTO observers (idx, id, name, iata, last_seen, first_seen) VALUES (?, ?, ?, ?, ?, ?)`,
			o.idx, o.id, o.name, o.iata, pgsISO(0), pgsISO(-time.Hour))
	}
	for _, n := range []struct {
		pk, name, role string
		lastSeen       interface{}
		lat, lon       interface{}
		adverts        int
	}{
		{pgsNodeA, "Alpha", "repeater", pgsISO(time.Hour), 37.1, -122.1, 5},
		{pgsNodeB, "Bravo", "companion", nil, nil, nil, 1},
		{pgsNodeC, "Charlie", "repeater", pgsISO(2 * time.Hour), 37.2, -122.2, 3},
		{pgsNodeD, "Delta", "room", pgsISO(time.Hour), 0.0, 0.0, 2},
	} {
		d.exec(t, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			n.pk, n.name, n.role, n.lat, n.lon, n.lastSeen, pgsISO(-48*time.Hour), n.adverts)
	}
	d.exec(t, `INSERT INTO inactive_nodes (public_key, name, role, last_seen) VALUES (?, 'Gone', 'repeater', ?)`, pgsInact, pgsISO(-72*time.Hour))

	nullable := func(s string) interface{} { // "-" seeds SQL NULL
		if s == "-" {
			return nil
		}
		return s
	}
	for _, tx := range []struct {
		id              int
		first           time.Duration
		route, ptype    int
		from, scopeName string
		decoded         string
	}{
		{1, 0, 1, 4, pgsNodeA, "-", `{"type":"ADVERT","name":"Alpha"}`},
		{2, 10 * time.Minute, 0, 5, "-", "west", `{"type":"GRP_TXT"}`},
		{3, 10 * time.Minute, 1, 5, "-", "", `{"type":"GRP_TXT","n":3}`}, // ties tx 2 on first_seen; unmatched scope
		{4, 20 * time.Minute, 1, 4, pgsNodeA, "east", `{"type":"ADVERT","name":"Alpha"}`},
		{5, 20 * time.Minute, 1, 2, "-", "-", `{"type":"TXT_MSG"}`}, // no observations, ties tx 4
		{6, 30 * time.Minute, 1, 5, "-", "badjson", `{"type":"GRP_TXT"}`},
		{7, 40 * time.Minute, 2, 5, "-", "-", `{"type":"GRP_TXT"}`}, // route type outside the audit
		{8, 50 * time.Minute, 0, 4, pgsNodeC, "west", `{"type":"ADVERT","name":"Charlie"}`},
	} {
		d.exec(t, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey, last_seen, scope_name)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
			tx.id, fmt.Sprintf("11%02dAABBCCDD", tx.id), fmt.Sprintf("hash%04d", tx.id), pgsISO(tx.first), tx.route, tx.ptype,
			tx.decoded, nullable(tx.from), pgsEpoch(tx.first+time.Minute), nullable(tx.scopeName))
	}
	for _, o := range []struct {
		id, tx   int
		observer interface{}
		path     interface{}
		at       time.Duration
		resolved interface{}
	}{
		{1, 1, 1, `["A1B2","C3"]`, 1 * time.Minute, `["` + pgsNodeA + `"]`},
		{2, 1, 2, `["a1b2"]`, 1 * time.Minute, nil}, // same second as obs 1
		{3, 1, 3, `["A1B2","E5F6"]`, 2 * time.Minute, nil},
		{4, 2, 3, `["A1","E5F6"]`, 11 * time.Minute, nil},
		{5, 2, 1, `["e5f6","a1b2"]`, 12 * time.Minute, nil},
		{6, 3, 1, `{"x":"A1B2"}`, 13 * time.Minute, nil},
		{7, 3, 2, `null`, 11 * time.Minute, nil},
		{8, 3, 3, `[]`, 14 * time.Minute, nil},
		{9, 3, nil, nil, 15 * time.Minute, nil}, // no observer, no path
		{10, 4, 1, `[null,"A1B2C3"]`, 21 * time.Minute, nil},
		{11, 4, 2, `[1234,"E5F6"]`, 22 * time.Minute, nil},
		{12, 6, 1, `["A1B2",`, 31 * time.Minute, nil}, // invalid JSON
		{13, 6, 2, `["A1B2"`, 32 * time.Minute, nil},  // invalid JSON
		{14, 7, 1, `["A1B2"]`, 41 * time.Minute, nil},
		{15, 8, 2, `["A1B2","E5F6"]`, 51 * time.Minute, nil},
		{16, 3, 2, `["E5F6"]`, 16 * time.Minute, nil},
	} {
		d.exec(t, `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, resolved_path)
			VALUES (?, ?, ?, 'rx', ?, ?, ?, ?, ?, ?)`,
			o.id, o.tx, o.observer, 4.25+float64(o.id), -100.0+float64(o.id), o.id%3, o.path, pgsEpoch(o.at), o.resolved)
	}
	for _, e := range [][2]int{{1, 1}, {4, 1}, {4, 2}, {8, 2}} {
		d.exec(t, `INSERT INTO advert_route_evidence (tx_id, bit) VALUES (?, ?)`, e[0], e[1])
	}
	d.exec(t, `INSERT INTO tx_rewrite_feed (tx_id, created_at) VALUES (2, ?), (3, ?)`, pgsEpoch(0), pgsEpoch(0))

	for i, c := range []struct {
		target, rx, at, name string
	}{
		{pgsRepX, pgsRx1, pgsISO(1 * time.Minute), "X-old"},
		{pgsRepX, pgsRx2, pgsISO(3 * time.Minute), "X-newest"},
		{pgsRepX, pgsRx1, pgsISO(2 * time.Minute), "X-mid"},
		{pgsRepY, pgsRx1, pgsISO(5 * time.Minute), "Y-first"},
		{pgsRepY, pgsRx2, pgsISO(5 * time.Minute), "Y-second"}, // observed_at tie: lowest id wins
		{pgsNodeA, pgsRx1, pgsISO(4 * time.Minute), "Alpha-reported"},
	} {
		d.exec(t, `INSERT INTO client_node_names (id, target, rx_pubkey, observed_at, ingested_at, name) VALUES (?, ?, ?, ?, ?, ?)`,
			i+1, c.target, c.rx, c.at, c.at, c.name)
	}
	for i, r := range []struct {
		rx, heard, src string
		lat, lon       float64
		at             time.Duration
	}{
		{pgsRx1, pgsRepX, "discover", 37.30, -122.30, 1 * time.Minute},
		{pgsRx2, pgsRepX, "discover", 37.31, -122.31, 2 * time.Minute},
		{pgsRx1, pgsRepY[:16], "advert", 37.40, -122.40, 3 * time.Minute},
		{pgsRx2, pgsRepX[:4], "rxlog", 37.32, -122.32, 4 * time.Minute},
		{pgsRx1, pgsNodeA, "discover", 37.10, -122.10, 5 * time.Minute},
	} {
		d.exec(t, `INSERT INTO client_receptions (id, rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, rx_at, ingested_at, src)
			VALUES (?, ?, ?, ?, -90, 6.5, ?, ?, ?, ?, ?)`,
			i+1, r.rx, r.heard, len(r.heard)/2, r.lat, r.lon, pgsISO(r.at), pgsISO(r.at), r.src)
	}
	d.exec(t, `INSERT INTO observer_neighbor_reports (observer_id, received_at, reported_at, neighbor_count, neighbors_json)
		VALUES (?, ?, ?, 2, '[]')`, pgsObsKey1, pgsISO(0), pgsISO(-time.Minute))
}

type pgsObs struct {
	ID, TxID                                               int
	ObserverID, ObserverName, IATA, Direction, Path, Stamp string
	SNR, RSSI                                              *float64
	Score                                                  *int
}

type pgsTx struct {
	ID                                                int
	Hash, FirstSeen, LatestSeen, Decoded, Path, ObsID string
	Scope                                             *string
	Route, PType                                      *int
	Count, Observers                                  int
	Evidence                                          uint8
	Obs                                               []pgsObs
}

type pgsStoreSummary struct {
	Packets            []pgsTx
	MaxTxID, MaxObsID  int
	TotalObs           int
	EvidenceCur, RWCur int64
}

// summarizeStore captures everything the load put in memory that a backend
// difference could show up in, in the store's own packet order.
func summarizeStore(s *PacketStore) pgsStoreSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := pgsStoreSummary{MaxTxID: s.maxTxID, MaxObsID: s.maxObsID, TotalObs: s.totalObs,
		EvidenceCur: int64(s.advertEvidenceCursor), RWCur: int64(s.txRewriteCursor)}
	for _, tx := range s.packets {
		st := pgsTx{ID: tx.ID, Hash: tx.Hash, FirstSeen: tx.FirstSeen, LatestSeen: tx.LatestSeen,
			Decoded: tx.DecodedJSON, Path: tx.PathJSON, ObsID: tx.ObserverID, Scope: tx.ScopeName,
			Route: tx.RouteType, PType: tx.PayloadType, Count: tx.ObservationCount,
			Observers: tx.UniqueObserverCount, Evidence: tx.AdvertRouteEvidence}
		for _, o := range tx.Observations {
			st.Obs = append(st.Obs, pgsObs{o.ID, o.TransmissionID, o.ObserverID, o.ObserverName, o.ObserverIATA,
				o.Direction, o.PathJSON, o.Timestamp, o.SNR, o.RSSI, o.Score})
		}
		out.Packets = append(out.Packets, st)
	}
	return out
}

func requirePgsLoaded(t *testing.T, name string, got pgsStoreSummary) {
	t.Helper()
	if len(got.Packets) != 8 || got.TotalObs != 16 {
		t.Fatalf("%s: loaded %d tx / %d obs, want 8 / 16", name, len(got.Packets), got.TotalObs)
	}
	for _, tx := range got.Packets {
		for _, o := range tx.Obs {
			if _, err := time.Parse("2006-01-02T15:04:05.000Z", o.Stamp); err != nil {
				t.Fatalf("%s: obs %d timestamp %q is not the store's ISO form", name, o.ID, o.Stamp)
			}
		}
	}
}

func TestPgStoreLoadPaths(t *testing.T) {
	d := newDualTestDB(t)
	seedPgStore(t, d)

	got := requireSame(t, d, "Load", func(db *DB) (pgsStoreSummary, error) {
		s := NewPacketStore(db, &PacketStoreConfig{})
		err := s.Load()
		return summarizeStore(s), err
	})
	requirePgsLoaded(t, "Load", got)
	// The tie-breaks: tx 2 and 3 share first_seen (tx 2's newest obs is newer,
	// so it comes first); tx 4 and 5 share it and tx 5 has no observations
	// (its NULL row sorts last); obs 1 and 2 share a second (newest id first).
	var order []int
	for _, tx := range got.Packets {
		order = append(order, tx.ID)
	}
	if want := []int{1, 3, 2, 4, 5, 6, 7, 8}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("Load packet order %v, want %v", order, want)
	}
	if got.EvidenceCur != 4 || got.RWCur != 2 {
		t.Errorf("watermarks: evidence %d, rewrite %d; want 4, 2", got.EvidenceCur, got.RWCur)
	}

	chunked := requireSame(t, d, "LoadChunked", func(db *DB) (pgsStoreSummary, error) {
		s := NewPacketStore(db, &PacketStoreConfig{})
		err := s.LoadChunked(3) // several chunks over 8 transmissions
		return summarizeStore(s), err
	})
	requirePgsLoaded(t, "LoadChunked", chunked)

	requireSame(t, d, "loadChunk", func(db *DB) (pgsStoreSummary, error) {
		s := NewPacketStore(db, &PacketStoreConfig{})
		err := s.loadChunk(pgsBase.Add(-time.Hour), pgsBase.Add(10*time.Hour))
		return summarizeStore(s), err
	})

	type ingested struct {
		Rows  []map[string]interface{}
		MaxID int
	}
	ing := requireSame(t, d, "IngestNewFromDB", func(db *DB) (ingested, error) {
		s := NewPacketStore(db, &PacketStoreConfig{})
		rows, max := s.IngestNewFromDB(0, 100)
		return ingested{rows, max}, nil
	})
	if ing.MaxID != 8 || len(ing.Rows) == 0 {
		t.Fatalf("IngestNewFromDB: max %d, %d rows", ing.MaxID, len(ing.Rows))
	}

	// New observations after the load, one tying an existing obs's second.
	stores := map[*DB]*PacketStore{}
	for _, db := range []*DB{d.sqlite, d.pg} {
		s := NewPacketStore(db, &PacketStoreConfig{})
		if err := s.Load(); err != nil {
			t.Fatal(err)
		}
		stores[db] = s
	}
	d.exec(t, `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, path_json, timestamp) VALUES (17, 2, 2, 'rx', 1.5, '["E5F6"]', ?), (18, 8, 1, 'rx', NULL, '["c3"]', ?)`,
		pgsEpoch(13*time.Minute), pgsEpoch(51*time.Minute))
	d.exec(t, `INSERT INTO tx_rewrite_feed (tx_id, created_at) VALUES (4, ?)`, pgsEpoch(0))
	d.exec(t, `UPDATE transmissions SET decoded_json = '{"rewritten":true}' WHERE id = 4`)
	obs := requireSame(t, d, "IngestNewObservations", func(db *DB) ([]map[string]interface{}, error) {
		return stores[db].IngestNewObservations(16, 500), nil
	})
	if len(obs) != 2 {
		t.Fatalf("IngestNewObservations: %d rows, want 2", len(obs))
	}
	after := requireSame(t, d, "store after ingest + rewrite poll", func(db *DB) (pgsStoreSummary, error) {
		err := stores[db].pollTxRewrites(500)
		return summarizeStore(stores[db]), err
	})
	if after.TotalObs != 18 || after.RWCur != 3 {
		t.Fatalf("after ingest: %d obs, rewrite cursor %d", after.TotalObs, after.RWCur)
	}
}

func TestPgStoreQueries(t *testing.T) {
	d := newDualTestDB(t)
	seedPgStore(t, d)
	store := func(db *DB) *PacketStore { return NewPacketStore(db, &PacketStoreConfig{}) }
	srv := func(db *DB) *Server { return &Server{db: db, cfg: &Config{}} }

	present := requireSame(t, d, "table probes", func(db *DB) ([]bool, error) {
		return []bool{db.advertEvidencePresent(), db.txRewriteFeedPresent(), db.hasNeighborReports(),
			db.clientNodeNamesPresent(), db.declaredRegionsTablePresent(), db.inactiveNodesPresent(),
			db.clientNamesUsable(), tableExists(db.conn, "observer_radios"), tableExists(db.conn, "no_such_table")}, nil
	})
	if fmt.Sprint(present) != "[true true true true true true true true false]" {
		t.Fatalf("table probes: %v", present)
	}

	nodes := requireSame(t, d, "getAllNodes", func(db *DB) ([]nodeInfo, error) {
		n := store(db).getAllNodes()
		// Unordered SQL on both backends; callers key it by pubkey.
		sort.Slice(n, func(i, j int) bool { return n[i].PublicKey < n[j].PublicKey })
		return n, nil
	})
	if len(nodes) != 4 || nodes[0].FirstSeen == "" || nodes[0].ObservationCount == 0 {
		t.Fatalf("getAllNodes: %+v", nodes)
	}

	health := requireSame(t, d, "bulkHealthNodes", func(db *DB) ([]bulkHealthNode, error) {
		return store(db).bulkHealthNodes(10, false, nil)
	})
	var hpk []string
	for _, n := range health {
		hpk = append(hpk, n.pk[:4])
	}
	// C newest; A and D tie (public_key order); B's NULL last_seen last.
	if want := "[e5f6 a1b2 a1ff c3d4]"; fmt.Sprint(hpk) != want {
		t.Fatalf("bulkHealthNodes order %v, want %s", hpk, want)
	}

	counts := requireSame(t, d, "refreshObsCounts", func(db *DB) ([2]int, error) {
		h, day, err := store(db).refreshObsCounts(pgsEpoch(30*time.Minute), pgsEpoch(-time.Hour))
		return [2]int{h, day}, err
	})
	if counts != [2]int{4, 16} {
		t.Fatalf("refreshObsCounts: %v", counts)
	}

	floods := requireSame(t, d, "CountFloodAdvertsForNode", func(db *DB) (int, error) {
		return db.CountFloodAdvertsForNode(pgsNodeA, 48, 1000)
	})
	if floods != 2 {
		t.Fatalf("CountFloodAdvertsForNode = %d, want 2", floods)
	}

	confirm := requireSame(t, d, "confirmResolvedPathContains", func(db *DB) ([2]bool, error) {
		s := store(db)
		return [2]bool{s.confirmResolvedPathContains(1, strings.ToUpper(pgsNodeA)), s.confirmResolvedPathContains(1, pgsNodeC)}, nil
	})
	if confirm != [2]bool{true, false} {
		t.Fatalf("confirmResolvedPathContains: %v", confirm)
	}

	hits := requireSame(t, d, "lookupPrefixes", func(db *DB) (map[string]prefixHit, error) {
		return srv(db).lookupPrefixes([]string{"a1b2", "A1B2", "a1", "e5f6", "7e7e", "6d6d", "ffff"}), nil
	})
	if hits["A1B2"].pubkey != pgsNodeA || hits["a1"].matches != 2 || hits["7e7e"].name != "X-newest" ||
		hits["6d6d"].name != "Y-first" || hits["ffff"].matches != 0 {
		t.Fatalf("lookupPrefixes: %+v", hits)
	}

	reported := requireSame(t, d, "computeReportedNodes", func(db *DB) (*ReportedNodesResponse, error) {
		return srv(db).computeReportedNodes()
	})
	if len(reported.Nodes) != 2 || reported.Nodes[0].Name != "Y-first" || reported.Nodes[1].Name != "X-newest" ||
		reported.Nodes[1].Reporters != 2 || reported.Nodes[1].Receptions == 0 {
		t.Fatalf("computeReportedNodes: %+v", reported.Nodes)
	}

	shared := requireSame(t, d, "sharedEightBytePrefixes", func(db *DB) (map[string]bool, error) {
		return srv(db).sharedEightBytePrefixes([]string{pgsRepX, pgsNodeA, "a1b2c3d4e5f60718ffffffff"}), nil
	})
	if !shared["a1b2c3d4e5f60718"] || shared[pgsRepX[:16]] {
		t.Fatalf("sharedEightBytePrefixes: %v", shared)
	}

	seen := requireSame(t, d, "companionLastSeen", func(db *DB) (map[string]string, error) {
		return srv(db).companionLastSeen([]string{pgsRx1, pgsRx2, "0303"}), nil
	})
	if seen[pgsRx1] != pgsISO(5*time.Minute) || len(seen) != 2 {
		t.Fatalf("companionLastSeen: %v", seen)
	}

	summaries := requireSame(t, d, "GetObserverNeighborSummaries", func(db *DB) (map[string]neighborSummary, error) {
		return db.GetObserverNeighborSummaries(), nil
	})
	if len(summaries) != 1 {
		t.Fatalf("GetObserverNeighborSummaries: %v", summaries)
	}

	reach := requireSame(t, d, "scanReachRows", func(db *DB) ([]pathRow, error) {
		s := srv(db)
		if s.reachHopIndexCovers(context.Background(), 0) {
			t.Errorf("hop index claimed on the end-state schema")
		}
		rows, err := s.scanReachRows(context.Background(), map[string]bool{"A1B2": true, "E5F6": true}, pgsEpoch(0))
		// No ORDER BY on either backend; the caller only aggregates.
		sort.Slice(rows, func(i, j int) bool {
			return fmt.Sprint(rows[i]) < fmt.Sprint(rows[j])
		})
		return rows, err
	})
	// Every observation whose path_json text holds "A1B2" or "E5F6" in either
	// case (LIKE is a substring match, so the object and the invalid JSON
	// count): 1-6, 11-16. Not 10, whose hop is A1B2C3.
	if len(reach) != 12 {
		t.Fatalf("scanReachRows: %d rows %+v", len(reach), reach)
	}

	type audit struct {
		All, InSJC, InNowhere map[string]*scopeAuditTargetAgg
	}
	au := requireSame(t, d, "ScopeAuditForwardingHeardIn", func(db *DB) (audit, error) {
		s := store(db)
		since := pgsISO(-time.Hour)
		targets := []string{pgsNodeA, pgsNodeC}
		var a audit
		var err error
		if a.All, err = s.ScopeAuditForwardingHeardIn(since, targets, nil); err != nil {
			return a, err
		}
		if a.InSJC, err = s.ScopeAuditForwardingHeardIn(since, targets, []string{"SJC"}); err != nil {
			return a, err
		}
		a.InNowhere, err = s.ScopeAuditForwardingHeardIn(since, targets, []string{"ZZZ"})
		return a, err
	})
	a := au.All[pgsNodeA]
	if a == nil || a.scopes["west"] == nil || a.scopes["east"] == nil || a.unscopedPackets == 0 {
		t.Fatalf("scope audit for A: %+v", a)
	}
	if c := au.All[pgsNodeC]; c == nil || c.unmatchedPackets != 1 {
		t.Fatalf("scope audit for C: %+v", c)
	}
	for target, agg := range au.All {
		// tx 6 carries only invalid path_json: skipped, never an error.
		if agg.scopes["badjson"] != nil {
			t.Fatalf("invalid path_json attributed to %s: %+v", target, agg.scopes["badjson"])
		}
	}
	if len(au.InSJC) == 0 || len(au.InNowhere) != 0 {
		t.Fatalf("region-filtered audit: SJC %d targets, ZZZ %d", len(au.InSJC), len(au.InNowhere))
	}

	// neighbor_edges last: with rows in it, a load needs the graph first.
	requireSame(t, d, "neighborEdgesTableExists empty", func(db *DB) (bool, error) {
		return neighborEdgesTableExists(db.conn), nil
	})
	d.exec(t, `INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 3, ?), (?, ?, 1, ?)`,
		pgsNodeA, pgsNodeC, pgsISO(0), pgsNodeA, strings.ToUpper(pgsNodeD), pgsISO(0))
	if !requireSame(t, d, "neighborEdgesTableExists", func(db *DB) (bool, error) {
		return neighborEdgesTableExists(db.conn), nil
	}) {
		t.Fatal("neighborEdgesTableExists = false with rows")
	}
	type degree struct {
		Total int
		Deg   map[string]int
		Desc  []int
	}
	deg := requireSame(t, d, "getDegreeSnapshot", func(db *DB) (degree, error) {
		snap := srv(db).getDegreeSnapshot(context.Background())
		return degree{snap.total, snap.deg, snap.sortedDesc}, nil
	})
	if deg.Total != 3 || deg.Deg[pgsNodeA] != 2 {
		t.Fatalf("degree snapshot: %+v", deg)
	}
}

// seedLoadBenchDB writes nTx transmissions with three observations each into a
// SQLite file built from the end-state schema (every production index), the
// shape LoadChunked reads on startup.
func seedLoadBenchDB(b *testing.B, nTx int) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "load-bench.db")
	rw, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		b.Fatal(err)
	}
	defer rw.Close()
	rw.SetMaxOpenConns(1)
	if _, err := rw.Exec(dbschema.SQLiteEndState); err != nil {
		b.Fatal(err)
	}
	tx, err := rw.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := tx.Exec(`INSERT INTO observers (idx, id, name, iata) VALUES (?, ?, ?, ?)`,
			i, fmt.Sprintf("%064X", i), fmt.Sprintf("obs%d", i), "SJC"); err != nil {
			b.Fatal(err)
		}
	}
	insTx, _ := tx.Prepare(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
		VALUES (?, ?, ?, ?, 1, 5, 1, ?, ?)`)
	insObs, _ := tx.Prepare(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, score, path_json, timestamp)
		VALUES (?, ?, ?, ?, 0, ?, ?)`)
	base := int64(1_790_000_000)
	for i := 1; i <= nTx; i++ {
		ts := base + int64(i)
		if _, err := insTx.Exec(i, "1505AABBCC", fmt.Sprintf("%016x", i), fmt.Sprintf("2026-09-%02dT%02d:%02d:%02d.000Z", 1+i%28, i%24, i%60, (i/60)%60),
			`{"type":"GRP_TXT","text":"hello"}`, ts); err != nil {
			b.Fatal(err)
		}
		for o := 1; o <= 3; o++ {
			if _, err := insObs.Exec(i, o, 5.5, -90.0, fmt.Sprintf(`["%02X","%02X%02X"]`, i%256, o, i%256), ts+int64(o)); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return path
}

// BenchmarkPgStoreLoadChunked20k times the startup load over 20k
// transmissions (60k observations) on SQLite: the hot path the shared
// observation-timestamp expression and the ORDER BY tie-breaks sit on.
func BenchmarkPgStoreLoadChunked20k(b *testing.B) {
	path := seedLoadBenchDB(b, 20000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, err := OpenDB(path)
		if err != nil {
			b.Fatal(err)
		}
		store := NewPacketStore(db, &PacketStoreConfig{})
		if err := store.LoadChunked(10000); err != nil {
			b.Fatal(err)
		}
		if len(store.packets) != 20000 || store.totalObs != 60000 {
			b.Fatalf("loaded %d tx / %d obs", len(store.packets), store.totalObs)
		}
		db.conn.Close()
	}
}
