package main

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// #2267: byNode includes senders and destinations, not only observed relays.
func TestCertainForRequiresObservedEvidence(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalidAdvert=%t", invalid), func(t *testing.T) {
			s := chStore()
			tx := chTx(19001, 1, 2, 1, time.Now())
			tx.RawHex = "0900" + strings.Repeat("00", 100)
			tx.DecodedJSON = fmt.Sprintf(`{"srcPubKey":%q,"destPubKey":%q}`, chA, chC)
			if invalid {
				tx = chTx(19002, 1, 4, 1, time.Now())
				tx.RawHex = "1100" + strings.Repeat("00", 100)
				tx.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"signatureValid":false}`, chC)
			}
			s.indexByNode(tx)
			if len(s.byNode[chC]) != 1 || tx.signatureFailed() != invalid {
				t.Fatal("fixture must retain the broad attribution and signature state")
			}
			if certainFor(tx, chC, chPM()) {
				t.Error("a destination or failed advert without observed relay evidence is certain")
			}
		})
	}
}

// #2266: these two frames have the same content hash. The first frame's
// width/route must never qualify the later observation's path as certain.
func TestCertainObservationHeaderEvidence(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%t", direct), func(t *testing.T) {
			first := "1540" + strings.Repeat("00", 18)
			next, path := "15014f"+strings.Repeat("00", 18), `["4F"]`
			if direct {
				next, path = "1641"+lpRelay[:4]+strings.Repeat("00", 18), `["4F2A"]`
			}
			if ComputeContentHash(first) != ComputeContentHash(next) {
				t.Fatal("fixture must represent the same deduplicated payload")
			}
			db, w, at := lpFixture(t, true, true)
			ceSetFrames(t, w, at, first, next, path, true)
			s := NewPacketStore(db, &PacketStoreConfig{})
			if err := s.Load(); err != nil {
				t.Fatal(err)
			}
			heard, _ := s.LastHeardMap([]string{lpRelay})
			if !heard[lpRelay].IsZero() {
				t.Errorf("relay credited using another observation's header: %s", heard[lpRelay])
			}
		})
	}
}

func ceSetFrames(t *testing.T, w *sql.DB, at, first, next, path string, resolved bool) {
	t.Helper()
	var rp interface{}
	if resolved {
		rp = `["` + lpRelay + `"]`
	}
	for _, q := range []struct {
		sql  string
		args []interface{}
	}{
		{`ALTER TABLE nodes ADD COLUMN advert_count INTEGER DEFAULT 0`, nil},
		{`ALTER TABLE nodes ADD COLUMN battery_mv INTEGER`, nil},
		{`ALTER TABLE nodes ADD COLUMN temperature_c REAL`, nil},
		{`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`, nil},
		{`UPDATE transmissions SET raw_hex=?`, []interface{}{first}},
		{`UPDATE observations SET raw_hex=?, path_json='[]', resolved_path=NULL`, []interface{}{first}},
		{`INSERT INTO nodes(public_key,name,role) VALUES (?,'Other repeater','repeater')`, []interface{}{"4f8b" + strings.Repeat("9", 60)}},
		{`INSERT INTO observations(id,transmission_id,observer_id,observer_name,direction,path_json,timestamp,raw_hex,resolved_path)
			VALUES(2,1,'synthetic-observer','Synthetic','rx',?,?,?,?)`, []interface{}{path, at, next, rp}},
	} {
		if _, err := w.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

func TestCertainEvidenceAcrossLoadPaths(t *testing.T) {
	payload := strings.Repeat("00", 18)
	cases := []struct {
		name, first, raw, path string
		want                   bool
	}{
		{"direct planned hop", "1540", "16414f2a", `["4F2A"]`, false},
		{"transport direct", "1540", "1700000000414f2a", `["4F2A"]`, false},
		{"ambiguous one-byte flood", "1540", "15014f", `["4F"]`, false},
		{"observed two-byte flood", "1540", "15414f2a", `["4F2A"]`, true},
		{"observed transport flood", "1540", "1400000000814f2a55", `["4F2A55"]`, true},
		{"direct representative then flood", "1600", "15414f2a", `["4F2A"]`, true},
		{"narrow representative then wider flood", "1500", "15414f2a", `["4F2A"]`, true},
		{"missing observation header", "1540", "", `["4F2A"]`, false},
		{"legacy schema without observation headers", "1540", "15414f2a", `["4F2A"]`, false},
		{"unreadable observation header", "1540", "not-hex", `["4F2A"]`, false},
		{"resolved node absent from path", "1540", "154140aa", `["40AA"]`, false},
	}
	for _, c := range cases {
		for _, mode := range []string{"Load", "loadChunk", "LoadChunked", "live", "late"} {
			for _, resolved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/resolved=%t", c.name, mode, resolved), func(t *testing.T) {
					db, w, at := lpFixture(t, true, true)
					first, raw := c.first+payload, c.raw
					if raw != "" && raw != "not-hex" {
						raw += payload
						if ComputeContentHash(first) != ComputeContentHash(raw) {
							t.Fatal("fixture frames have different content hashes")
						}
					}
					ceSetFrames(t, w, at, first, raw, c.path, resolved)
					if c.name == "legacy schema without observation headers" {
						db.hasObsRawHex = false
					}
					s := NewPacketStore(db, &PacketStoreConfig{})
					s.useResolvedPathIndex = true
					s.initResolvedPathIndex()
					var err error
					switch mode {
					case "Load":
						err = s.Load()
					case "loadChunk":
						err = s.loadChunk(time.Now().Add(-time.Hour), time.Now())
					case "LoadChunked":
						err = s.LoadChunked(1)
					case "live":
						s.IngestNewFromDB(0, 100)
					case "late":
						// Stage observation 2 outside the loader's table until
						// the representative frame is already in memory.
						if _, err := w.Exec(`CREATE TEMP TABLE pending AS SELECT * FROM observations WHERE id=2`); err != nil {
							t.Fatal(err)
						}
						if _, err := w.Exec(`DELETE FROM observations WHERE id=2`); err != nil {
							t.Fatal(err)
						}
						if err = s.Load(); err != nil {
							t.Fatal(err)
						}
						if heard, _ := s.LastHeardMap([]string{lpRelay}); !heard[lpRelay].IsZero() {
							t.Fatal("zero-hop representative credited a relay")
						}
						if _, err := w.Exec(`INSERT INTO observations SELECT * FROM pending`); err != nil {
							t.Fatal(err)
						}
						s.IngestNewObservations(1, 100)
					}
					if err != nil {
						t.Fatal(err)
					}
					tx := s.byTxID[1]
					if tx == nil || len(tx.Observations) != 2 {
						t.Fatal("both observations must be loaded")
					}
					for _, obs := range tx.Observations {
						ceAssertNoRaw(t, obs)
					}
					heard, _ := s.LastHeardMap([]string{lpRelay})
					if got := !heard[lpRelay].IsZero(); got != c.want {
						t.Errorf("LastHeardMap evidence = %v; want %v", got, c.want)
					}
					wantRelay := ""
					if c.want {
						wantRelay = at
					}
					assertCertainRelay(t, s, lpRelay, wantRelay)
					_, pm := s.getCachedNodesAndPM()
					if got := certainFor(tx, lpRelay, pm); got != c.want {
						t.Errorf("certainFor = %v; want %v", got, c.want)
					}
					analytics, err := s.GetNodeAnalytics(lpRelay, 1)
					if err != nil || analytics == nil {
						t.Fatalf("analytics: %v", err)
					}
					total := 0
					for _, bucket := range analytics.ActivityTimeline {
						if bucket.Certain != nil {
							total += *bucket.Certain
						}
					}
					if (total == 1) != c.want || total > 1 {
						t.Errorf("analytics certain count = %d; want certain=%v", total, c.want)
					}
				})
			}
		}
	}
}

// Works whether the lean observation omits RawHex entirely or retains an
// empty compatibility field; neither may retain the full wire packet.
func ceAssertNoRaw(t *testing.T, obs *StoreObs) {
	t.Helper()
	field := reflect.ValueOf(obs).Elem().FieldByName("RawHex")
	if field.IsValid() && field.String() != "" {
		t.Error("certainty must not retain each observation's full raw packet")
	}
}

func TestCertainAnalyticsRejectsBroadAttribution(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalidAdvert=%t", invalid), func(t *testing.T) {
			db, w, at := lpFixture(t, false, true)
			ceSetFrames(t, w, at, "1100", "1100", `[]`, false)
			if _, err := w.Exec(`DELETE FROM nodes WHERE public_key<>?`, lpRelay); err != nil {
				t.Fatal(err)
			}
			decoded := fmt.Sprintf(`{"srcPubKey":%q,"destPubKey":%q}`, chA, lpRelay)
			if invalid {
				decoded = fmt.Sprintf(`{"pubKey":%q,"signatureValid":false}`, lpRelay)
			}
			if _, err := w.Exec(`UPDATE transmissions SET decoded_json=?, first_seen=?`, decoded, time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339)); err != nil {
				t.Fatal(err)
			}
			// A genuine own packet keeps the learning window open through the
			// bad attribution; its presence must not legitimise that packet.
			if _, err := w.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,route_type,payload_type,payload_version,decoded_json)
				VALUES(2,'1100','certain-own',?,1,4,0,?)`, at, fmt.Sprintf(`{"pubKey":%q,"signatureValid":true}`, lpRelay)); err != nil {
				t.Fatal(err)
			}
			s := NewPacketStore(db, &PacketStoreConfig{})
			if err := s.Load(); err != nil {
				t.Fatal(err)
			}
			if len(s.byNode[lpRelay]) != 2 {
				t.Fatal("broad analytics attribution must retain both packets")
			}
			// Keep the learning window independent of fixture's short history.
			s.oldestLoaded = ""
			analytics, err := s.GetNodeAnalytics(lpRelay, 1)
			if err != nil || analytics == nil {
				t.Fatalf("analytics: %v", err)
			}
			certain, attributed := 0, 0
			for _, bucket := range analytics.ActivityTimeline {
				attributed += bucket.Count
				if bucket.Certain != nil {
					certain += *bucket.Certain
				}
			}
			if certain != 1 || attributed != 2 {
				t.Errorf("analytics counts: certain=%d attributed=%d; want 1 and 2", certain, attributed)
			}
			total := 0
			for _, count := range s.CertainActivity([]string{lpRelay}, time.Now())[lpRelay].Hourly {
				total += count
			}
			if total != 1 {
				t.Errorf("adaptive activity counts %d certain packets; want 1", total)
			}
		})
	}
}

