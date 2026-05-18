package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleMetrics serves server metrics in the Prometheus text exposition format
// (version 0.0.4) at GET /metrics. The route is intentionally unauthenticated,
// following Prometheus convention: it exposes only aggregate counts — the same
// data class as the already-public /api/stats endpoint.
//
// Note: there is deliberately NO `mqtt_connected` metric. The server process
// does not connect to MQTT — the separate ingestor binary does — so the server
// cannot observe MQTT connectivity. `corescope_ingestor_lag_seconds` is the
// ingestion-health signal instead: it measures how stale the most recent
// transmission is, which goes up if the ingestor stops feeding data.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// Prefer store-backed stats when available (same pattern as handleStats).
	var stats *Stats
	var err error
	if s.store != nil {
		stats, err = s.store.GetStoreStats()
	} else {
		stats, err = s.db.GetStats()
	}
	if err != nil {
		writeInternalError(w, "handleMetrics", err)
		return
	}

	uptimeSeconds := time.Since(s.startedAt).Seconds()
	ingestorLagSeconds := s.ingestorLagSeconds()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	type metric struct {
		name  string
		help  string
		typ   string // "counter" or "gauge"
		value string
	}
	metrics := []metric{
		{"corescope_packets_total", "Total number of packets recorded.", "counter", strconv.Itoa(stats.TotalPackets)},
		{"corescope_transmissions_total", "Total number of transmissions recorded.", "counter", strconv.Itoa(stats.TotalTransmissions)},
		{"corescope_observations_total", "Total number of observations recorded.", "counter", strconv.Itoa(stats.TotalObservations)},
		{"corescope_nodes_active", "Number of nodes seen in the last 7 days.", "gauge", strconv.Itoa(stats.TotalNodes)},
		{"corescope_nodes_total", "Total number of nodes ever seen.", "gauge", strconv.Itoa(stats.TotalNodesAllTime)},
		{"corescope_observers_total", "Number of active observers.", "gauge", strconv.Itoa(stats.TotalObservers)},
		{"corescope_packets_last_hour", "Number of packets observed in the last hour.", "gauge", strconv.Itoa(stats.PacketsLastHour)},
		{"corescope_uptime_seconds", "Server uptime in seconds.", "gauge", strconv.FormatFloat(uptimeSeconds, 'f', 3, 64)},
		{"corescope_ingestor_lag_seconds", "Seconds since the most recent transmission; -1 if unknown.", "gauge", strconv.FormatFloat(ingestorLagSeconds, 'f', 3, 64)},
	}

	for _, m := range metrics {
		fmt.Fprintf(w, "# HELP %s %s\n", m.name, m.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", m.name, m.typ)
		fmt.Fprintf(w, "%s %s\n", m.name, m.value)
	}
}

// ingestorLagSeconds returns the number of seconds since the most recent
// transmission was recorded. It returns -1 when there are no transmissions or
// the stored timestamp cannot be parsed, so a missing signal never errors the
// whole /metrics endpoint.
//
// The transmissions table has no `timestamp` column — its time column is
// `first_seen`, a TEXT field holding an RFC3339 string (see the schema in
// cmd/ingestor/db.go). We parse it as RFC3339, falling back to RFC3339Nano and
// the SQLite "2006-01-02 15:04:05" form, matching parsing done elsewhere.
func (s *Server) ingestorLagSeconds() float64 {
	var raw sql.NullString
	if err := s.db.conn.QueryRow("SELECT MAX(first_seen) FROM transmissions").Scan(&raw); err != nil {
		return -1
	}
	if !raw.Valid || raw.String == "" {
		return -1
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw.String); err == nil {
			lag := time.Since(ts).Seconds()
			if lag < 0 {
				lag = 0
			}
			return lag
		}
	}
	return -1
}
