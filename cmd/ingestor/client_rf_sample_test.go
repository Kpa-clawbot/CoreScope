package main

import "testing"

func TestHandleClientRfSample(t *testing.T) {
	s := newTestStore(t)
	msg := map[string]interface{}{
		"type": "RF_SAMPLE", "timestamp": "2026-08-17T10:00:00.000Z",
		"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4, "acc_m": 8.0},
		"stationary":  false,
		"uptime_secs": 84213.0, "noise_floor": -119.0, "rx_air_secs": 20877.0,
	}
	handleClientRfSample(s, "test", "aa11", msg)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestHandleClientRfSampleRejects(t *testing.T) {
	s := newTestStore(t)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"timestamp":   "2026-08-17T10:00:00.000Z",
			"gps":         map[string]interface{}{"lat": 51.2, "lon": 4.4},
			"uptime_secs": 1.0,
		}
	}

	handleClientRfSample(s, "test", "NOT-HEX!", base()) // bad topic pubkey

	noGPS := base()
	delete(noGPS, "gps")
	handleClientRfSample(s, "test", "aa11", noGPS)

	badGPS := base()
	badGPS["gps"] = map[string]interface{}{"lat": 999.0, "lon": 4.4}
	handleClientRfSample(s, "test", "aa11", badGPS)

	noUptime := base()
	delete(noUptime, "uptime_secs")
	handleClientRfSample(s, "test", "aa11", noUptime)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n)
	if n != 0 {
		t.Errorf("rows = %d, want 0 — all four must be rejected", n)
	}
}

func TestRfDeltaBreaksOnReboot(t *testing.T) {
	s := newTestStore(t)
	insert := func(at string, uptime, rxAir int64) {
		u, r := uptime, rxAir
		if _, err := s.InsertClientRfSample(&ClientRfSample{
			RxPubkey: "aa11", SampledAt: at, IngestedAt: at,
			Lat: 51.2, Lon: 4.4, UptimeSecs: u, RxAirSecs: &r,
		}); err != nil {
			t.Fatalf("seed %s: %v", at, err)
		}
	}
	insert("2026-08-17T10:00:00Z", 1000, 500)
	insert("2026-08-17T10:00:15Z", 1015, 512) // +12 s of RX air over 15 s
	insert("2026-08-17T10:00:30Z", 10, 3)     // rebooted: uptime dropped

	deltas, err := s.ClientRfDeltas("aa11", "2026-08-17T00:00:00Z", "2026-08-18T00:00:00Z")
	if err != nil {
		t.Fatalf("deltas: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %d, want 1 (the reboot pair must be skipped)", len(deltas))
	}
	if deltas[0].RxAirDelta != 12 || deltas[0].WallSecs != 15 {
		t.Errorf("delta = %d over %ds, want 12 over 15s", deltas[0].RxAirDelta, deltas[0].WallSecs)
	}
}
