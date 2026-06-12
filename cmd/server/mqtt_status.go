package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
)

// mqttPasswordRe matches `scheme://user:password@host` for broker URL
// schemes used by paho/MQTT (mqtt://, mqtts://, tcp://, ssl://, ws://,
// wss://) and captures everything before the password so the password
// itself can be substituted with a redaction marker.
//
// Intentionally narrow: only matches the inline user:pass@ form. A bare
// password embedded elsewhere (e.g. as a JSON value) is the ingestor's
// responsibility to keep out of the broker URL; this regex is a
// defense-in-depth at the serving boundary, not a sweep of all source
// fields.
var mqttPasswordRe = regexp.MustCompile(`(?i)((?:mqtt|mqtts|tcp|ssl|ws|wss)://[^:/@\s]+):[^@\s]+@`)

// maskBrokerURL returns the broker URL with any inline password redacted.
// `mqtt://user:secret@host:1883` -> `mqtt://user:****@host:1883`.
// URLs without inline credentials are returned unchanged.
func maskBrokerURL(s string) string {
	if s == "" {
		return s
	}
	return mqttPasswordRe.ReplaceAllString(s, `$1:****@`)
}

// MqttSourceStatus is the per-MQTT-source status row surfaced via
// /api/mqtt/status. Mirrors the on-disk shape the ingestor publishes
// (cmd/ingestor SourceStatusSnapshot) but with the broker URL credentials
// redacted before serving — operators must not see the broker password
// in the API response (#1043 acceptance criterion).
type MqttSourceStatus struct {
	Name               string `json:"name"`
	Broker             string `json:"broker"`
	Connected          bool   `json:"connected"`
	LastConnectUnix    int64  `json:"lastConnectUnix"`
	LastDisconnectUnix int64  `json:"lastDisconnectUnix"`
	LastPacketUnix     int64  `json:"lastPacketUnix"`
	ConnectCount       int64  `json:"connectCount"`
	DisconnectCount    int64  `json:"disconnectCount"`
	PacketsTotal       int64  `json:"packetsTotal"`
	PacketsLast5m      int64  `json:"packetsLast5m"`
	LastError          string `json:"lastError,omitempty"`
}

// MqttStatusResponse is the JSON envelope returned by /api/mqtt/status.
type MqttStatusResponse struct {
	Sources  []MqttSourceStatus `json:"sources"`
	SampleAt string             `json:"sampleAt"`
}

// ingestorMqttStatusEnvelope is the partial shape the server decodes from
// the ingestor stats file (additive — older ingestors omit the field).
type ingestorMqttStatusEnvelope struct {
	SampledAt      string             `json:"sampledAt"`
	SourceStatuses []MqttSourceStatus `json:"source_statuses"`
}

// handleMqttStatus serves GET /api/mqtt/status. Reads the ingestor stats
// file, masks broker-URL passwords, and returns the per-source status
// list. Returns an empty list (200 OK) when the stats file is missing
// or unparseable — the UI panel renders a "no data yet" state.
func (s *Server) handleMqttStatus(w http.ResponseWriter, r *http.Request) {
	resp := MqttStatusResponse{Sources: []MqttSourceStatus{}, SampleAt: ""}
	data, err := os.ReadFile(IngestorStatsPath())
	if err != nil {
		writeJSON(w, resp)
		return
	}
	var env ingestorMqttStatusEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		writeJSON(w, resp)
		return
	}
	resp.SampleAt = env.SampledAt
	for _, src := range env.SourceStatuses {
		src.Broker = maskBrokerURL(src.Broker)
		// Broker libraries occasionally quote the failing URL in the
		// error string — redact there too as defense-in-depth.
		src.LastError = maskBrokerURL(src.LastError)
		resp.Sources = append(resp.Sources, src)
	}
	writeJSON(w, resp)
}
