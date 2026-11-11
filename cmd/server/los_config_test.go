package main

import (
	"bytes"
	"log"
	"os"
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
		cc.KFactor != defaultLOSKFactor || cc.Samples != losSamples ||
		cc.MaxPoints != losMaxPoints || cc.MaxLegKm != losMaxLegKm {
		t.Fatalf("unexpected defaults: %+v", *cc)
	}
	r, warns := (&LOSConfig{Enabled: true}).resolved()
	if r.ElevationURL != defaultLOSElevationURL || len(warns) != 0 {
		t.Fatalf("default url/warns: %q %v", r.ElevationURL, warns)
	}
}

func TestLOSConfigOverridesAndValidation(t *testing.T) {
	r, warns := (&LOSConfig{Enabled: true, ElevationURL: "http://dem.local:8080/v1/elevation",
		FrequencyMHz: 869.618, AntennaHeightM: 12, KFactor: 1}).resolved()
	if r.ElevationURL != "http://dem.local:8080/v1/elevation" || r.FrequencyMHz != 869.618 ||
		r.AntennaHeightM != 12 || r.KFactor != 1 || len(warns) != 0 {
		t.Fatalf("valid overrides not kept: %+v %v", r, warns)
	}
	r, warns = (&LOSConfig{Enabled: true, ElevationURL: "file:///etc/passwd",
		FrequencyMHz: 50, AntennaHeightM: -3, KFactor: 99}).resolved()
	if r.ElevationURL != defaultLOSElevationURL || r.FrequencyMHz != defaultLOSFrequencyMHz ||
		r.AntennaHeightM != defaultLOSAntennaM || r.KFactor != defaultLOSKFactor {
		t.Fatalf("invalid values must fall back to defaults: %+v", r)
	}
	if len(warns) != 4 {
		t.Fatalf("want 4 warnings, got %v", warns)
	}
}

func TestLOSConfigLegBudgets(t *testing.T) {
	r, warns := (&LOSConfig{Enabled: true}).resolved()
	if r.LegsPerMinute != defaultLOSLegsPerMinute || r.LegsPerHour != defaultLOSLegsPerHour ||
		r.LegsPerDay != defaultLOSLegsPerDay || len(warns) != 0 {
		t.Fatalf("defaults: %+v %v", r, warns)
	}
	r, warns = (&LOSConfig{Enabled: true, LegsPerMinute: 5, LegsPerHour: 50, LegsPerDay: 1000000}).resolved()
	if r.LegsPerMinute != 5 || r.LegsPerHour != 50 || r.LegsPerDay != 1000000 || len(warns) != 0 {
		t.Fatalf("valid: %+v %v", r, warns)
	}
	r, warns = (&LOSConfig{Enabled: true, LegsPerMinute: -1, LegsPerHour: 1000001, LegsPerDay: 7}).resolved()
	if r.LegsPerMinute != defaultLOSLegsPerMinute || r.LegsPerHour != defaultLOSLegsPerHour || r.LegsPerDay != 7 || len(warns) != 2 {
		t.Fatalf("invalid: %+v %v", r, warns)
	}
}

func TestLOSConfigLogRedactsURLQuery(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	logLOSConfig(&LOSConfig{Enabled: true, ElevationURL: "https://user:pw@dem.example/v1/elevation?apikey=SECRET"})
	out := buf.String()
	if strings.Contains(out, "SECRET") || strings.Contains(out, "pw@") || strings.Contains(out, "apikey") {
		t.Fatalf("log leaks the URL query: %s", out)
	}
	if !strings.Contains(out, "https://dem.example/v1/elevation") {
		t.Fatalf("log should name scheme://host/path: %s", out)
	}
}
