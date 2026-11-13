package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx/dbxtest"
	"github.com/meshcore-analyzer/mbcapqueue"
)

// openPostgresTestStore opens a Store through OpenPostgresStore itself (DSN,
// pool settings, pgschema.Apply, guard) inside a throwaway schema created by
// dbxtest. queuePath anchors the store's file queues. Skips without
// $CORESCOPE_TEST_PG_DSN.
func openPostgresTestStore(t testing.TB, queuePath string) *Store {
	t.Helper()
	scoped := dbxtest.OpenPostgres(t) // creates the schema, drops it in Cleanup
	var schema string
	if err := scoped.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("CORESCOPE_TEST_PG_DSN"))
	if err != nil || !isPostgresURL(u.String()) {
		t.Fatal("CORESCOPE_TEST_PG_DSN must be a postgres:// URL for this test")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := OpenPostgresStore(u.String(), queuePath, 300)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	t.Cleanup(func() { s.Close() }) // runs before dbxtest drops the schema
	return s
}

// wallClockColumns are the only columns TestIngestSameOnBothBackends does not
// require to be equal: each is written from the ingestor's (or the
// database's) clock at the moment of the write, so the two backends, written
// microseconds apart, can legitimately differ across a second boundary. A
// pair that differs must still be NULL on both sides or, on both sides, a
// time inside the test's run; a clamp gone wrong (a stored 2099) or a packet
// time where the clock belongs fails that check. Some of them hold a caller's
// time when one is given (UpsertObserverAt's lastSeen), which then compares
// equal.
var wallClockColumns = map[string][]string{
	// Bookkeeping stamped by datetime('now') / to_char(now()).
	"_async_migrations": {"started_at", "ended_at"},
	"dropped_packets":   {"dropped_at"},
	"tx_rewrite_feed":   {"created_at"},
	"key_backfill_done": {"done_at"},
	// Packet times are ingest time, not the envelope's (#1370), and so is
	// what is derived from them.
	"transmissions":  {"first_seen", "last_seen", "created_at"},
	"observations":   {"timestamp"},
	"neighbor_edges": {"last_seen"},
	// Liveness: "when did the analyzer last hear from it" is ingest time
	// (#1465); nodes take ingest time for an advert's first/last seen.
	"nodes":     {"first_seen", "last_seen"},
	"observers": {"first_seen", "last_seen", "last_packet_at"},
	// Observer status history and samples, stamped at receipt.
	"observer_radios":           {"first_seen", "last_seen"},
	"observer_neighbor_reports": {"received_at"},
	// RoundToInterval(now, sampleInterval): see wallClockSlack.
	"observer_metrics": {"timestamp"},
	// Mobile-client rows carry the receive time (compared) and the ingest
	// time (here).
	"client_observers":       {"last_seen"},
	"client_receptions":      {"ingested_at"},
	"client_rx_observations": {"ingested_at"},
	"client_rf_samples":      {"ingested_at"},
	"node_declared_regions":  {"ingested_at"},
	"client_node_names":      {"ingested_at"},
}

// backendOnlyTables are not compared, with the reason.
var backendOnlyTables = map[string]string{
	"advert_evidence_backfill": "progress cursor of advert_route_evidence_v1, a backfill of rows written before " +
		"the evidence table existed; it runs on a SQLite database and leaves its cursor row, while a Postgres " +
		"database starts at the end state with the migration seeded as done and never runs it",
}

// wallClockSlack widens the accepted window below the test's start for
// columns rounded down to an interval (observer_metrics.timestamp is
// RoundToInterval(now, 300s)).
var wallClockSlack = map[string]time.Duration{"observer_metrics.timestamp": 300 * time.Second}

