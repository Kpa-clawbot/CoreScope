package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

func TestCoreScopeBenchmarkSmallCorpus(t *testing.T) {
	dir := t.TempDir()
	c := benchConfig{Corpus: "S", Seed: 20261008, Epoch: 1791451200, SQLite: filepath.Join(dir, "corpus.sqlite"), Output: dir, Shape: benchShape{Transmissions: 100, Observations: 300, Nodes: 200, Observers: 16, Days: 2}}
	if e := benchPrepare(c); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", c.SQLite+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for table, want := range map[string]int{"transmissions": 100, "observations": 300, "nodes": 200, "observers": 16} {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n != want {
			t.Fatalf("%s=%d want %d", table, n, want)
		}
	}
	var invalid int
	if e := db.QueryRow(`SELECT count(*) FROM observations o LEFT JOIN transmissions t ON t.id=o.transmission_id WHERE t.id IS NULL`).Scan(&invalid); e != nil {
		t.Fatal(e)
	}
	if invalid != 0 {
		t.Fatal("generator created orphans")
	}
	if e := db.QueryRow(`SELECT count(*) FROM observations WHERE score IS NOT NULL AND typeof(score) != 'integer'`).Scan(&invalid); e != nil {
		t.Fatal(e)
	}
	if invalid != 0 {
		t.Fatalf("%d fractional scores would make the unchanged baseline silently discard corpus rows", invalid)
	}
	// The handler returns void, so prove its durable effects rather than
	// treating delivery to the callback as a successful ingestion.
	db.Close()
	store, e := OpenStore(c.SQLite)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	store.WaitForAsyncMigrations()
	f := benchFixtureFor(c)
	packet, e := f.packet(1_000_001, 0, c.Epoch)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]any{"raw": packet.RawHex, "origin": "Synthetic bench", "SNR": 6.5, "RSSI": -95.5})
	handleMessage(store, "bench", MQTTSource{Name: "bench"}, &mockMessage{topic: "meshcore/AAA/" + f.public[0] + "/packets", payload: body}, f.channels, nil, &Config{})
	var inserted int
	if e := store.db.QueryRow(`SELECT count(*) FROM transmissions WHERE hash=?`, packet.Hash).Scan(&inserted); e != nil {
		t.Fatal(e)
	}
	if inserted != 1 || store.Stats.WriteErrors.Load() != 0 {
		t.Fatal("handler callback did not produce a durable synthetic transmission")
	}
}

func TestCoreScopeBenchmarkNullableObservationIdentity(t *testing.T) {
	dir := t.TempDir()
	// This includes the first collision in the real B key space without
	// requiring a two-million-row fixture for the regression itself.
	c := benchConfig{Corpus: "B", Seed: 20261008, Epoch: 1791451200, SQLite: filepath.Join(dir, "corpus.sqlite"), Output: dir,
		Shape: benchShape{Transmissions: 610, Observations: 9760, Nodes: 2000, Observers: 128, Days: 8}}
	if err := benchPrepare(c); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", c.SQLite+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows, missing, duplicateKeys int
	if err := db.QueryRow(`SELECT count(*),sum(observer_idx IS NULL) FROM observations`).Scan(&rows, &missing); err != nil {
		t.Fatal(err)
	}
	if rows != 9760 || missing != 366 {
		t.Fatalf("generator changed fanout/NULL coverage: observations=%d, NULL observers=%d", rows, missing)
	}
	if err := db.QueryRow(`SELECT count(*) FROM (
		SELECT o.transmission_id,coalesce(obs.id,''),o.path_json FROM observations o
		LEFT JOIN observers obs ON obs.rowid=o.observer_idx
		GROUP BY o.transmission_id,coalesce(obs.id,''),o.path_json HAVING count(*)>1
	)`).Scan(&duplicateKeys); err != nil {
		t.Fatal(err)
	}
	if duplicateKeys != 0 {
		t.Fatalf("%d generated observer/path keys would be dropped by the unchanged upstream loader", duplicateKeys)
	}
}

