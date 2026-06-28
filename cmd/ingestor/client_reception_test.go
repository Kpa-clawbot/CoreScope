package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// TestPruneOldClientReceptions verifies the retention reaper bounds the coverage
// tables: rows older than the window (and stale companion names) are deleted,
// recent ones kept, and days=0 disables it.
func TestPruneOldClientReceptions(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	recent := now.AddDate(0, 0, -1).Format(time.RFC3339)
	old := now.AddDate(0, 0, -40).Format(time.RFC3339)
	const companion2 = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"

	s.InsertClientReception(&ClientReception{RxPubkey: testCompanionPK, HeardKey: "aabbcc", HeardKeyLen: 3, Lat: 51, Lon: 3.7, RxAt: recent, IngestedAt: "x", Src: "rxlog"})
	s.InsertClientReception(&ClientReception{RxPubkey: testCompanionPK, HeardKey: "aabbcc", HeardKeyLen: 3, Lat: 51, Lon: 3.7, RxAt: old, IngestedAt: "x", Src: "rxlog"})
	s.UpsertClientObserver(testCompanionPK, "Fresh", recent)
	s.UpsertClientObserver(companion2, "Stale", old)

	if n, _ := s.PruneOldClientReceptions(0); n != 0 {
		t.Fatalf("days=0 must be a no-op, got %d", n)
	}
	n, err := s.PruneOldClientReceptions(7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 old reception pruned, got %d", n)
	}
	var recN, obsN int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&recN)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_observers`).Scan(&obsN)
	if recN != 1 {
		t.Fatalf("expected 1 reception remaining (recent), got %d", recN)
	}
	if obsN != 1 {
		t.Fatalf("expected 1 observer remaining (fresh), got %d", obsN)
	}
}

func TestClientReceptionsTableExists(t *testing.T) {
	s := newTestStore(t)
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(client_receptions)`)
	if err != nil {
		t.Fatalf("PRAGMA failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"id", "rx_pubkey", "heard_key", "heard_keylen", "rssi", "snr", "lat", "lon", "pos_acc_m", "rx_at", "ingested_at", "src"} {
		if !cols[want] {
			t.Errorf("missing column %q in client_receptions", want)
		}
	}
}

func crF(f float64) *float64 { return &f }
func crI(i int) *int         { return &i }

// TestClientReceptionsCoverageQueryUsesIndex verifies #5/#18: the dominant
// per-node coverage query (sargable heard_key IN-list + bbox, mirroring
// cmd/server coverageHeardKeyCandidates) seeks the heard_key composite index
// rather than scanning the table. Without idx_client_recept_heard_geo the plan
// is "SCAN client_receptions".
func TestClientReceptionsCoverageQueryUsesIndex(t *testing.T) {
	s := newTestStore(t)
	q := `EXPLAIN QUERY PLAN SELECT lat, lon, snr, rssi, heard_key, rx_at
		FROM client_receptions
		WHERE heard_key IN (?,?,?) AND lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?`
	rows, err := s.db.Query(q, "aabbccddeeff00112233", "aabbcc", "aabb", 50.0, 52.0, 3.0, 4.0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "USING INDEX idx_client_recept") {
		t.Fatalf("coverage query should use a client_recept index, plan was:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN client_receptions") {
		t.Fatalf("coverage query should not full-scan, plan was:\n%s", plan)
	}
}

// TestClientReceptionsRetentionUsesRxAtIndex verifies the retention reaper's
// DELETE ... WHERE rx_at < ? (and the leaderboard's rx_at window) seek the rx_at
// index rather than full-scanning under the writer lock (polish review).
func TestClientReceptionsRetentionUsesRxAtIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN DELETE FROM client_receptions WHERE rx_at < ?`, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_client_recept_rxat") {
		t.Fatalf("retention DELETE should use idx_client_recept_rxat, plan was:\n%s", plan)
	}
}

// TestRxLeaderboardQueryIsIndexBacked pins the planner choice for the leaderboard
// SELECT (the rx_at-windowed, rx_pubkey-grouped query in cmd/server/rx_dashboard.go).
// SQLite serves it from the UNIQUE(rx_pubkey,heard_key,rx_at) constraint index as a
// COVERING scan (not idx_client_recept_rxat, and not a table-heap scan). The table
// is retention-bounded, so a covering scan is acceptable; this test guards against a
// silent regression to a bare table scan under the writer lock when the schema is
// next tweaked. Representative form (no JOINs — they don't change whether `cr` is
// index-backed).
func TestRxLeaderboardQueryIsIndexBacked(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN
		SELECT cr.rx_pubkey, COUNT(*), COUNT(DISTINCT cr.heard_key)
		FROM client_receptions cr
		WHERE cr.rx_at >= ?
		GROUP BY cr.rx_pubkey
		ORDER BY COUNT(*) DESC
		LIMIT ?`, "2026-01-01T00:00:00Z", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	t.Logf("leaderboard plan:\n%s", plan)
	// The concern is a bare table-heap scan, not which specific index wins. The
	// plan must stay index-backed (covering or search) — a regression to a bare
	// "SCAN cr" without an index fails here.
	if !strings.Contains(plan, "INDEX") {
		t.Fatalf("leaderboard SELECT must stay index-backed (no full table-heap scan), plan was:\n%s", plan)
	}
}