func TestCertainEvidenceSurvivesObservationDedup(t *testing.T) {
	for _, mode := range []string{"Load", "loadChunk", "LoadChunked", "live", "late"} {
		for _, floodFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/floodFirst=%t", mode, floodFirst), func(t *testing.T) {
				db, w, at := lpFixture(t, true, true)
				first, next := "16414f2a00", "15414f2a00"
				if floodFirst {
					first, next = next, first
				}
				ceSetFrames(t, w, at, first, next, `["4F2A"]`, true)
				if _, err := w.Exec(`UPDATE observations SET observer_id='same-observer',path_json='["4F2A"]',resolved_path=?`, `["`+lpRelay+`"]`); err != nil {
					t.Fatal(err)
				}
				// Load sorts newest first; make the first frame sort first.
				if _, err := w.Exec(`UPDATE observations SET timestamp=? WHERE id=1`, time.Now().UTC().Format(time.RFC3339)); err != nil {
					t.Fatal(err)
				}
				s := NewPacketStore(db, &PacketStoreConfig{})
				var err error
				switch mode {
				case "Load":
					err = s.Load()
				case "loadChunk":
					err = s.loadChunk(time.Now().Add(-time.Hour), time.Now())
				case "LoadChunked":
					err = s.LoadChunked(1)
				case "live":
					s.IngestNewFromDB(0, 100)
				case "late":
					if _, err := w.Exec(`DELETE FROM observations WHERE id=2`); err != nil {
						t.Fatal(err)
					}
					if err = s.Load(); err != nil {
						t.Fatal(err)
					}
					if _, err := w.Exec(`INSERT INTO observations(id,transmission_id,observer_id,path_json,timestamp,raw_hex)
						VALUES(2,1,'same-observer','["4F2A"]',?,?)`, at, next); err != nil {
						t.Fatal(err)
					}
					s.IngestNewObservations(1, 100)
				}
				if err != nil {
					t.Fatal(err)
				}
				tx := s.byTxID[1]
				if tx == nil || len(tx.Observations) != 1 {
					t.Fatal("same observer/path must still occupy one observation")
				}
				heard, _ := s.LastHeardMap([]string{lpRelay})
				_, pm := s.getCachedNodesAndPM()
				if heard[lpRelay].IsZero() || !certainFor(tx, lpRelay, pm) {
					t.Fatal("dedup discarded the genuine flood observation")
				}
				assertCertainRelay(t, s, lpRelay, at)
			})
		}
	}
}

