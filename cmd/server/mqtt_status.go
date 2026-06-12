package main

import (
	"encoding/json"
	"net/http"
	"os"
)

// MqttSourceStatus is the per-MQTT-source status row surfaced via
// /api/mqtt/status. Mirrors the on-disk shape the ingestor publishes.
// STUB (#1043 red commit): broker URL is passed through unmodified —
// password masking is the green-commit work; the test asserts the
// masked behavior and must fail here.
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

// handleMqttStatus serves GET /api/mqtt/status. STUB: returns the raw
// ingestor-published rows with no redaction so the red-commit test fails
// on the masking assertion (assertion-fail, not compile-fail).
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
	resp.Sources = append(resp.Sources, env.SourceStatuses...)
	writeJSON(w, resp)
}
