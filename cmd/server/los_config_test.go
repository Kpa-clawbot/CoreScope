package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
)

func TestLOSConfigDisabledByDefault(t *testing.T) {
	var c *LOSConfig
	if c.enabled() {
		t.Fatal("nil los config must be disabled")
	}
	if c.clientConfig() != nil {
		t.Fatal("disabled los must not appear in /api/config/client")
	}
	if (&LOSConfig{}).clientConfig() != nil {
		t.Fatal("enabled=false must not appear in /api/config/client")
	}
}

func TestLOSConfigDefaults(t *testing.T) {
	cc := (&LOSConfig{Enabled: true}).clientConfig()
	if cc == nil {
		t.Fatal("enabled los must appear in /api/config/client")
	}
	if cc.FrequencyMHz != defaultLOSFrequencyMHz || cc.AntennaHeightM != defaultLOSAntennaM ||
		cc.KFactor != defaultLOSKFactor || cc.MaxPoints != losMaxPoints || cc.MaxLegKm != losMaxLegKm {
		t.Fatalf("unexpected defaults: %+v", *cc)
	}
	// The sample count varies per leg now; the client config no longer names one.
	b, _ := json.Marshal(cc)
	if strings.Contains(string(b), "samples") {
		t.Fatalf("client config %s still carries samples", b)
	}
	if _, warns := (&LOSConfig{Enabled: true}).resolved(); len(warns) != 0 {
		t.Fatalf("default warns: %v", warns)
	}
}

func TestLOSConfigOverridesAndValidation(t *testing.T) {
	r, warns := (&LOSConfig{Enabled: true, FrequencyMHz: 869.618, AntennaHeightM: 12, KFactor: 1}).resolved()
	if r.FrequencyMHz != 869.618 || r.AntennaHeightM != 12 || r.KFactor != 1 || len(warns) != 0 {
		t.Fatalf("valid overrides not kept: %+v %v", r, warns)
	}
	r, warns = (&LOSConfig{Enabled: true, FrequencyMHz: 50, AntennaHeightM: -3, KFactor: 99}).resolved()
	if r.FrequencyMHz != defaultLOSFrequencyMHz || r.AntennaHeightM != defaultLOSAntennaM || r.KFactor != defaultLOSKFactor {
		t.Fatalf("invalid values must fall back to defaults: %+v", r)
	}
	if len(warns) != 3 {
		t.Fatalf("want 3 warnings, got %v", warns)
	}
}

func TestLOSConfigLog(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	logLOSConfig(&LOSConfig{Enabled: true, FrequencyMHz: 50})
	out := buf.String()
	for _, want := range []string{
		"los.frequencyMhz must be within 100-6000; using the default",
		"[los] enabled (910.525 MHz, antenna=5.0 m, k=1.333, up to 16 terrain tiles a request, 20 tile downloads/min per client)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	logLOSConfig(&LOSConfig{})
	if buf.Len() != 0 {
		t.Fatalf("disabled los logged %q", buf.String())
	}
}
