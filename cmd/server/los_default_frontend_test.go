package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// public/map-ducting.js treats los.frequencyMhz equal to the shipped default
// as "the operator did not choose a frequency". It keeps its own copy of
// that default, which must follow defaultLOSFrequencyMHz.
func TestMapDuctingLOSDefaultMatchesServer(t *testing.T) {
	src, err := os.ReadFile("../../public/map-ducting.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*var LOS_DEFAULT_MHZ = ([0-9.]+);`).FindAllSubmatch(src, -1)
	if len(m) != 1 {
		t.Fatalf("found %d `var LOS_DEFAULT_MHZ = <number>;` lines in public/map-ducting.js, want 1", len(m))
	}
	got, err := strconv.ParseFloat(string(m[0][1]), 64)
	if err != nil {
		t.Fatalf("LOS_DEFAULT_MHZ %q: %v", m[0][1], err)
	}
	if got != defaultLOSFrequencyMHz {
		t.Errorf("LOS_DEFAULT_MHZ in public/map-ducting.js is %v, defaultLOSFrequencyMHz in cmd/server/los_config.go is %v: update both together",
			got, defaultLOSFrequencyMHz)
	}
}