func TestDeriveHeardKey(t *testing.T) {
	full := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	k, l, src, ok := deriveHeardKey("rx", packetpath.RouteFlood, nil, strings.ToUpper(full), true)
	if !ok || l != 32 || src != "advert" || k != full {
		t.Fatalf("0-hop advert: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	k, l, src, ok = deriveHeardKey("rx", packetpath.RouteFlood, []string{"aa", "bbccdd"}, "", false)
	if !ok || k != "bbccdd" || l != 3 || src != "rxlog" {
		t.Fatalf("flood path: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	// DIRECT route: path[last] is the route's far end, not the transmitter — must be rejected.
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteDirect, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteTransportDirect, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("transport-direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, []string{"aa", "bb"}, "", false); ok {
		t.Fatalf("1-byte last hop should be rejected")
	}
	if _, _, _, ok = deriveHeardKey("tx", packetpath.RouteFlood, []string{"aabbcc"}, "", false); ok {
		t.Fatalf("tx must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, nil, "", false); ok {
		t.Fatalf("no hops + non-advert must be rejected")
	}
}

func TestBuildClientReception(t *testing.T) {
	acc := 8.0
	rec, ok := buildClientReception("companionpk", "rx", packetpath.RouteFlood, []string{"aa", "bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, &acc, "2026-06-09T12:00:00Z", "2026-06-09T12:00:01Z")
	if !ok || rec.HeardKey != "bbccdd" || rec.HeardKeyLen != 3 || rec.Src != "rxlog" {
		t.Fatalf("bad reception: %+v ok=%v", rec, ok)
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteDirect, []string{"bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, nil, "t", "t"); ok {
		t.Fatal("direct-route path must be rejected (not the transmitter)")
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteFlood, []string{"bbccdd"}, "", false, nil, nil, 99.0, 3.72, nil, "t", "t"); ok {
		t.Fatal("out-of-range lat must be rejected")
	}
}

func TestInsertClientReceptionRoundTripAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	rec := &ClientReception{
		RxPubkey: "companionpk", HeardKey: "bbccdd", HeardKeyLen: 3, RSSI: crI(-92),
		Lat: 51.05, Lon: 3.72, RxAt: "2026-06-09T12:00:00Z", IngestedAt: "2026-06-09T12:00:01Z", Src: "rxlog",
	}
	if ins, err := s.InsertClientReception(rec); err != nil || !ins {
		t.Fatalf("first insert: ins=%v err=%v", ins, err)
	}
	if ins, err := s.InsertClientReception(rec); err != nil || ins {
		t.Fatalf("second insert should be a no-op: ins=%v err=%v", ins, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestHandleClientPacketRelayedAdvertWritesReception(t *testing.T) {
	s := newTestStore(t)
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	msg := map[string]interface{}{
		"raw":       advertHex,
		"direction": "rx",
		"timestamp": "2026-06-09T12:00:00Z",
		"origin":    "MyMob",
		"SNR":       -7.0,
		"RSSI":      -92.0,
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientPacket(s, "test", testCompanionPK, msg, nil)

	var obsName string
	s.db.QueryRow(`SELECT name FROM client_observers WHERE pubkey=?`, testCompanionPK).Scan(&obsName)
	if obsName != "MyMob" {
		t.Fatalf("expected client_observers name 'MyMob', got %q", obsName)
	}

	// This fixture is a relayed advert (non-empty path), so by the capture HARD
	// RULE we record the directly-heard LAST hop (multibyte), not the originator.
	// The 0-hop advert→full-pubkey branch is covered by TestDeriveHeardKey.
	var n, keylen int
	var src string
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(heard_keylen),0), COALESCE(MAX(src),'') FROM client_receptions WHERE rx_pubkey=?`, testCompanionPK).Scan(&n, &keylen, &src); err != nil {
		t.Fatal(err)
	}
	if n != 1 || keylen < 2 || src != "rxlog" {
		t.Fatalf("expected 1 rxlog reception (multibyte last hop), got n=%d keylen=%d src=%q", n, keylen, src)
	}

	// No GPS → no row.
	const companion2 = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"
	handleClientPacket(s, "test", companion2, map[string]interface{}{"raw": advertHex, "direction": "rx"}, nil)
	var n2 int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions WHERE rx_pubkey=?`, companion2).Scan(&n2)
	if n2 != 0 {
		t.Fatalf("packet without gps must be dropped, got %d rows", n2)
	}
}

// TestHandleClientPacketZeroHopAdvertWritesReception covers the #9 gap: the
// advert fixture used above is a RELAYED advert (non-empty path), so it exercises
// the rxlog last-hop branch, not the 0-hop src='advert' branch. Here we rebuild
// the same advert with zero hops — header (FLOOD ADVERT) + "00" (0 hops) + the
// same advert payload — so handleClientPacket stores the advertiser by its full
// pubkey with src='advert', and we assert gps/snr were captured too.
func TestHandleClientPacketZeroHopAdvertWritesReception(t *testing.T) {
	s := newTestStore(t)
	relayed := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	// relayed = header(2) + path-descriptor(2) + 5*2-byte hops(20) + payload.
	payload := relayed[24:]
	zeroHop := "1100" + payload
	advertPubkey := strings.ToLower(payload[:64]) // advert payload starts with the 32-byte pubkey

	msg := map[string]interface{}{
		"raw": zeroHop, "direction": "rx", "timestamp": "2026-06-09T12:00:00Z",
		"origin": "MyMob", "SNR": -7.0, "RSSI": -92.0,
		"gps": map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientPacket(s, "test", testCompanionPK, msg, nil)

	var heardKey, src string
	var keylen int
	var snr sql.NullFloat64
	var lat, lon float64
	if err := s.db.QueryRow(`SELECT heard_key, heard_keylen, src, snr, lat, lon FROM client_receptions WHERE rx_pubkey=?`, testCompanionPK).
		Scan(&heardKey, &keylen, &src, &snr, &lat, &lon); err != nil {
		t.Fatalf("expected a 0-hop advert reception: %v", err)
	}
	if src != "advert" || keylen != 32 || heardKey != advertPubkey {
		t.Fatalf("0-hop advert: want advert/32/%s, got %s/%d/%s", advertPubkey, src, keylen, heardKey)
	}
	if !snr.Valid || snr.Float64 != -7 || lat != 51.05 || lon != 3.72 {
		t.Fatalf("gps/snr not captured: snr=%v lat=%f lon=%f", snr, lat, lon)
	}
}

// TestHandleClientPacketRejectsNonHexPubkey verifies the #2 fix: a companion
// pubkey from the topic that isn't lowercase hex (a no-ACL broker could publish
// meshcore/client/!@#$/packets) writes nothing to either coverage table. Without
// the clientPubkeyRe guard this fixture would insert a polluting row.
func TestHandleClientPacketRejectsNonHexPubkey(t *testing.T) {
	s := newTestStore(t)
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	for _, bad := range []string{"!@#$", "companionpk", "", "g0g0", "xyz"} {
		msg := map[string]interface{}{
			"raw": advertHex, "direction": "rx", "timestamp": "2026-06-09T12:00:00Z",
			"origin": "Spoof", "SNR": -7.0, "RSSI": -92.0,
			"gps": map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
		}
		handleClientPacket(s, "test", bad, msg, nil)
	}
	var nRecept, nObs int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&nRecept)
	s.db.QueryRow(`SELECT COUNT(*) FROM client_observers`).Scan(&nObs)
	if nRecept != 0 || nObs != 0 {
		t.Fatalf("non-hex pubkey must write nothing, got %d receptions, %d observers", nRecept, nObs)
	}
}
