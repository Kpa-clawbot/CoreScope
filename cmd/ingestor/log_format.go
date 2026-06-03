package main

import "fmt"

// formatStatusLog formats the "status: name (iata)" log line emitted on
// MQTT status messages. name + iata are MQTT-controlled and routed
// through sanitizeLogString so CR/LF/control bytes cannot inject forged
// log lines.
//
// See audit-input-vulns-20260603 follow-up to #1540 — call site
// cmd/ingestor/main.go:531.
func formatStatusLog(tag, name, iata string) string {
	return fmt.Sprintf("MQTT [%s] status: %s (%s)", tag, sanitizeLogString(name), sanitizeLogString(iata))
}

// formatChannelMessageLog formats the "channel message: chN from S" log line
// emitted on MQTT channel messages. channelIdx + sender are MQTT-controlled.
//
// Call site cmd/ingestor/main.go:854.
func formatChannelMessageLog(tag, channelIdx, sender string) string {
	return fmt.Sprintf("MQTT [%s] channel message: ch%s from %s", tag, sanitizeLogString(channelIdx), sanitizeLogString(sender))
}

// formatDirectMessageLog formats the "direct message from S" log line
// emitted on MQTT DM messages. sender is MQTT-controlled.
//
// Call site cmd/ingestor/main.go:940.
func formatDirectMessageLog(tag, sender string) string {
	return fmt.Sprintf("MQTT [%s] direct message from %s", tag, sanitizeLogString(sender))
}
