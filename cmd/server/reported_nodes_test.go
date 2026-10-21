package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func snrp(v float64) *float64 { return &v }

// Offsets in metres around a reference point, small enough for a flat-earth
// conversion to be exact to well under a metre.
func offset(dNorthM, dEastM float64) (float64, float64) {
	const lat0, lon0 = 51.0, 5.38
	return lat0 + dNorthM/110540, lon0 + dEastM/(111320*math.Cos(lat0*math.Pi/180))
}

func pt(dN, dE float64, snr *float64) rxPoint {
	lat, lon := offset(dN, dE)
	return rxPoint{Lat: lat, Lon: lon, SNR: snr}
}

func TestEstimatePositionNeedsThreeSpreadSpots(t *testing.T) {
	// Fifteen receptions from one driveway are one spot: no estimate.
	var parked []rxPoint
	for i := 0; i < 15; i++ {
		parked = append(parked, pt(float64(i), 0, snrp(-5)))
	}
	if est, spots := estimatePosition(parked); est != nil || spots != 1 {
		t.Fatalf("parked: est=%v spots=%d, want nil and 1", est, spots)
	}
	// Two spots: still nothing.
	if est, spots := estimatePosition([]rxPoint{pt(0, 0, snrp(-5)), pt(0, 1000, snrp(-5))}); est != nil || spots != 2 {
		t.Fatalf("two spots: est=%v spots=%d", est, spots)
	}
	// Three spots, but all within 200 m of each other: nothing.
	close3 := []rxPoint{pt(0, 0, snrp(-5)), pt(0, 120, snrp(-5)), pt(120, 0, snrp(-5))}
	if est, spots := estimatePosition(close3); est != nil || spots != 3 {
		t.Fatalf("tight cluster: est=%v spots=%d", est, spots)
	}
}

func TestEstimatePositionWeightedCentroid(t *testing.T) {
	// Equal SNR: the plain centroid of the three spots.
	eq := []rxPoint{pt(0, 0, snrp(-10)), pt(0, 900, snrp(-10)), pt(900, 0, snrp(-10))}
	est, spots := estimatePosition(eq)
	if est == nil || spots != 3 {
		t.Fatalf("est=%v spots=%d", est, spots)
	}
	wantLat, wantLon := offset(300, 300)
	if d := distanceM(est.Lat, est.Lon, wantLat, wantLon); d > 2 {
		t.Errorf("centroid off by %.1f m", d)
	}
	// One spot 20 dB stronger pulls the estimate towards it.
	strong := []rxPoint{pt(0, 0, snrp(10)), pt(0, 900, snrp(-10)), pt(900, 0, snrp(-10))}
	est2, _ := estimatePosition(strong)
	lat0, lon0 := offset(0, 0)
	if distanceM(est2.Lat, est2.Lon, lat0, lon0) >= distanceM(est.Lat, est.Lon, lat0, lon0) {
		t.Errorf("the strong spot did not pull the estimate towards it: %+v vs %+v", est2, est)
	}
}

func TestEstimatePositionRadius(t *testing.T) {
	// Spots far apart: the radius reaches the farthest spot.
	wide := []rxPoint{pt(0, 0, snrp(-5)), pt(0, 3000, snrp(-5)), pt(3000, 0, snrp(-5))}
	est, _ := estimatePosition(wide)
	if est == nil {
		t.Fatal("no estimate")
	}
	far := 0.0
	for _, p := range wide {
		far = math.Max(far, distanceM(est.Lat, est.Lon, p.Lat, p.Lon))
	}
	if math.Abs(est.RadiusM-far) > 1 {
		t.Errorf("radius %.0f, want the farthest spot at %.0f", est.RadiusM, far)
	}
	// Spots close together: the radius never drops below the floor.
	near := []rxPoint{pt(0, 0, snrp(-5)), pt(0, 250, snrp(-5)), pt(250, 0, snrp(-5))}
	if est, _ := estimatePosition(near); est == nil || est.RadiusM != estimateMinRadiusM {
		t.Errorf("near: %+v, want radius %v", est, estimateMinRadiusM)
	}
}

// A spot keeps its best SNR, and a reception without SNR still counts as a spot.
func TestSpotsOfKeepsBestSNR(t *testing.T) {
	spots := spotsOf([]rxPoint{pt(0, 0, snrp(-12)), pt(30, 0, snrp(-3)), pt(60, 0, nil), pt(0, 500, nil)})
	if len(spots) != 2 {
		t.Fatalf("spots = %d, want 2", len(spots))
	}
	if spots[0].SNR == nil || *spots[0].SNR != -3 {
		t.Errorf("first spot SNR = %v, want -3", spots[0].SNR)
	}
	if spots[1].SNR != nil {
		t.Errorf("second spot SNR = %v, want nil", spots[1].SNR)
	}
}

// --- endpoint ---

