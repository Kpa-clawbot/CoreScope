package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
)

// Latest /neighbors report per observer (#2270, #2271); schema in
// internal/dbschema/observer_neighbors.go.
//
// The report is unauthenticated, so what is stored is a sanitised copy:
// neighbors with a 64-hex key only, numbers finite and in range, strings
// clamped, at most maxStoredNeighbors rows.

const maxStoredNeighbors = 500

var neighborStatusRe = regexp.MustCompile(`^[a-z_]{1,32}$`)

// StoredNeighbor is one neighbors[] entry as kept in neighbors_json.
type StoredNeighbor struct {
	Pubkey       string   `json:"pubkey"`
	SNR          *float64 `json:"snr,omitempty"`
	HeardSecsAgo *int64   `json:"heard_secs_ago,omitempty"`
	Status       string   `json:"status,omitempty"`
	Scopes       string   `json:"scopes,omitempty"`
}

// sanitizeNeighbors keeps the well-formed entries of a report's neighbors[].
func sanitizeNeighbors(raw []interface{}) []StoredNeighbor {
	out := make([]StoredNeighbor, 0, len(raw))
	for _, item := range raw {
		if len(out) >= maxStoredNeighbors {
			break
		}
		n, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pk, _ := n["pubkey"].(string)
		pk = strings.ToLower(strings.TrimSpace(pk))
		if !targetPubkeyRe.MatchString(pk) {
			continue
		}
		e := StoredNeighbor{Pubkey: pk}
		if f, ok := toFloat64(n["snr"]); ok && !math.IsNaN(f) && f >= -64 && f <= 64 {
			f = math.Round(f*100) / 100
			e.SNR = &f
		}
		if f, ok := toFloat64(n["heard_secs_ago"]); ok && f >= 0 && f <= 10*365*86400 {
			v := int64(f)
			e.HeardSecsAgo = &v
		}
		if s, _ := n["status"].(string); neighborStatusRe.MatchString(s) {
			e.Status = s
		}
		if s, _ := n["scopes"].(string); s != "" {
			e.Scopes = clampObserverField(strings.TrimSpace(s), 256)
		}
		out = append(out, e)
	}
	return out
}

// UpsertNeighborReport stores the observer's latest report.
//
// Only a live report is dated "now". A replay is dated by the observer's own
// timestamp and replaces the stored row only when it is newer, so it can fill
// a gap but never make a long-dead observer look fresh. What counts as a
// replay is judged like status liveness (#2330): the retain flag, or a
// payload timestamp older than statusLivenessMaxAge. The flag alone is not
// enough, because a mosquitto bridge republishes retained messages with
// RETAIN cleared (statusIsLiveness explains how).
func (s *Store) UpsertNeighborReport(observerID string, msg map[string]interface{}, retained bool, now time.Time) error {
	if observerID == "" {
		return nil
	}
	rawList, _ := msg["neighbors"].([]interface{})
	neighbors := sanitizeNeighbors(rawList)
	count := len(neighbors)
	// total_neighbors is the observer's own count (firmware may truncate the
	// list); trust it only when it is at least what arrived.
	if f, ok := toFloat64(msg["total_neighbors"]); ok && f >= float64(len(rawList)) && f <= 100000 {
		count = int(f)
	}
	var queried interface{}
	if f, ok := toFloat64(msg["queried_neighbors"]); ok && f >= 0 && f <= 100000 {
		queried = int(f)
	}
	var selfScopes, selfDefault interface{}
	if self, ok := msg["self"].(map[string]interface{}); ok {
		if v, ok := self["scopes"].(string); ok {
			selfScopes = clampObserverField(strings.TrimSpace(v), 256)
		}
		if v, ok := self["default_scope"].(string); ok && v != "" {
			selfDefault = clampObserverField(strings.TrimSpace(v), 64)
		}
	}
	reportedAt, _ := msg["timestamp"].(string)
	body, err := json.Marshal(neighbors)
	if err != nil {
		return err
	}
	receivedAt := now.UTC()
	// A live report replaces the row; a replay only when it is newer.
	onConflict := ""
	if t, _, perr := parseEnvelopeTime(reportedAt); perr == nil && (retained || t.Before(now.Add(-statusLivenessMaxAge))) {
		if t.Before(receivedAt) {
			receivedAt = t.UTC()
		}
		onConflict = " WHERE excluded.received_at > observer_neighbor_reports.received_at"
	} else if retained {
		// A replay we cannot date says nothing about when it was made.
		return nil
	}
	_, err = s.db.Exec(`INSERT INTO observer_neighbor_reports
		(observer_id, received_at, reported_at, neighbor_count, queried_neighbors, self_scopes, self_default_scope, neighbors_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(observer_id) DO UPDATE SET
			received_at = excluded.received_at, reported_at = excluded.reported_at,
			neighbor_count = excluded.neighbor_count, queried_neighbors = excluded.queried_neighbors,
			self_scopes = excluded.self_scopes, self_default_scope = excluded.self_default_scope,
			neighbors_json = excluded.neighbors_json`+onConflict,
		observerID, receivedAt.Format(time.RFC3339), nilIfEmpty(clampObserverField(reportedAt, 40)),
		count, queried, selfScopes, selfDefault, string(body))
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}