func TestObservedRelayEvidenceValidation(t *testing.T) {
	for _, c := range []struct {
		name, raw, path string
		want            uint8
	}{
		{"flood", "15414f2a00", `["4f2A"]`, 4},
		{"transport flood", "1400000000814f2a5500", `["4F2A55"]`, 6},
		{"direct", "16414f2a00", `["4F2A"]`, 0},
		{"transport direct", "1700000000414f2a00", `["4F2A"]`, 0},
		{"TRACE header", "25414f2a00", `["4F2A"]`, 0},
		{"missing", "", `["4F2A"]`, 0},
		{"zero hop", "154000", `["4F2A"]`, 0},
		{"truncated", "15414f", `["4F2A"]`, 0},
		{"nonhex hop", "15414fzz00", `["4FZZ"]`, 0},
		{"reserved width", "15c14f2a5566", `["4F2A5566"]`, 0},
		{"mismatched path", "15414f2a00", `["4F8B"]`, 0},
		{"extra path entry", "15414f2a00", `["4F2A","4F8B"]`, 0},
		{"oversized path", "1596" + strings.Repeat("4f2a55", 22), `[` + strings.TrimSuffix(strings.Repeat(`"4F2A55",`, 22), ",") + `]`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := observedRelayHopHexLen(c.raw, c.path); got != c.want {
				t.Errorf("evidence width = %d; want %d", got, c.want)
			}
		})
	}
}

