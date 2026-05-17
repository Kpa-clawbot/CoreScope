package main

import (
	"encoding/json"
	"testing"
)

// Regression test for #1044: observer metadata (model, firmware, battery_mv,
// noise_floor) is silently dropped when an MQTT status payload arrives, even
// though the same payload's `radio` and `client_version` fields ARE persisted.
//
// Real-world payload captured from the production MQTT bridge:
//
//	{"status":"online","origin":"TestObserver","origin_id":"AABBCCDD",
//	 "radio":"910.5250244,62.5,7,5",
//	 "model":"Heltec V3",
//	 "firmware_version":"1.12.0-test",
//	 "client_version":"meshcoretomqtt/1.0.8.0",
//	 "stats":{"battery_mv":4209,"uptime_secs":75821,"noise_floor":-109,
//	          "tx_air_secs":80,"rx_air_secs":1903,"recv_errors":934}}
func TestStatusMessageMetadataPersisted_Issue1044(t *testing.T) {
	const payload = `{"status":"online","origin":"TestObserver","origin_id":"AABBCCDD","radio":"910.5250244,62.5,7,5","model":"Heltec V3","firmware_version":"1.12.0-test","client_version":"meshcoretomqtt/1.0.8.0","stats":{"battery_mv":4209,"uptime_secs":75821,"noise_floor":-109,"tx_air_secs":80,"rx_air_secs":1903,"recv_errors":934}}`

	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	meta := extractObserverMeta(msg)
	if meta == nil {
		t.Fatal("extractObserverMeta returned nil for a payload that contains model/firmware/battery_mv")
	}
	if meta.Model == nil || *meta.Model != "Heltec V3" {
		t.Errorf("meta.Model = %v, want \"Heltec V3\"", meta.Model)
	}
	if meta.Firmware == nil || *meta.Firmware != "1.12.0-test" {
		t.Errorf("meta.Firmware = %v, want \"1.12.0-test\"", meta.Firmware)
	}
	if meta.ClientVersion == nil || *meta.ClientVersion != "meshcoretomqtt/1.0.8.0" {
		t.Errorf("meta.ClientVersion = %v, want \"meshcoretomqtt/1.0.8.0\"", meta.ClientVersion)
	}
	if meta.Radio == nil || *meta.Radio != "910.5250244,62.5,7,5" {
		t.Errorf("meta.Radio = %v, want radio string", meta.Radio)
	}
	if meta.BatteryMv == nil || *meta.BatteryMv != 4209 {
		t.Errorf("meta.BatteryMv = %v, want 4209", meta.BatteryMv)
	}
	if meta.NoiseFloor == nil || *meta.NoiseFloor != -109 {
		t.Errorf("meta.NoiseFloor = %v, want -109", meta.NoiseFloor)
	}
	if meta.UptimeSecs == nil || *meta.UptimeSecs != 75821 {
		t.Errorf("meta.UptimeSecs = %v, want 75821", meta.UptimeSecs)
	}

	// Now drive the meta through UpsertObserver and verify the row.
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("AABBCCDD", "TestObserver", "SJC", meta); err != nil {
		t.Fatalf("UpsertObserver: %v", err)
	}

	var (
		gotModel, gotFirmware, gotClientVersion, gotRadio string
		gotBattery                                        int
		gotUptime                                         int64
		gotNoise                                          float64
	)
	err = s.db.QueryRow(`SELECT model, firmware, client_version, radio,
	                            battery_mv, uptime_secs, noise_floor
	                     FROM observers WHERE id = 'AABBCCDD'`).Scan(
		&gotModel, &gotFirmware, &gotClientVersion, &gotRadio,
		&gotBattery, &gotUptime, &gotNoise,
	)
	if err != nil {
		t.Fatalf("scan observer row: %v", err)
	}
	if gotModel != "Heltec V3" {
		t.Errorf("DB model = %q, want \"Heltec V3\"", gotModel)
	}
	if gotFirmware != "1.12.0-test" {
		t.Errorf("DB firmware = %q, want \"1.12.0-test\"", gotFirmware)
	}
	if gotBattery != 4209 {
		t.Errorf("DB battery_mv = %d, want 4209", gotBattery)
	}
	if gotUptime != 75821 {
		t.Errorf("DB uptime_secs = %d, want 75821", gotUptime)
	}
	if gotNoise != -109 {
		t.Errorf("DB noise_floor = %f, want -109", gotNoise)
	}
}