const (
	repA = "c0cdee59e0db981b00000000000000000000000000000000000000000000000a" // reported, spread receptions
	repB = "b1b1220000000000000000000000000000000000000000000000000000000000" // reported, one spot
	repC = "c2c2330000000000000000000000000000000000000000000000000000000000" // reported, but has an advert position
	repH = "c3c3440000000000000000000000000000000000000000000000000000000000" // hidden name
	repD = "d4d4550000000000000000000000000000000000000000000000000000000000" // reported, has an advert without a position
	repF = "f6f6770000000000000000000000000000000000000000000000000000000000" // reported, never heard directly
	repR = "a7a7880000000000000000000000000000000000000000000000000000000000" // reported, heard only in rxlog paths
)

func insRec(t *testing.T, db *DB, hk, src string, dN, dE, snr float64, at string) {
	t.Helper()
	lat, lon := offset(dN, dE)
	mustExecDB(t, db, fmt.Sprintf(`INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('comp','%s',%d,%f,%f,%f,'%s','x','%s')`, hk, len(hk)/2, snr, lat, lon, at, src))
}

func seedReported(t *testing.T) *Server {
	t.Helper()
	db := seedCoverageDB(t)
	createClientNodeNames(t, db)
	insClientName(t, db, repA, "Self Named", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, repB, "Lonely", "2026-10-16T09:00:00.000Z")
	insClientName(t, db, repC, "Has Advert", "2026-10-16T08:00:00.000Z")
	insClientName(t, db, repH, "🚫Secret", "2026-10-16T07:00:00.000Z")
	insClientName(t, db, repD, "No GPS", "2026-10-16T06:00:00.000Z")
	insClientName(t, db, repF, "Invented", "2026-10-16T05:00:00.000Z")
	insClientName(t, db, repR, "Path Only", "2026-10-16T04:00:00.000Z")
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES ('`+repC+`','Has Advert','repeater',51.0,5.38)`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+repD+`','Advert Name','repeater')`)
	// Heard directly, so only the advert (repC, repD) or the hidden name (repH)
	// keeps them out.
	for _, pk := range []string{repC, repD, repH} {
		insRec(t, db, pk[:16], "discover", 0, 0, -5, "2026-10-07T10:00:00Z")
	}
	insRec(t, db, repR[:4], "rxlog", 0, 0, -5, "2026-10-07T10:00:00Z")

	// repA: an 8-byte discover key at three spread spots, plus a 2-byte rxlog
	// hash that resolves uniquely to it.
	insRec(t, db, repA[:16], "discover", 0, 0, -5, "2026-10-01T10:00:00Z")
	insRec(t, db, repA[:16], "discover", 0, 900, -10, "2026-10-02T10:00:00Z")
	insRec(t, db, repA, "discover", 900, 0, -10, "2026-10-03T10:00:00Z")
	insRec(t, db, repA[:4], "rxlog", 10, 10, -6, "2026-10-04T10:00:00Z")
	// A geo row is not a direct reception of this node and must be ignored.
	insRec(t, db, repA[:4], "geo", 5000, 5000, -1, "2026-10-05T10:00:00Z")
	// repB: heard from one place only.
	insRec(t, db, repB[:16], "discover", 0, 0, -4, "2026-10-06T10:00:00Z")
	insRec(t, db, repB[:16], "discover", 20, 0, -6, "2026-10-06T10:01:00Z")
	return &Server{db: db, cfg: &Config{HiddenNamePrefixes: []string{"🚫"}, ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
}

func getReported(t *testing.T, srv *Server) ReportedNodesResponse {
	t.Helper()
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/reported", srv.handleReportedNodes).Methods("GET")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/nodes/reported", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp ReportedNodesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestReportedNodesEndpoint(t *testing.T) {
	resp := getReported(t, seedReported(t))
	if resp.Method.MinSpots != estimateMinSpots || resp.Method.MinRadiusM != estimateMinRadiusM {
		t.Errorf("method = %+v", resp.Method)
	}
	got := map[string]ReportedNode{}
	for _, n := range resp.Nodes {
		got[n.Pubkey] = n
	}
	if len(got) != 2 {
		t.Fatalf("nodes = %v, want repA and repB only (repC and repD sent an advert, repH is hidden, repF was never heard, repR only in rxlog paths)", resp.Nodes)
	}
	a := got[repA]
	if a.Name != "Self Named" || a.Receptions != 4 || a.Spots != 3 || a.Estimate == nil {
		t.Fatalf("repA = %+v", a)
	}
	if a.FirstHeard != "2026-10-01T10:00:00Z" || a.LastHeard != "2026-10-04T10:00:00Z" {
		t.Errorf("repA heard %s..%s", a.FirstHeard, a.LastHeard)
	}
	far, _ := offset(5000, 5000)
	if a.Estimate.Lat > far-0.01 {
		t.Errorf("the geo row moved the estimate: %+v", a.Estimate)
	}
	b := got[repB]
	if b.Receptions != 2 || b.Spots != 1 || b.Estimate != nil {
		t.Fatalf("repB = %+v, want 2 receptions in 1 spot and no estimate", b)
	}
	// Newest name report first.
	if resp.Nodes[0].Pubkey != repA {
		t.Errorf("order: first = %s, want repA", resp.Nodes[0].Pubkey)
	}
}

// A short rxlog hash shared with a node in nodes is ambiguous and must not be
// attributed to the reported repeater.
func TestReportedNodesSkipsAmbiguousPathHash(t *testing.T) {
	srv := seedReported(t)
	mustExecDB(t, srv.db, `INSERT INTO nodes (public_key, name, role) VALUES ('c0cd990000000000000000000000000000000000000000000000000000000000','Other','repeater')`)
	resp := getReported(t, srv)
	for _, n := range resp.Nodes {
		if n.Pubkey == repA && n.Receptions != 3 {
			t.Fatalf("repA receptions = %d, want 3 (the ambiguous c0cd rxlog row dropped)", n.Receptions)
		}
	}
}

func TestReportedNodesBlacklisted(t *testing.T) {
	srv := seedReported(t)
	srv.cfg.NodeBlacklist = []string{repA}
	for _, n := range getReported(t, srv).Nodes {
		if n.Pubkey == repA {
			t.Fatal("blacklisted repeater listed")
		}
	}
}

// Without client_node_names (an older ingestor) the endpoint answers an empty
// list, not an error.
func TestReportedNodesWithoutTable(t *testing.T) {
	srv := &Server{db: setupTestDBv2(t), cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	if resp := getReported(t, srv); len(resp.Nodes) != 0 {
		t.Fatalf("nodes = %v, want none", resp.Nodes)
	}
}

// The list and its estimate come from companion GPS fixes, so the endpoint is
// gated like the other client_receptions endpoints.
func TestReportedNodesGatedByClientRxCoverage(t *testing.T) {
	srv := seedReported(t)
	srv.cfg.ClientRxCoverage = nil
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/reported", srv.handleReportedNodes).Methods("GET")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/nodes/reported", nil))
	if rr.Code != 404 {
		t.Fatalf("status %d with clientRxCoverage off, want 404", rr.Code)
	}
}

// A /names report for a made-up pubkey that shares the first 8 bytes of a
// heard repeater must not count as heard: the 8-byte discover rows belong to
// the repeater in nodes. Before the uniqueness check in clientNameHeard, one
// such message made the repeater's short prefix ambiguous and listed the
// made-up target with the repeater's receptions.
func TestReportedNameSharingEightBytePrefixIsNotHeard(t *testing.T) {
	const real = "c5c5660011223344000000000000000000000000000000000000000000000001"
	const fake = "c5c5660011223344ffffffffffffffffffffffffffffffffffffffffffffffff"
	srv := seedReported(t)
	mustExecDB(t, srv.db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+real+`','Real','repeater')`)
	insRec(t, srv.db, real[:16], "discover", 0, 0, -5, "2026-10-08T10:00:00Z")
	insRec(t, srv.db, real[:16], "discover", 0, 900, -5, "2026-10-08T11:00:00Z")
	insRec(t, srv.db, real[:16], "discover", 900, 0, -5, "2026-10-08T12:00:00Z")
	insClientName(t, srv.db, fake, "Spoof", "2026-10-17T10:00:00.000Z")

	var r ResolvePrefixResp
	json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix=c5c5").Body.Bytes(), &r)
	if r.Ambiguous || r.Pubkey != real || r.Name != "Real" {
		t.Errorf("resolve c5c5 = %+v, want the real repeater, not ambiguous", r)
	}
	for _, n := range getReported(t, srv).Nodes {
		if n.Pubkey == fake {
			t.Fatalf("made-up target listed with borrowed receptions: %+v", n)
		}
	}
}

// Two reported targets that share an 8-byte prefix: an 8-byte reception could
// be either, so it counts for neither. A full-key reception still counts.
func TestReportedTargetsSharingEightBytePrefix(t *testing.T) {
	const t1 = "e5e5770011223344000000000000000000000000000000000000000000000001"
	const t2 = "e5e5770011223344000000000000000000000000000000000000000000000002"
	srv := seedReported(t)
	insClientName(t, srv.db, t1, "First", "2026-10-17T10:00:00.000Z")
	insClientName(t, srv.db, t2, "Second", "2026-10-17T10:00:00.000Z")
	insRec(t, srv.db, t1, "discover", 0, 0, -5, "2026-10-08T10:00:00Z")
	insRec(t, srv.db, t1[:16], "discover", 0, 900, -5, "2026-10-08T11:00:00Z")
	insRec(t, srv.db, t1[:16], "discover", 900, 0, -5, "2026-10-08T12:00:00Z")

	got := map[string]ReportedNode{}
	for _, n := range getReported(t, srv).Nodes {
		got[n.Pubkey] = n
	}
	if n, ok := got[t1]; !ok || n.Receptions != 1 {
		t.Errorf("t1 = %+v (listed %v), want listed with only its full-key reception", n, ok)
	}
	if _, ok := got[t2]; ok {
		t.Errorf("t2 listed, but it was only heard by a shared 8-byte prefix")
	}
}
