package main

import (
	"strings"
	"testing"
)

// TestFormatStatusLog_SanitizesMQTTFields pins the status log line at
// cmd/ingestor/main.go:531 — MQTT-derived name + iata must not be able to
// inject CR/LF/control bytes into the log stream.
func TestFormatStatusLog_SanitizesMQTTFields(t *testing.T) {
	got := formatStatusLog("ds1", "evil\r\n[FAKE LOG LINE]", "X\nY")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("formatStatusLog leaked CR/LF: %q", got)
	}
	if strings.Contains(got, "[FAKE LOG LINE]") && !strings.Contains(got, "?[FAKE LOG LINE]") {
		t.Fatalf("formatStatusLog passed injection payload through unmodified: %q", got)
	}
}

// TestFormatChannelMessageLog_SanitizesMQTTFields pins
// cmd/ingestor/main.go:854 — channelIdx + sender are MQTT-controlled.
func TestFormatChannelMessageLog_SanitizesMQTTFields(t *testing.T) {
	got := formatChannelMessageLog("ds1", "0\r\n[FAKE]", "evil\nguy")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("formatChannelMessageLog leaked CR/LF: %q", got)
	}
}

// TestFormatDirectMessageLog_SanitizesMQTTFields pins
// cmd/ingestor/main.go:940 — sender is MQTT-controlled.
func TestFormatDirectMessageLog_SanitizesMQTTFields(t *testing.T) {
	got := formatDirectMessageLog("ds1", "evil\r\n[FAKE LOG LINE] something")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("formatDirectMessageLog leaked CR/LF: %q", got)
	}
	if !strings.Contains(got, "??[FAKE LOG LINE]") {
		t.Fatalf("formatDirectMessageLog did not sanitize injection payload: %q", got)
	}
}

// Sanity: legitimate input passes through untouched apart from tag framing.
func TestFormatLogs_LegitInputUnchanged(t *testing.T) {
	if got := formatStatusLog("ds1", "alpha-node", "BG"); got != "MQTT [ds1] status: alpha-node (BG)" {
		t.Fatalf("unexpected status line: %q", got)
	}
	if got := formatChannelMessageLog("ds1", "3", "bob"); got != "MQTT [ds1] channel message: ch3 from bob" {
		t.Fatalf("unexpected channel line: %q", got)
	}
	if got := formatDirectMessageLog("ds1", "bob"); got != "MQTT [ds1] direct message from bob" {
		t.Fatalf("unexpected DM line: %q", got)
	}
}
