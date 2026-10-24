package main

import (
	"strings"
	"testing"
	"time"
)

const testBatteryObserver = "7e76aa00112233445566778899aabbccddeeff00112233445566778899009400"

func batteryReading(node string, mv float64, sampled time.Time) map[string]interface{} {
	return map[string]interface{}{
		"node_id":    node,
		"battery_mv": mv,
		"snr":        7.4,
		"rssi":       -88.0,
		"source":     "poll",
		"reply_hops": 0.0,
		"method":     "status",
		"sampled_at": sampled.UTC().Format(time.RFC3339),
	}
}

func batteryMsg(readings ...map[string]interface{}) map[string]interface{} {
	list := make([]interface{}, len(readings))
	for i, r := range readings {
		list[i] = r
	}
	return map[string]interface{}{
		"v":         1.0,
		"origin":    "Test OBS",
		"origin_id": strings.ToUpper(testBatteryObserver),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"readings":  list,
	}
}

func batteryRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM neighbor_battery`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func batteryCfg() *Config {
	return &Config{NeighborBattery: &NeighborBatteryConfig{Enabled: true}}
}

func TestHandleBatteryReportStoresReading(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	handleBatteryReport(s, batteryCfg(), "test", strings.ToUpper(testBatteryObserver), batteryMsg(batteryReading(strings.ToUpper(testTargetPK), 3240, now)))

	var node, obs, sampled, method, source string
	var mv, hops int
	var snr float64
	var rssi int
	err := s.db.QueryRow(`SELECT node_id, observer_id, sampled_at, battery_mv, snr, rssi, method, source, reply_hops FROM neighbor_battery`).
		Scan(&node, &obs, &sampled, &mv, &snr, &rssi, &method, &source, &hops)
	if err != nil {
		t.Fatal(err)
	}
	if node != testTargetPK || obs != testBatteryObserver {
		t.Errorf("keys = %s/%s, want lowercase %s/%s", node, obs, testTargetPK, testBatteryObserver)
	}
	if mv != 3240 || snr != 7.4 || rssi != -88 || method != "status" || source != "poll" || hops != 0 {
		t.Errorf("row = %d mV snr %v rssi %d %s %s hops %d", mv, snr, rssi, method, source, hops)
	}
	if sampled != now.Format(batteryTimeLayout) {
		t.Errorf("sampled_at = %s, want %s", sampled, now.Format(batteryTimeLayout))
	}
}

// Zero millivolts is how an openHop repeater without a power sensor answers.
// It is stored as is: "no battery fitted", not a dropped reading.
func TestHandleBatteryReportKeepsZero(t *testing.T) {
	s := newTestStore(t)
	handleBatteryReport(s, batteryCfg(), "test", testBatteryObserver, batteryMsg(batteryReading(testTargetPK, 0, time.Now())))
	if n := batteryRows(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestParseBatteryReportRejectsMessage(t *testing.T) {
	now := time.Now().UTC()
	good := batteryReading(testTargetPK, 3500, now)
	cases := map[string]struct {
		observer string
		msg      map[string]interface{}
	}{
		"observer not a key": {"abc", batteryMsg(good)},
		"unknown version":    {testBatteryObserver, func() map[string]interface{} { m := batteryMsg(good); m["v"] = 2.0; return m }()},
		"no version":         {testBatteryObserver, func() map[string]interface{} { m := batteryMsg(good); delete(m, "v"); return m }()},
		"origin_id mismatch": {testBatteryObserver, func() map[string]interface{} { m := batteryMsg(good); m["origin_id"] = testTargetPK; return m }()},
		"no readings":        {testBatteryObserver, batteryMsg()},
		"too many readings": {testBatteryObserver, func() map[string]interface{} {
			rs := make([]map[string]interface{}, batteryMaxReadings+1)
			for i := range rs {
				rs[i] = good
			}
			return batteryMsg(rs...)
		}()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseBatteryReport(c.observer, c.msg, now); err == nil {
				t.Fatal("accepted, want rejected")
			}
		})
	}
}

func TestParseBatteryReportSkipsBadReadings(t *testing.T) {
	now := time.Now().UTC()
	other := strings.Repeat("c", 64)
	set := func(k string, v interface{}) map[string]interface{} {
		r := batteryReading(other, 3500, now)
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
		return r
	}
	cases := map[string]map[string]interface{}{
		"short node_id":        set("node_id", "abcd"),
		"self-report":          set("node_id", testBatteryObserver),
		"negative mV":          set("battery_mv", -1.0),
		"above 10000 mV":       set("battery_mv", 10001.0),
		"fractional mV":        set("battery_mv", 3300.5),
		"missing mV":           set("battery_mv", nil),
		"missing sampled_at":   set("sampled_at", nil),
		"sampled_at too old":   set("sampled_at", now.Add(-3*time.Hour).Format(time.RFC3339)),
		"sampled_at in future": set("sampled_at", now.Add(10*time.Minute).Format(time.RFC3339)),
		"unknown source":       set("source", "flood"),
		"negative reply_hops":  set("reply_hops", -1.0),
		"bad method":           set("method", "DROP TABLE"),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			out, skipped, err := parseBatteryReport(testBatteryObserver, batteryMsg(batteryReading(testTargetPK, 3500, now), bad), now)
			if err != nil {
				t.Fatalf("whole message rejected: %v", err)
			}
			if len(out) != 1 || out[0].NodeID != testTargetPK {
				t.Fatalf("kept %d readings, want only the good one", len(out))
			}
			if len(skipped) != 1 {
				t.Fatalf("skipped = %v, want one entry", skipped)
			}
		})
	}
}

func TestParseBatteryReportSkipsDuplicateNode(t *testing.T) {
	now := time.Now().UTC()
	out, skipped, err := parseBatteryReport(testBatteryObserver, batteryMsg(
		batteryReading(testTargetPK, 3500, now), batteryReading(testTargetPK, 3400, now)), now)
	if err != nil || len(out) != 1 || len(skipped) != 1 {
		t.Fatalf("out=%d skipped=%v err=%v, want 1 kept, 1 skipped", len(out), skipped, err)
	}
}

func TestParseBatteryReportDefaults(t *testing.T) {
	now := time.Now().UTC()
	r := map[string]interface{}{"node_id": testTargetPK, "battery_mv": 3700.0, "sampled_at": now.Format(time.RFC3339)}
	out, _, err := parseBatteryReport(testBatteryObserver, batteryMsg(r), now)
	if err != nil || len(out) != 1 {
		t.Fatalf("out=%v err=%v", out, err)
	}
	got := out[0]
	if got.Source != "poll" || got.Method != "status" || got.ReplyHops != 0 || got.SNR != nil || got.RSSI != nil {
		t.Errorf("defaults = %+v", got)
	}
}

func TestHandleBatteryReportMinInterval(t *testing.T) {
	s := newTestStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	send := func(at time.Time) {
		handleBatteryReport(s, batteryCfg(), "test", testBatteryObserver, batteryMsg(batteryReading(testTargetPK, 3500, at)))
	}
	send(base)
	send(base) // same reading again
	send(base.Add(5 * time.Minute))
	if n := batteryRows(t, s); n != 1 {
		t.Fatalf("rows = %d after repeats inside 10 min, want 1", n)
	}
	send(base.Add(11 * time.Minute))
	if n := batteryRows(t, s); n != 2 {
		t.Fatalf("rows = %d, want 2 once the interval has passed", n)
	}
	// Another observer's reading of the same node is its own series.
	other := strings.Repeat("d", 64)
	handleBatteryReport(s, batteryCfg(), "test", other, func() map[string]interface{} {
		m := batteryMsg(batteryReading(testTargetPK, 3480, base))
		m["origin_id"] = other
		return m
	}())
	if n := batteryRows(t, s); n != 3 {
		t.Fatalf("rows = %d, want 3 with a second observer", n)
	}
}

func TestPruneOldNeighborBattery(t *testing.T) {
	s := newTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -40).Format(batteryTimeLayout)
	recent := time.Now().UTC().Add(-time.Hour).Format(batteryTimeLayout)
	for _, at := range []string{old, recent} {
		if _, err := s.InsertNeighborBattery(&NeighborBatteryReading{
			NodeID: testTargetPK, ObserverID: testBatteryObserver, SampledAt: at, BatteryMv: 3500,
			Method: "status", Source: "poll", ReceivedAt: at,
		}, 0); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneOldNeighborBattery(30)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (err %v), want 1", n, err)
	}
	if left := batteryRows(t, s); left != 1 {
		t.Fatalf("rows left = %d, want 1", left)
	}
}

func TestNeighborBatteryConfigDefaults(t *testing.T) {
	var c Config
	if c.NeighborBatteryEnabled() {
		t.Error("enabled by default, want opt-in")
	}
	if got := c.NeighborBatteryMinIntervalMinutes(); got != 10 {
		t.Errorf("min interval = %d, want 10", got)
	}
	if got := c.NeighborBatteryDaysOrZero(); got != 0 {
		t.Errorf("retention = %d, want 0", got)
	}
}

// The /battery topic is stored only when neighborBattery.enabled is set, and
// never falls through to the packet path.
func TestBatteryTopicGate(t *testing.T) {
	topic := "meshcore/EDI/" + strings.ToUpper(testBatteryObserver) + "/battery"
	payload := `{"v":1,"origin_id":"` + testBatteryObserver + `","readings":[{"node_id":"` + testTargetPK +
		`","battery_mv":3600,"sampled_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`

	off := newTestStore(t)
	handleMessage(off, "test", MQTTSource{Name: "test"}, newMockMsg(topic, payload), nil, nil, &Config{})
	if n := batteryRows(t, off); n != 0 {
		t.Fatalf("disabled: rows = %d, want 0", n)
	}

	on := newTestStore(t)
	handleMessage(on, "test", MQTTSource{Name: "test"}, newMockMsg(topic, payload), nil, nil, batteryCfg())
	if n := batteryRows(t, on); n != 1 {
		t.Fatalf("enabled: rows = %d, want 1", n)
	}
	var obs int
	if err := on.db.QueryRow(`SELECT COUNT(*) FROM observers`).Scan(&obs); err != nil {
		t.Fatal(err)
	}
	if obs != 0 {
		t.Errorf("observers = %d, want 0: a battery report is not an observer heartbeat", obs)
	}
}
