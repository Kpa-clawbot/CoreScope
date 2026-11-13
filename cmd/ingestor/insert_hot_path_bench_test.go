package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkInsertTransmissionHotPath measures the per-packet write path the
// MQTT handler runs (main.go): InsertTransmission, UpsertNode for an advert,
// and UpsertObserverAt for the observer that heard it. That covers the node
// and observer upserts plus the observer last_seen UPDATE, the statements the
// portable last_seen clamp (clampedMaxSQL) rewrote.
//
// Mix per b.N iteration: 2000 InsertTransmission calls over 3 observers and 50
// nodes. Every 4th packet is an advert from one of the nodes; each packet is
// heard by 1-3 observers, so later observations take the duplicate-hash path.
func BenchmarkInsertTransmissionHotPath(b *testing.B) {
	// OpenStore logs its config; keep the benchmark lines readable.
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	s, err := OpenStore(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	benchInsertTransmissionHotPath(b, s)
}

// BenchmarkInsertTransmissionHotPathPostgres is the same mix against
// Postgres (informational: it includes a network round trip per statement).
// Skips without $CORESCOPE_TEST_PG_DSN.
func BenchmarkInsertTransmissionHotPathPostgres(b *testing.B) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	benchInsertTransmissionHotPath(b, openPostgresTestStore(b, filepath.Join(b.TempDir(), "bench.db")))
}

func benchInsertTransmissionHotPath(b *testing.B, s *Store) {
	const (
		callsPerOp = 2000
		observers  = 3
		nodes      = 50
	)
	obsIDs := make([]string, observers)
	for i := range obsIDs {
		obsIDs[i] = fmt.Sprintf("%064x", 0xb0+i)
		if err := s.UpsertObserverAt(obsIDs[i], fmt.Sprintf("obs-%d", i), "SEA", nil, ""); err != nil {
			b.Fatal(err)
		}
	}
	nodeKeys := make([]string, nodes)
	for i := range nodeKeys {
		nodeKeys[i] = fmt.Sprintf("%064x", 0x1000+i)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	pkt := 0
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for calls := 0; calls < callsPerOp; pkt++ {
			isAdvert := pkt%4 == 0
			node := nodeKeys[pkt%nodes]
			ts := base.Add(time.Duration(pkt) * time.Second).Format(time.RFC3339)
			data := &PacketData{
				Hash:        fmt.Sprintf("hash-%d", pkt),
				RawHex:      "1100aa",
				RouteType:   1,
				PayloadType: 2,
				PathJSON:    `["ab","cd"]`,
				DecodedJSON: `{"type":"TXT_MSG"}`,
				Timestamp:   ts,
			}
			if isAdvert {
				data.PayloadType = 4
				data.DecodedJSON = `{"type":"ADVERT"}`
				data.FromPubkey = node
			}
			heardBy := 1 + pkt%observers
			for o := 0; o < heardBy && calls < callsPerOp; o++ {
				d := *data
				d.ObserverID = obsIDs[(pkt+o)%observers]
				if _, err := s.InsertTransmission(&d); err != nil {
					b.Fatal(err)
				}
				if isAdvert {
					if err := s.UpsertNode(node, "node", "repeater", nil, nil, ts); err != nil {
						b.Fatal(err)
					}
				}
				if err := s.UpsertObserverAt(d.ObserverID, "obs", "SEA", nil, ""); err != nil {
					b.Fatal(err)
				}
				calls++
			}
		}
	}
}
