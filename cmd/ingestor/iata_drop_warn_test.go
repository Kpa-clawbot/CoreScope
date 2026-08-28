package main

import (
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