func benchOpen(c benchConfig) (*Store, error) { return OpenStore(c.SQLite) }
func benchSettings(s *Store) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range []string{"journal_mode", "synchronous", "foreign_keys", "busy_timeout", "cache_size", "page_size", "mmap_size", "cache_spill", "journal_size_limit", "wal_autocheckpoint"} {
		var v string
		if e := s.db.QueryRow("PRAGMA " + name).Scan(&v); e != nil {
			return nil, e
		}
		out[name] = v
	}
	var version string
	if e := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); e != nil {
		return nil, e
	}
	out["sqlite_version"] = version
	if out["journal_mode"] != "wal" || out["synchronous"] != "2" || out["foreign_keys"] != "1" {
		return nil, fmt.Errorf("SQLite durability differs from WAL/FULL/FK contract")
	}
	return out, nil
}

func benchExplain(s *Store, query string, args ...any) (any, error) {
	rows, e := s.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	type step struct {
		ID     int    `json:"id"`
		Parent int    `json:"parent"`
		Detail string `json:"detail"`
	}
	var result []step
	for rows.Next() {
		var item step
		var unused int
		if e := rows.Scan(&item.ID, &item.Parent, &unused, &item.Detail); e != nil {
			return nil, e
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func benchPrepare(c benchConfig) error {
	if _, e := os.Stat(c.SQLite); !os.IsNotExist(e) {
		return fmt.Errorf("corpus path must be new")
	}
	s, e := OpenStore(c.SQLite)
	if e != nil {
		return e
	}
	defer s.Close()
	s.WaitForAsyncMigrations()
	f := benchFixtureFor(c)
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	stamp := time.Unix(c.Epoch, 0).UTC().Format(time.RFC3339)
	exec := func(q string, args ...any) error { _, e := tx.Exec(q, args...); return e }
	for i, pk := range f.public {
		role := []string{"repeater", "companion", "room", "sensor"}[i%4]
		if e := exec(`INSERT INTO nodes(public_key,name,role,lat,lon,first_seen,last_seen,advert_count) VALUES(?,?,?,?,?,?,?,0)`, pk, fmt.Sprintf("Synthetic %04d", i), role, 20+float64(i%2000)/10000, 30+float64(i%2000)/10000, stamp, stamp); e != nil {
			return e
		}
	}
	for i := 0; i < c.Shape.Observers; i++ {
		if e := exec(`INSERT INTO observers(rowid,id,name,iata,last_seen,first_seen,packet_count,noise_floor,can_relay,can_relay_seen) VALUES(?,?,?,?,?,?,0,-110.5,1,1)`, i+1, f.public[i], fmt.Sprintf("Synthetic observer %d", i), []string{"AAA", "BBB", "CCC", "DDD"}[i%4], stamp, stamp); e != nil {
			return e
		}
	}
	transmission, e := tx.Prepare(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,route_type,payload_type,payload_version,decoded_json,channel_hash,from_pubkey,last_seen,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if e != nil {
		return e
	}
	defer transmission.Close()
	observation, e := tx.Prepare(`INSERT INTO observations(transmission_id,observer_idx,direction,snr,rssi,score,path_json,timestamp,raw_hex,resolved_path) VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if e != nil {
		return e
	}
	defer observation.Close()
	evidence, e := tx.Prepare(`INSERT INTO advert_route_evidence(tx_id,bit) VALUES(?,?)`)
	if e != nil {
		return e
	}
	defer evidence.Close()
	var observationCount int64
	countsByDay := make([]int, c.Shape.Days)
	var newest, oldest string
	for i := 0; i < c.Shape.Transmissions; i++ {
		at := benchStamp(c, i)
		p, e := f.packet(i, 0, at)
		if e != nil {
			return e
		}
		count := benchFanout(c, i)
		if _, e = transmission.Exec(i+1, p.RawHex, p.Hash, p.Timestamp, p.RouteType, p.PayloadType, p.PayloadVersion, p.DecodedJSON, p.ChannelHash, nilIfEmpty(p.FromPubkey), at+int64(count-1), stamp); e != nil {
			return e
		}
		bits := packetpath.AdvertRouteEvidence(p.RawHex)
		observationKeys := make(map[string]bool, count)
		for o := 0; o < count; o++ {
			generated, e := f.observation(i, o, at, observationKeys)
			if e != nil {
				return e
			}
			seen, observer := generated.Packet, generated.ObserverIndex
			var snr any = float64((i+o)%20) - 10.5
			if o%11 == 10 {
				snr = nil
			}
			var resolved any
			if i%3 != 0 && seen.PathJSON != "[]" {
				b, _ := json.Marshal([]string{f.public[(i+generated.PathVariant*7+1)%len(f.public)]})
				resolved = string(b)
			}
			if _, e = observation.Exec(i+1, observer, "rx", snr, -100.5+float64(o%25), p.Score, seen.PathJSON, at+int64(o), seen.RawHex, resolved); e != nil {
				return e
			}
			observationCount++
			bits |= packetpath.AdvertRouteEvidence(seen.RawHex)
		}
		if p.PayloadType == 4 {
			for _, bit := range []uint8{1, 2} {
				if bits&bit != 0 {
					if _, e = evidence.Exec(i+1, bit); e != nil {
						return e
					}
				}
			}
		}
		countsByDay[(i/10)%c.Shape.Days]++
		if i == 0 {
			newest = p.Hash
		}
		if i == 70 {
			oldest = p.Hash
		}
	}
	if observationCount != int64(c.Shape.Observations) {
		return fmt.Errorf("fanout generated %d observations, expected %d", observationCount, c.Shape.Observations)
	}
	if e := exec(`UPDATE nodes SET advert_count=(SELECT count(*) FROM transmissions t WHERE t.payload_type=4 AND t.from_pubkey=nodes.public_key)`); e != nil {
		return e
	}
	for i := 0; i < c.Shape.Nodes; i++ {
		if e := exec(`INSERT INTO neighbor_edges(node_a,node_b,count,last_seen) VALUES(?,?,?,?)`, f.public[i], f.public[(i+1)%len(f.public)], 1+i%100, stamp); e != nil {
			return e
		}
	}
	metric, e := tx.Prepare(`INSERT INTO observer_metrics(observer_id,timestamp,noise_floor,tx_air_secs,rx_air_secs,recv_errors,battery_mv,packets_sent,packets_recv) VALUES(?,?,?,?,?,?,?,?,?)`)
	if e != nil {
		return e
	}
	defer metric.Close()
	for observer := 0; observer < c.Shape.Observers; observer++ {
		for sample := 0; sample < c.Shape.Days*288; sample++ {
			if observer == c.Shape.Observers-1 && sample%48 != 0 {
				continue
			}
			at := time.Unix(c.Epoch-int64(c.Shape.Days*86400)+int64(sample*300), 0).UTC().Format(time.RFC3339)
			if _, e := metric.Exec(f.public[observer], at, -110.5+float64(observer%5), sample*2, sample*9, sample%3, 3700+observer%200, sample*3, sample*20); e != nil {
				return e
			}
		}
	}
	// Every enabled mobile feature has deterministic data; this is the base
	// corpus extension, not a claim to have run the optional million-RX corpus.
	reception, e := tx.Prepare(`INSERT INTO client_receptions(rx_pubkey,heard_key,heard_keylen,rssi,snr,lat,lon,pos_acc_m,rx_at,ingested_at,src) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if e != nil {
		return e
	}
	defer reception.Close()
	for i := 0; i < c.Shape.Transmissions; i++ {
		key := f.public[i%len(f.public)]
		size := []int{32, 8, 3, 2}[i%4]
		at := time.Unix(benchStamp(c, i), int64(i)).UTC().Format("2006-01-02T15:04:05.000000000Z")
		if _, e := reception.Exec(f.public[(i+100)%len(f.public)], key[:size*2], size, -95, 6.5, 20+float64(i%2000)/10000, 30+float64(i%2000)/10000, 5, at, stamp, "synthetic"); e != nil {
			return e
		}
	}
	for i := 0; i < 100; i++ {
		pk := f.public[i%len(f.public)]
		at := time.Unix(c.Epoch-int64(i*300), 0).UTC().Format(time.RFC3339)
		if e := exec(`INSERT INTO client_observers(pubkey,name,last_seen) VALUES(?,?,?)`, pk, fmt.Sprintf("Synthetic mobile %d", i), stamp); e != nil {
			return e
		}
		if e := exec(`INSERT INTO node_declared_regions(target,rx_pubkey,observed_at,ingested_at,regions_csv,truncated,lat,lon) VALUES(?,?,?,?,?,0,20,30)`, pk, pk, at, stamp, "#bench"); e != nil {
			return e
		}
		if e := exec(`INSERT INTO client_rf_samples(rx_pubkey,sampled_at,ingested_at,lat,lon,stationary,uptime_secs,noise_floor,last_snr) VALUES(?,?,?,20,30,0,1000,-110,6.5)`, pk, at, stamp); e != nil {
			return e
		}
		if e := exec(`INSERT INTO dropped_packets(hash,raw_hex,reason,observer_id,dropped_at) VALUES(?,?,?,?,?)`, fmt.Sprintf("synthetic-drop-%d", i), "", "synthetic-rejection", pk, stamp); e != nil {
			return e
		}
	}
	if e := exec(`INSERT INTO client_rx_observations(rx_pubkey,rx_at,ingested_at,pkt_hash,route_type,payload_type,hash_size,hop_count,lat,lon) SELECT ?,first_seen,?,hash,route_type,payload_type,1,0,20,30 FROM transmissions WHERE id<=100`, f.public[0], stamp); e != nil {
		return e
	}
	if e := exec(`INSERT INTO inactive_nodes(public_key,name,role,first_seen,last_seen,advert_count) VALUES('ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff','Synthetic inactive','repeater',?,?,0)`, stamp, stamp); e != nil {
		return e
	}
	if e := exec(`INSERT INTO advert_evidence_backfill(id,tx_cursor,obs_cursor) VALUES(1,?,?)`, c.Shape.Transmissions, c.Shape.Observations); e != nil {
		return e
	}
	if e := exec(`UPDATE _async_migrations SET started_at=?,ended_at=?`, stamp, stamp); e != nil {
		return e
	}
	if e := exec(`INSERT OR REPLACE INTO _async_migrations(name,status,started_at,ended_at) VALUES('advert_route_evidence_v1','done',?,?)`, stamp, stamp); e != nil {
		return e
	}
	if e := exec(`UPDATE scope_match_totals SET since_unix=?,updated_unix=?`, c.Epoch, c.Epoch); e != nil {
		return e
	}
	if e := tx.Commit(); e != nil {
		return e
	}
	if _, e := s.db.Exec(`ANALYZE`); e != nil {
		return e
	}
	summary := map[string]any{"transmissions": c.Shape.Transmissions, "observations": observationCount, "transmissions_by_day": countsByDay, "nodes": c.Shape.Nodes, "observers": c.Shape.Observers, "channels": 20, "rx_rows": c.Shape.Transmissions, "dense_observer": f.public[0], "sparse_observer": f.public[c.Shape.Observers-1], "node": f.public[0], "new_packet": newest, "old_packet": oldest, "epoch": c.Epoch, "protocol_source": "meshcore-dev/MeshCore@a366955cb2f67b8e6842d4f00d2b6a554dddd88a"}
	b, e := json.MarshalIndent(summary, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(c.Output, "corpus.json"), b, 0600)
}

var _ *sql.DB // database/sql remains the same application boundary on both sides.
