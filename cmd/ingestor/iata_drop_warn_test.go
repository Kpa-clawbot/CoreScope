package main

import (
	"fmt"
	"testing"
	"time"
)

func TestIngestorIATAWhitelistGate(t *testing.T) {
	// Empty whitelist: the facility stays inert, everything passes.
	open := &Config{}
	for _, code := range []string{"SJC", "PHL", "", "zzz"} {
		if !open.IsObserverIATAAllowed(code) {
			t.Errorf("empty whitelist should allow %q", code)
		}
	}

	cfg := &Config{ObserverIATAWhitelist: []string{"SJC", "oak", " MRY "}}
	tests := []struct {
		iata string
		want bool
	}{
		{"SJC", true},
		{"sjc", true},
		{"OAK", true},
		{"MRY", true},
		{" mry ", true},
		{"PHL", false},
		{"MCO", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := cfg.IsObserverIATAAllowed(tt.iata); got != tt.want {
			t.Errorf("IsObserverIATAAllowed(%q) = %v, want %v", tt.iata, got, tt.want)
		}
	}
}

func TestIngestorIATAWarnInterval(t *testing.T) {
	if got := (&Config{}).IATAWarnInterval(); got != 6*time.Hour {
		t.Errorf("default interval = %v, want 6h", got)
	}
	if got := (&Config{IATAWarnIntervalSec: 90}).IATAWarnInterval(); got != 90*time.Second {
		t.Errorf("configured interval = %v, want 90s", got)
	}
	// Negative/zero fall back to the default rather than logging every message.
	if got := (&Config{IATAWarnIntervalSec: -5}).IATAWarnInterval(); got != 6*time.Hour {
		t.Errorf("negative interval = %v, want 6h", got)
	}
}

func TestIngestorShouldWarnIATADropThrottles(t *testing.T) {
	cfg := &Config{ObserverIATAWhitelist: []string{"SJC"}, IATAWarnIntervalSec: 3600}

	if !cfg.ShouldWarnIATADrop("PHL") {
		t.Fatal("first drop for a region should warn")
	}
	if cfg.ShouldWarnIATADrop("PHL") {
		t.Error("second drop inside the interval should be suppressed")
	}
	if cfg.ShouldWarnIATADrop("phl") {
		t.Error("case variant should hit the same throttle bucket")
	}
	// A different region is tracked independently.
	if !cfg.ShouldWarnIATADrop("MCO") {
		t.Error("a distinct region should warn on its first drop")
	}

	// Once the interval elapses the region re-logs, so an ongoing drop stays
	// visible to a scraper instead of decaying into silence.
	cfg.iataWarnMu.Lock()
	cfg.iataWarnLast["PHL"] = time.Now().Add(-2 * time.Hour)
	cfg.iataWarnMu.Unlock()
	if !cfg.ShouldWarnIATADrop("PHL") {
		t.Error("region should re-log after the interval elapses")
	}
}

func TestIngestorShouldWarnIATADropEdges(t *testing.T) {
	var nilCfg *Config
	if nilCfg.ShouldWarnIATADrop("PHL") {
		t.Error("nil config should not warn")
	}
	cfg := &Config{}
	if cfg.ShouldWarnIATADrop("   ") {
		t.Error("blank region should not warn")
	}
}

// TestShouldWarnIATADropBoundsItsMap pins the fix for the review blocker on
// #2008: the throttle key is a topic segment the publisher controls, so an
// unbounded map is a remote memory sink. Measured on the original branch,
// 200,000 distinct codes retained 200,000 entries and 15.1 MB of heap.
func TestShouldWarnIATADropBoundsItsMap(t *testing.T) {
	c := &Config{}

	// Far more distinct codes than the cap, as a hostile or broken feed would.
	for i := 0; i < iataWarnMaxTracked*4; i++ {
		c.ShouldWarnIATADrop(fmt.Sprintf("X%05d", i))
	}

	c.iataWarnMu.Lock()
	tracked := len(c.iataWarnLast)
	c.iataWarnMu.Unlock()

	if tracked > iataWarnMaxTracked {
		t.Errorf("tracked %d codes, cap is %d — the map grows with publisher-supplied input",
			tracked, iataWarnMaxTracked)
	}
}

// TestShouldWarnIATADropKeepsWarningPastTheCap is the other half: bounding the
// map must not silence the warning, which would reintroduce the silent-drop bug
// this feature exists to fix, one level up. Past the cap the warning is
// throttled on a shared timestamp rather than dropped.
func TestShouldWarnIATADropKeepsWarningPastTheCap(t *testing.T) {
	c := &Config{}
	for i := 0; i < iataWarnMaxTracked; i++ {
		c.ShouldWarnIATADrop(fmt.Sprintf("F%05d", i))
	}

	// A brand new code, with the map already full.
	if !c.ShouldWarnIATADrop("ZZZ") {
		t.Fatal("a drop past the cap must still warn once, not be swallowed")
	}
	// ...and the next one is throttled rather than flooding.
	if c.ShouldWarnIATADrop("YYY") {
		t.Error("a second overflow drop inside the interval must be throttled")
	}

	// After the interval elapses, it speaks again. Rewind the shared timestamp
	// rather than sleeping.
	c.iataWarnMu.Lock()
	c.iataWarnOverflowLast = time.Now().Add(-2 * c.IATAWarnInterval())
	c.iataWarnMu.Unlock()
	if !c.ShouldWarnIATADrop("WWW") {
		t.Error("once the interval has passed the overflow warning must return")
	}
}

// TestShouldWarnIATADropStillThrottlesKnownCodes guards the normal path: the
// cap must not change behaviour for a code already being tracked.
func TestShouldWarnIATADropStillThrottlesKnownCodes(t *testing.T) {
	c := &Config{}
	if !c.ShouldWarnIATADrop("BRU") {
		t.Fatal("first sighting must warn")
	}
	if c.ShouldWarnIATADrop("BRU") {
		t.Error("second sighting inside the interval must be throttled")
	}
}

// TestShouldWarnIATADropReclaimsExpiredSlots covers the flaw the cap had on its
// own: the map never shrank, so the first iataWarnMaxTracked codes ever seen
// owned it forever and a legitimate region arriving later was permanently
// demoted to the shared overflow throttle behind whatever transient junk filled
// it first.
//
// Sweeping expired entries at the cap is semantically free — an entry past the
// interval would be re-logged on its next sighting regardless — so the map ends
// up holding only regions that are actively arriving.
func TestShouldWarnIATADropReclaimsExpiredSlots(t *testing.T) {
	c := &Config{}

	// Fill the map with junk, then age all of it past the interval.
	for i := 0; i < iataWarnMaxTracked; i++ {
		c.ShouldWarnIATADrop(fmt.Sprintf("J%05d", i))
	}
	stale := time.Now().Add(-2 * c.IATAWarnInterval())
	c.iataWarnMu.Lock()
	for k := range c.iataWarnLast {
		c.iataWarnLast[k] = stale
	}
	c.iataWarnMu.Unlock()

	// A legitimate region shows up now. It must get its own slot, not the
	// shared overflow throttle.
	if !c.ShouldWarnIATADrop("BRU") {
		t.Fatal("a new region must warn once")
	}
	c.iataWarnMu.Lock()
	_, tracked := c.iataWarnLast["BRU"]
	size := len(c.iataWarnLast)
	c.iataWarnMu.Unlock()

	if !tracked {
		t.Error("BRU must occupy its own slot once the stale entries were reclaimed")
	}
	if size > iataWarnMaxTracked {
		t.Errorf("map grew to %d past the cap of %d", size, iataWarnMaxTracked)
	}
	// And its own throttle applies, rather than the shared one.
	if c.ShouldWarnIATADrop("BRU") {
		t.Error("a tracked region must be throttled on its own timestamp")
	}
}
