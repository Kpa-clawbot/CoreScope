package main

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	rxSumPK    = "bb11220000000000000000000000000000000000000000000000000000000001"
	rxSumTwin  = "bb11ff0000000000000000000000000000000000000000000000000000000002"
	rxSumOther = "5a5a5a0000000000000000000000000000000000000000000000000000000003"
)

func rxSumPM() *prefixMap {
	return buildPrefixMap([]nodeInfo{
		{PublicKey: rxSumPK, Role: "repeater"},
		{PublicKey: rxSumTwin, Role: "repeater"},
		{PublicKey: rxSumOther, Role: "repeater"},
	})
}

func TestUniqueHeardKeyCandidates(t *testing.T) {
	got := uniqueHeardKeyCandidates(rxSumPK, rxSumPM())
	want := []string{rxSumPK, rxSumPK[:16], "bb1122"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v (the shared bb11 must be left out)", got, want)
	}

	pm := rxSumPM()
	pm.retired = map[string]struct{}{"5a5a": {}}
	got = uniqueHeardKeyCandidates(rxSumOther, pm)
	want = []string{rxSumOther, rxSumOther[:16], "5a5a5a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("retired prefix: got %v, want %v", got, want)
	}

	// A companion is no path candidate, so a prefix whose only candidate is
	// another node must not be taken as the companion's.
	comp := "5a5a5a1100000000000000000000000000000000000000000000000000000009"
	got = uniqueHeardKeyCandidates(comp, rxSumPM())
	want = []string{comp, comp[:16]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("companion: got %v, want %v", got, want)
	}
}

func TestMobileReceptionsSummary(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src) VALUES
		('rx1','bb1122',3,-3,51.00,4.00,'2026-11-10T10:00:00Z','x','rxlog'),
		('rx1','`+rxSumPK[:16]+`',8,1,51.10,4.00,'2026-11-10T11:00:00Z','x','discover'),
		('rx2','bb1122',3,4,51.00,4.00,'2026-11-11T10:00:00Z','x','rxlog'),
		('rx3','bb11',2,9,52.00,4.00,'2026-11-11T10:00:00Z','x','rxlog'),
		('rx4','bb1122',3,9,52.00,4.00,'2026-11-01T10:00:00Z','x','rxlog')`)
	srv := &Server{db: db, cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	lat, lon := 51.0, 4.0

	got := srv.mobileReceptionsSummary(rxSumPK, "2026-11-05T00:00:00Z", &lat, &lon, rxSumPM())
	if got == nil || got.Receptions != 3 || got.Receivers != 2 || got.MedianSNR == nil || *got.MedianSNR != 1 {
		t.Fatalf("summary = %+v, want 3 receptions, 2 receivers, median 1", got)
	}
	if got.MaxDistanceKm == nil || math.Abs(*got.MaxDistanceKm-11.12) > 0.05 {
		t.Fatalf("max distance = %v, want about 11.12 km", got.MaxDistanceKm)
	}

	got = srv.mobileReceptionsSummary(rxSumPK, "2026-11-05T00:00:00Z", nil, nil, rxSumPM())
	if got == nil || got.MaxDistanceKm != nil {
		t.Fatalf("no node position: summary = %+v, want no distance", got)
	}

	if got = srv.mobileReceptionsSummary(rxSumTwin, "2026-11-05T00:00:00Z", nil, nil, rxSumPM()); got != nil {
		t.Fatalf("node without receptions: summary = %+v, want nil", got)
	}
}

func TestNodeAnalytics_MobileOmittedWhenFeatureOff(t *testing.T) {
	_, router := setupTestServer(t)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=7", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "mobileReceptions") {
		t.Fatal("mobileReceptions present while clientRxCoverage is off")
	}
}

func TestNodeAnalytics_MobileReceptionsWhenFeatureOn(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	mustExecDB(t, srv.db, `CREATE TABLE client_receptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT, heard_key TEXT, heard_keylen INTEGER,
		rssi INTEGER, snr REAL, lat REAL, lon REAL, pos_acc_m REAL, rx_at TEXT, ingested_at TEXT, src TEXT)`)
	mustExecDB(t, srv.db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('rx1','aabbccdd11223344',32,-2,51.0,4.0,'2099-01-01T00:00:00Z','x','rxlog')`)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/analytics?days=7", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"mobileReceptions":{"receptions":1,"receivers":1,"medianSnr":-2`) {
		t.Fatalf("mobileReceptions missing or wrong: %s", w.Body.String())
	}
}