// TestIngestSameOnBothBackends drives a SQLite store and a Postgres store
// through one realistic sequence and requires every table to end up the same
// on both. The sequence goes through handleMessage, the code path MQTT
// messages take, with real packets: adverts from several nodes (direct, flood
// with a path, transport-coded), group text decrypted at ingest and later by
// the key backfill, one packet heard by three observers plus exact and
// path-differing duplicates, observer status (live and retained, with radios
// and metrics), a clock-skewed observer, an invalid signature (dropped), the
// four mobile-client topics, a neighbors report, the multibyte snapshot, the
// neighbor-edge build, stale-node and stale-observer retention, and every
// retention prune with data on both sides of its cutoff.
func TestIngestSameOnBothBackends(t *testing.T) {
	testStart := time.Now().UTC()

	liteDir := t.TempDir()
	lite, err := OpenStore(filepath.Join(liteDir, "meshcore.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer lite.Close()
	lite.WaitForAsyncMigrations()
	pgDir := t.TempDir()
	pg := openPostgresTestStore(t, filepath.Join(pgDir, "meshcore.db"))
	isolateControlTables(t, pg)
	if !pg.pg || lite.pg {
		t.Fatalf("backend detection: lite.pg=%v pg.pg=%v", lite.pg, pg.pg)
	}
	stores := []*Store{lite, pg}
	each := func(what string, fn func(*Store) error) {
		t.Helper()
		for _, s := range stores {
			if err := fn(s); err != nil {
				t.Fatalf("%s (pg=%v): %v", what, s.pg, err)
			}
		}
	}

	// Timestamps: "recent" is inside every retention window used below,
	// "old" outside them (but inside resolveRxTime's 30-day acceptance).
	recent := testStart.Add(-2 * time.Hour).Truncate(time.Second)
	old := testStart.Add(-20 * 24 * time.Hour).Truncate(time.Second)
	at := func(base time.Time, sec int) string {
		return base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
	}

	keyNow := deriveHashtagChannelKey("#diffnow")
	keyLater := deriveHashtagChannelKey("#difflater")
	ingestKeys := map[string]string{"#diffnow": keyNow}
	regions := regionSetOf("#be")
	newCfg := func(validateSigs bool) *Config {
		return &Config{
			ValidateSignatures:   &validateSigs,
			ClientRxCoverage:     &ClientRxCoverageConfig{Enabled: true},
			ClientRxObservations: &ClientRxObservationsConfig{Enabled: true},
			ClientRfSamples:      &ClientRfSamplesConfig{Enabled: true},
			ClientRegions:        &ClientRegionsConfig{Enabled: true},
			ClientNodeNames:      &ClientNodeNamesConfig{Enabled: true},
		}
	}
	cfg, cfgNoSig := newCfg(true), newCfg(false)

	send := func(c *Config, topic string, retained bool, payload string) {
		t.Helper()
		for _, s := range stores {
			handleMessage(s, "diff", MQTTSource{Name: "diff"},
				&mockMessage{topic: topic, payload: []byte(payload), retained: retained}, ingestKeys, regions, c)
		}
	}
	pkt := func(c *Config, observer, raw, ts string, extra string) {
		t.Helper()
		send(c, "meshcore/SJC/"+observer+"/packets", false,
			`{"raw":"`+raw+`","timestamp":"`+ts+`","origin":"`+strings.ToUpper(observer)+`"`+extra+`}`)
	}

	// Real packets used by the existing ingestor tests.
	const (
		advA      = "120046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
		advATrans = "10AABB00000046D62DE27D4C5194D7821FC5A34A45565DCC2537B300B9AB6275255CEFB65D840CE5C169C94C9AED39E8BCB6CB6EB0335497A198B33A1A610CD3B03D8DCFC160900E5244280323EE0B44CACAB8F02B5B38B91CFA18BD067B0B5E63E94CFC85F758A8530B9240933402E0E6B8F84D5252322D52"
		advB      = "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
		advC      = "120073CFF971E1CB5754A742C152B2D2E0EB108A19B246D663ED8898A72C4A5AD86EA6768E66694B025EDF6939D5C44CFF719C5D5520E5F06B20680A83AD9C2C61C3227BBB977A85EE462F3553445FECF8EDD05C234ECE217272E503F14D6DF2B1B9B133890C923CDF3002F8FDC1F85045414BF09F8CB3"
	)
	// Synthetic frames (unsigned: ingested with signature checks off).
	advStale := "1100" + hex.EncodeToString(advertPayload(0x77, "Stale"))
	advScoped := transportFrame(regionKey("#be"), 4, advertPayload(0x55, "Scoped"))
	advBadSig := "1100" + hex.EncodeToString(advertPayload(0x66, "Forged"))
	grpNow := grpTxtFrame(t, keyNow, "alice: hello mesh")
	grpLater := grpTxtFrame(t, keyLater, "bob: decrypted later")
	grpOld := grpTxtFrame(t, keyNow, "carol: ancient")

	// The background jobs main starts at boot. On a fresh SQLite database
	// they run and record themselves; on Postgres they are seeded as done
	// (or skipped: path_json), so both end with the same bookkeeping rows.
	each("boot backfills", func(s *Store) error {
		s.BackfillPathJSONAsync()
		s.BackfillDefaultScopeAsync(regions)
		s.BackfillTransportCodesAsync()
		if err := s.RunAsyncMigration(context.Background(), "advert_route_evidence_v1", s.backfillAdvertEvidence); err != nil {
			return err
		}
		s.WaitForAsyncMigrations()
		return nil
	})

	// Observers come up: live status with metadata, radios and counters.
	for i, o := range []string{"obsa", "obsb", "obsc"} {
		send(cfg, "meshcore/SJC/"+o+"/status", false, fmt.Sprintf(
			`{"origin":"%s","model":"Heltec V3","firmware_version":"1.%d.0","client_version":"mctomqtt/1.0",`+
				`"radio":"869.618,62.5,8,8","radios":[{"id":"r%d","radio":"869.525,250,11,5"}],`+
				`"stats":{"battery_mv":%d,"uptime_secs":%d,"noise_floor":-11%d.5,"tx_air_secs":%d,"rx_air_secs":%d,"recv_errors":%d,"packets_sent":%d,"packets_recv":%d},"repeat":%v}`,
			strings.ToUpper(o), i, i, 3900+i, 1000*(i+1), i, 10*i, 20*i, i, 5*i, 7*i, i%2 == 0))
	}
	// A retained status refreshes metadata only.
	send(cfg, "meshcore/SJC/obsb/status", true, `{"origin":"OBSB-retained","model":"RAK4631"}`)
	// An observer whose clock ran ahead: its future last_seen is clamped.
	each("UpsertObserverAt future", func(s *Store) error {
		return s.UpsertObserverAt("obsd", "OBSD", "pdx", nil, "2099-01-01T00:00:00Z")
	})
	each("RecordNaiveSkew", func(s *Store) error {
		return s.RecordNaiveSkew("obsd", 7200, testStart)
	})

	// Adverts: one packet heard by three observers, an exact duplicate (dedups
	// into the same observation), and the same advert relayed on a transport
	// route (same transmission, different path).
	pkt(cfg, "obsa", advA, at(recent, 0), `,"SNR":5.5,"RSSI":-101`)
	pkt(cfg, "obsb", advA, at(recent, 1), `,"SNR":-3.25,"RSSI":-118,"rx_radio_id":"r1"`)
	pkt(cfg, "obsc", advA, at(recent, 2), `,"snr":1,"rssi":-110,"score":0.75`)
	pkt(cfg, "obsa", advA, at(recent, 3), `,"SNR":6.0`)
	pkt(cfg, "obsb", advATrans, at(recent, 4), `,"SNR":-1`)
	pkt(cfg, "obsa", advB, at(recent, 5), `,"SNR":2,"direction":"rx"`)
	pkt(cfg, "obsb", advB, at(recent, 6), `,"SNR":3,"score":750`)
	pkt(cfg, "obsc", advC, at(recent, 7), `,"SNR":-7.5`)
	pkt(cfg, "obsd", advC, at(recent, 8), "")
	pkt(cfgNoSig, "obsa", advScoped, at(recent, 9), "")
	pkt(cfgNoSig, "obsa", advStale, at(old, 0), "")
	// Earlier copy heard later: first_seen moves back to it.
	pkt(cfg, "obsc", advB, at(recent, -60), `,"SNR":4`)
	// A forged advert is dropped and recorded.
	pkt(cfg, "obsc", advBadSig, at(recent, 10), "")

	// A node not heard for 20 days (adverts stamp ingest time, so it is
	// written directly): MoveStaleNodes moves it to inactive_nodes.
	each("UpsertNode stale", func(s *Store) error {
		lat, lon := 50.85, 4.35
		return s.UpsertNode(strings.Repeat("42", 32), "Gone", "repeater", &lat, &lon, at(old, 0))
	})

	// Group text: decrypted at ingest, not yet decryptable, and old.
	pkt(cfg, "obsa", grpNow, at(recent, 20), `,"SNR":1.5`)
	pkt(cfg, "obsb", grpNow, at(recent, 21), `,"SNR":2.5`)
	pkt(cfg, "obsc", grpNow, at(recent, 22), `,"SNR":3.5`)
	pkt(cfg, "obsb", grpNow, at(recent, 23), `,"SNR":9`)
	pkt(cfg, "obsa", grpLater, at(recent, 24), "")
	// A new observer after many conflicting upserts: its idx (and the ids
	// of rows created after conflicting inserts generally) must match too.
	pkt(cfg, "obse", grpLater, at(recent, 25), `,"SNR":0.5`)
	// Old traffic for the packet prune. handleMessage stamps ingest time
	// (#1370), so these go through the same InsertTransmission with the
	// time set the way BuildPacketData would have set it 20 days ago.
	oldPkt := func(observer, raw, radio string) {
		t.Helper()
		decoded, err := DecodePacket(raw, ingestKeys, false)
		if err != nil {
			t.Fatal(err)
		}
		snr := -4.5
		pd := BuildPacketData(&MQTTPacketMessage{Raw: raw, SNR: &snr, RxRadioID: radio}, decoded, observer, "SJC", regions)
		pd.Timestamp = at(old, 0)
		each("old InsertTransmission", func(s *Store) error {
			d := *pd
			_, err := s.InsertTransmission(&d)
			return err
		})
	}
	oldPkt("obsa", grpOld, "r0")
	oldPkt("obsb", grpOld, "")
	oldPkt("obsa", advStale, "")

	// Mobile clients: coverage, RF samples, declared regions, node names;
	// recent and old rows for each prune.
	for _, ts := range []string{at(recent, 30), at(old, 30)} {
		send(cfg, "meshcore/client/"+testCompanionPK+"/packets", false,
			`{"raw":"`+advB+`","direction":"rx","timestamp":"`+ts+`","origin":"MyMob","SNR":-7.0,"RSSI":-92.0,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
		send(cfg, "meshcore/client/"+testCompanionPK+"/rf", false,
			`{"timestamp":"`+ts+`","uptime_secs":100.0,"noise_floor":-119.0,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
		send(cfg, "meshcore/client/"+testCompanionPK+"/regions", false,
			`{"type":"REGIONS","timestamp":"`+ts+`","target":"`+testTargetPK+`","regions":["be","be-vlg"],"truncated":false,"gps":{"lat":51.05,"lon":3.72,"acc_m":8.0}}`)
		send(cfg, "meshcore/client/"+testCompanionPK+"/names", false,
			`{"raw":"`+advB+`","direction":"rx","type":"NODE_NAME","timestamp":"`+ts+`","target":"`+testTargetPK+`","name":"Aegis","repeater_clock":1789000000}`)
	}

	// Neighbors report naming two of the advertised nodes.
	pubs := nodeKeys(t, lite)
	if len(pubs) < 4 {
		t.Fatalf("expected the adverts to create at least 4 nodes, got %v", pubs)
	}
	send(cfg, "meshcore/SJC/obsa/neighbors", false, `{"timestamp":"`+at(recent, 40)+`","origin":"OBSA","origin_id":"obsa",`+
		`"total_neighbors":2,"queried_neighbors":2,"self":{"scopes":"#be","default_scope":"*"},"neighbors":[`+
		`{"pubkey":"`+strings.ToUpper(pubs[0])+`","snr":5.5,"heard_secs_ago":30,"status":"responded","scopes":"be,eu"},`+
		`{"pubkey":"`+pubs[1]+`","snr":-2,"heard_secs_ago":90,"status":"timeout","scopes":""}]}`)

	// Server-written multibyte snapshot, applied through each store's queue
	// (the file on SQLite, the control table on Postgres).
	snap := mbcapqueue.Snapshot{WrittenAt: testStart, Entries: []mbcapqueue.Entry{
		{PublicKey: pubs[0], Status: "confirmed", Evidence: "advert"},
		{PublicKey: pubs[1], Status: "suspected", Evidence: "path"},
		{PublicKey: pubs[2], Status: "unknown"},
	}}
	each("multibyte snapshot", func(s *Store) error {
		if err := s.mbcap.WriteSnapshot(snap); err != nil {
			return err
		}
		_, err := s.RunMultibyteCapPersist()
		return err
	})

	// Background work main runs, here synchronously.
	each("BackfillFromPubkey", func(s *Store) error { s.BackfillFromPubkey(5000, 0, nil); return nil })
	each("key backfill", func(s *Store) error {
		b := newTestBackfiller(s, map[string]string{"#diffnow": keyNow, "#difflater": keyLater}, regions)
		if err := b.runKind(context.Background(), keyBackfillKindChannel); err != nil {
			return err
		}
		return b.runKind(context.Background(), keyBackfillKindRegion)
	})
	each("neighbor edges", func(s *Store) error {
		_, err := s.buildAndPersistNeighborEdges(trustAllPrefixes())
		return err
	})
	each("SaveScopeMatchTotals", func(s *Store) error { return s.SaveScopeMatchTotals() })
	each("SoftDeleteBlacklistedObservers", func(s *Store) error {
		s.SoftDeleteBlacklistedObservers([]string{"obsd"})
		return nil
	})

	// Retention, with rows on both sides of every cutoff.
	each("MoveStaleNodes", func(s *Store) error { _, err := s.MoveStaleNodes(10); return err })
	each("RemoveStaleObservers", func(s *Store) error { _, err := s.RemoveStaleObservers(10); return err })
	each("PurgeStaleObservers", func(s *Store) error { _, err := s.PurgeStaleObservers(10); return err })
	each("PruneOldPackets", func(s *Store) error { _, err := s.PruneOldPackets(10); return err })
	each("PruneOldMetrics", func(s *Store) error { _, err := s.PruneOldMetrics(10); return err })
	each("PruneDroppedPackets", func(s *Store) error { _, err := s.PruneDroppedPackets(10); return err })
	each("PruneOldClientReceptions", func(s *Store) error { _, err := s.PruneOldClientReceptions(10); return err })
	each("PruneOldClientRxObservations", func(s *Store) error { _, err := s.PruneOldClientRxObservations(10); return err })
	each("PruneOldClientRfSamples", func(s *Store) error { _, err := s.PruneOldClientRfSamples(10); return err })
	each("PruneOldClientDeclaredRegions", func(s *Store) error { _, err := s.PruneOldClientDeclaredRegions(10); return err })
	each("PruneOldClientNodeNames", func(s *Store) error { _, err := s.PruneOldClientNodeNames(10); return err })
	each("PruneNeighborEdges", func(s *Store) error { _, err := s.PruneNeighborEdges(10); return err })
	// Maintenance: real on SQLite, no-ops on Postgres; neither may fail.
	each("maintenance", func(s *Store) error {
		s.Checkpoint()
		s.RunIncrementalVacuum(100)
		s.CheckAutoVacuum(&Config{})
		s.RefreshPlannerStats(1000)
		return s.ConfigureObservationHops(0)
	})
	for _, s := range stores {
		s.WaitForAsyncMigrations()
	}
	testEnd := time.Now().UTC()

	compareBackends(t, lite.db, pg.db, testStart, testEnd)

	// The envelope score is a float64: a fraction must survive on Postgres
	// (a BIGINT column truncated it to 0 without an error), and an integral
	// score must stay integral on both.
	for _, c := range []struct {
		observer, raw string
		want          float64
	}{{"obsc", advA, 0.75}, {"obsb", advB, 750}} {
		for _, s := range stores {
			var got float64
			if err := s.db.QueryRow(`SELECT o.score FROM observations o
				JOIN observers obs ON obs.idx = o.observer_idx
				JOIN transmissions t ON t.id = o.transmission_id
				WHERE obs.id = ? AND t.raw_hex = ?`, c.observer, c.raw).Scan(&got); err != nil {
				t.Fatalf("score (pg=%v): %v", s.pg, err)
			}
			if got != c.want {
				t.Errorf("score for %s (pg=%v) = %v, want %v", c.observer, s.pg, got, c.want)
			}
		}
	}
}

// nodeKeys returns the public keys in nodes, sorted.
func nodeKeys(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT public_key FROM nodes ORDER BY public_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

// compareBackends dumps every table of both databases (every column, ordered
// by primary key) and reports each difference. It also requires the table
// sets to match, and that the sequence actually left rows in the tables that
// carry the ingest path, so an empty dump cannot pass by accident.
func compareBackends(t *testing.T, lite, pg *sql.DB, lo, hi time.Time) {
	t.Helper()
	liteTables := sqliteTableNames(t, lite)
	pgTables := map[string]bool{}
	rows, err := pg.Query(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		pgTables[n] = true
	}
	rows.Close()
	delete(pgTables, "schema_version") // Postgres's own migration bookkeeping
	for n := range wallClockColumns {
		if !pgTables[n] {
			t.Errorf("wallClockColumns names table %s, which does not exist", n)
		}
	}
	for _, n := range liteTables {
		if !pgTables[n] {
			t.Errorf("table %s exists on SQLite but not on Postgres", n)
		}
		delete(pgTables, n)
	}
	for n := range pgTables {
		t.Errorf("table %s exists on Postgres but not on SQLite", n)
	}

	nonEmpty := map[string]int{}
	for _, table := range liteTables {
		if reason, skip := backendOnlyTables[table]; skip {
			t.Logf("not compared: %s (%s)", table, reason)
			continue
		}
		cols, pk := sqliteColumns(t, lite, table)
		order := pk
		if len(order) == 0 {
			order = cols
		}
		q := "SELECT " + strings.Join(cols, ", ") + " FROM " + table + " ORDER BY " + strings.Join(order, ", ")
		a := dumpRows(t, lite, q, len(cols))
		b := dumpRows(t, pg, q, len(cols))
		nonEmpty[table] = len(a)
		if len(a) != len(b) {
			t.Errorf("%s: %d rows on SQLite, %d on Postgres\n  sqlite:   %v\n  postgres: %v", table, len(a), len(b), a, b)
			continue
		}
		clock := map[int]bool{}
		for _, c := range wallClockColumns[table] {
			found := false
			for i, name := range cols {
				if name == c {
					clock[i], found = true, true
				}
			}
			if !found {
				t.Errorf("wallClockColumns names %s.%s, which does not exist", table, c)
			}
		}
		for r := range a {
			for i := range cols {
				va, vb := a[r][i], b[r][i]
				if clock[i] {
					if err := checkWallClock(va, vb, lo.Add(-wallClockSlack[table+"."+cols[i]]), hi); err != nil {
						t.Errorf("%s row %d (%s=%v) wall-clock column %s: %v", table, r, order[0], a[r][0], cols[i], err)
					}
					continue
				}
				if render(va) != render(vb) {
					t.Errorf("%s row %d (%s): sqlite %s, postgres %s\n  sqlite row:   %v\n  postgres row: %v",
						table, r, cols[i], render(va), render(vb), renderRow(a[r]), renderRow(b[r]))
				}
			}
		}
	}

	// The sequence must have exercised the write paths it claims to.
	for _, table := range []string{
		"transmissions", "observations", "observation_radios", "nodes", "inactive_nodes",
		"observers", "observer_metrics", "observer_radios", "observer_neighbor_reports",
		"advert_route_evidence", "dropped_packets", "neighbor_edges", "tx_rewrite_feed",
		"client_observers", "client_receptions", "client_rx_observations", "client_rf_samples",
		"node_declared_regions", "client_node_names", "scope_match_totals", "key_backfill_done",
	} {
		if nonEmpty[table] == 0 {
			t.Errorf("table %s is empty after the sequence: the comparison proves nothing for it", table)
		}
	}
}

func sqliteTableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'
		AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// sqliteColumns returns table's columns in declaration order and its primary
// key columns in key order.
func sqliteColumns(t *testing.T, db *sql.DB, table string) (cols, pk []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type pkCol struct {
		name string
		pos  int
	}
	var pks []pkCol
	for rows.Next() {
		var name string
		var pos int
		if err := rows.Scan(&name, &pos); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
		if pos > 0 {
			pks = append(pks, pkCol{name, pos})
		}
	}
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	for _, p := range pks {
		pk = append(pk, p.name)
	}
	return cols, pk
}

func dumpRows(t *testing.T, db *sql.DB, q string, n int) [][]any {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// render is a value's comparable form: numbers by value (an INTEGER and a
// REAL holding 5 compare equal, as they do in SQL), text quoted so it never
// equals a number.
func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return strconv.Quote(x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%T(%v)", v, v)
	}
}

func renderRow(r []any) string {
	parts := make([]string, len(r))
	for i, v := range r {
		parts[i] = render(v)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// checkWallClock accepts a pair of wall-clock values when they are equal, or
// both are times within [lo-2s, hi+2s].
func checkWallClock(a, b any, lo, hi time.Time) error {
	if renderClock(a) == renderClock(b) {
		return nil
	}
	if (a == nil) != (b == nil) {
		return fmt.Errorf("sqlite %s, postgres %s: NULL on one side only", render(a), render(b))
	}
	if a == nil {
		return nil
	}
	for _, v := range []any{a, b} {
		ts, ok := parseWallClock(v)
		if !ok {
			return fmt.Errorf("%s is not a time", render(v))
		}
		if ts.Before(lo.Add(-2*time.Second)) || ts.After(hi.Add(2*time.Second)) {
			return fmt.Errorf("%s is outside the test run [%s, %s]", render(v), lo.Format(time.RFC3339), hi.Format(time.RFC3339))
		}
	}
	return nil
}

// renderClock is render, with a time.Time (mattn/go-sqlite3 parses a
// DATETIME column) in the text form both engines store.
func renderClock(v any) string {
	if ts, ok := v.(time.Time); ok {
		return strconv.Quote(ts.UTC().Format(sqliteDatetimeLayout))
	}
	return render(v)
}

func parseWallClock(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case int64:
		if x > 1e12 { // milliseconds
			return time.UnixMilli(x), true
		}
		return time.Unix(x, 0), true
	case float64:
		return time.Unix(int64(x), 0), true
	case string:
		for _, layout := range []string{time.RFC3339Nano, sqliteDatetimeLayout, "2006-01-02T15:04:05"} {
			if ts, err := time.Parse(layout, x); err == nil {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}