func TestCertainObservedRelayRespectsSignatureAndCurrentPrefixes(t *testing.T) {
	pm := chPM()
	tx := chTx(1, 1, 4, 2, time.Now())
	chObservation(tx, chC)
	if !certainFor(tx, chC, pm) {
		t.Fatal("control: actual unique flood relay must be certain")
	}
	pm.retired = map[string]struct{}{chC[:4]: {}}
	if certainFor(tx, chC, pm) {
		t.Fatal("new retired namesake must make the retained evidence uncertain")
	}
	pm = chPM()
	pm.m[chC[:4]] = append(pm.m[chC[:4]], nodeInfo{PublicKey: chC[:4] + strings.Repeat("a", 60), Role: "repeater"})
	if certainFor(tx, chC, pm) {
		t.Fatal("new active namesake must make the retained evidence uncertain")
	}
	for _, valid := range []bool{false, true} {
		tx := chTx(2, 1, 4, 2, time.Now())
		tx.DecodedJSON = fmt.Sprintf(`{"pubKey":%q,"signatureValid":%t}`, chA, valid)
		s := chStore()
		chIndexResolved(s, tx, []string{chC}, map[string]bool{}, chPM())
		if got := certainFor(tx, chC, chPM()); got != valid {
			t.Errorf("relay certainty=%v with signatureValid=%v", got, valid)
		}
		if got := s.certainHeard[chC] != ""; got != valid {
			t.Errorf("relay last-heard=%v with signatureValid=%v", got, valid)
		}
	}
}

func TestCertainEvidenceConcurrentLateDedup(t *testing.T) {
	db, w, at := lpFixture(t, true, true)
	if _, err := w.Exec(`UPDATE observations SET observer_id='same-observer',raw_hex='16414f2a00'`); err != nil {
		t.Fatal(err)
	}
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	// Use the cached prefix map as the learner does, without a fixture-only
	// analytics schema query in each iteration.
	_, pm := s.getCachedNodesAndPM()
	if _, err := w.Exec(`INSERT INTO observations(id,transmission_id,observer_id,path_json,timestamp,raw_hex)
		VALUES(2,1,'same-observer','["4F2A"]',?,'15414f2a00')`, at); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				s.mu.RLock()
				certainFor(s.byTxID[1], lpRelay, pm)
				s.mu.RUnlock()
				s.CertainActivity([]string{lpRelay}, time.Now())
				s.LastHeardMap([]string{lpRelay})
				s.LastCertainRelayMap([]string{lpRelay})
			}
		}()
	}
	close(start)
	for i := 0; i < 20; i++ {
		s.IngestNewObservations(1, 100)
	}
	wg.Wait()
	if !certainFor(s.byTxID[1], lpRelay, pm) || len(s.byTxID[1].Observations) != 1 {
		t.Fatal("late evidence was lost or stored as a duplicate observation")
	}
}
