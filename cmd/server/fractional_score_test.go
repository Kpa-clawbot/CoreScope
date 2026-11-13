package main

import (
	"fmt"
	"testing"
	"time"
)

// The ingestor stores observations.score as a float (the MQTT value is parsed
// as float64), so the server must read it as one. Scanning a fractional score
// into an integer made the row's Scan fail and the loaders skipped it, so the
// observation vanished. Integral scores must still serialise as plain numbers.

func seedFractionalScores(t *testing.T, d *dualDB) {
	t.Helper()
	d.exec(t, `INSERT INTO observers (idx, id, name, iata, last_seen, first_seen) VALUES (?, ?, ?, ?, ?, ?)`,
		1, pgsObsKey1, "Obs-One", "SJC", pgsISO(0), pgsISO(-time.Hour))
	d.exec(t, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
		VALUES (1, '1101AABBCCDD', 'hashfrac01', ?, 1, 5, 1, '{"type":"GRP_TXT"}', ?)`, pgsISO(0), pgsEpoch(time.Minute))
	for i, score := range []interface{}{0.75, 3.0, nil} {
		d.exec(t, `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
			VALUES (?, 1, 1, 'rx', 1.5, -90.0, ?, ?, ?)`, i+1, score, fmt.Sprintf(`["A%d"]`, i), pgsEpoch(time.Duration(i+1)*time.Minute))
	}
}

func fractionalScores(t *testing.T, name string, got pgsStoreSummary) {
	t.Helper()
	if len(got.Packets) != 1 || len(got.Packets[0].Obs) != 3 {
		t.Fatalf("%s: want 1 tx with 3 observations, got %+v", name, got.Packets)
	}
	byID := map[int]*float64{}
	for _, o := range got.Packets[0].Obs {
		byID[o.ID] = o.Score
	}
	if byID[1] == nil || *byID[1] != 0.75 {
		t.Errorf("%s: obs 1 score = %v, want 0.75", name, byID[1])
	}
	if byID[2] == nil || *byID[2] != 3 {
		t.Errorf("%s: obs 2 score = %v, want 3", name, byID[2])
	}
	if s, ok := byID[3]; !ok || s != nil {
		t.Errorf("%s: obs 3 score = %v, want nil", name, s)
	}
}

func TestFractionalScoreSurvivesLoaders(t *testing.T) {
	d := newDualTestDB(t)
	seedFractionalScores(t, d)

	for name, load := range map[string]func(*PacketStore) error{
		"Load":        func(s *PacketStore) error { return s.Load() },
		"LoadChunked": func(s *PacketStore) error { return s.LoadChunked(1) },
		"loadChunk": func(s *PacketStore) error {
			return s.loadChunk(pgsBase.Add(-time.Hour), pgsBase.Add(10*time.Hour))
		},
		"IngestNewFromDB": func(s *PacketStore) error { s.IngestNewFromDB(0, 100); return nil },
	} {
		load := load
		got := requireSame(t, d, name, func(db *DB) (pgsStoreSummary, error) {
			s := NewPacketStore(db, &PacketStoreConfig{})
			err := load(s)
			return summarizeStore(s), err
		})
		fractionalScores(t, name, got)
	}
}

func TestFractionalScoreIngestNewObservations(t *testing.T) {
	d := newDualTestDB(t)
	seedFractionalScores(t, d)

	// Load first, then add the observation, so the incremental path is the
	// one that reads it.
	stores := map[string]*PacketStore{}
	for name, db := range map[string]*DB{"sqlite": d.sqlite, "postgres": d.pg} {
		s := NewPacketStore(db, &PacketStoreConfig{})
		if err := s.Load(); err != nil {
			t.Fatalf("%s Load: %v", name, err)
		}
		stores[name] = s
	}
	d.exec(t, `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
		VALUES (4, 1, 1, 'rx', 1.5, -90.0, 0.5, '["A9"]', ?)`, pgsEpoch(5*time.Minute))

	for name, s := range stores {
		s.IngestNewObservations(3, 100)
		var score *float64
		found := false
		for _, o := range summarizeStore(s).Packets[0].Obs {
			if o.ID == 4 {
				found, score = true, o.Score
			}
		}
		if !found {
			t.Errorf("%s: obs 4 not ingested", name)
		} else if score == nil || *score != 0.5 {
			t.Errorf("%s: obs 4 score = %v, want 0.5", name, score)
		}
	}
}
