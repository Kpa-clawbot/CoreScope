package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// Latest /neighbors report per observer (#2270, #2271), written by the
// ingestor (cmd/ingestor/neighbor_reports.go). The table is optional for the
// server: absent, it reads as "no report" and is probed again next time.

// ObserverNeighbor is one neighbor of the latest report, with the node's name
// when the analyzer knows it.
type ObserverNeighbor struct {
	Pubkey       string   `json:"pubkey"`
	Name         *string  `json:"name"`
	Role         *string  `json:"role"`
	SNR          *float64 `json:"snr"`
	HeardSecsAgo *int64   `json:"heard_secs_ago"`
	Status       string   `json:"status,omitempty"`
	Scopes       string   `json:"scopes,omitempty"`
}

// ObserverNeighborReport is the latest report of one observer.
type ObserverNeighborReport struct {
	ReceivedAt       string             `json:"received_at"`
	ReportedAt       *string            `json:"reported_at"`
	NeighborCount    int                `json:"neighbor_count"`
	QueriedNeighbors *int               `json:"queried_neighbors"`
	SelfScopes       *string            `json:"self_scopes"`
	SelfDefaultScope *string            `json:"self_default_scope"`
	Neighbors        []ObserverNeighbor `json:"neighbors"`
}

// neighborSummary is what the observers list shows: when the last report
// arrived and how many neighbors it named.
type neighborSummary struct {
	receivedAt string
	count      int
}

func (db *DB) hasNeighborReports() bool {
	var n int
	return db.conn.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='observer_neighbor_reports'`).Scan(&n) == nil
}

// GetObserverNeighborSummaries returns observer id -> latest report summary.
func (db *DB) GetObserverNeighborSummaries() map[string]neighborSummary {
	out := map[string]neighborSummary{}
	if !db.hasNeighborReports() {
		return out
	}
	rows, err := db.conn.Query(`SELECT observer_id, received_at, neighbor_count FROM observer_neighbor_reports`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s neighborSummary
		if rows.Scan(&id, &s.receivedAt, &s.count) == nil {
			out[id] = s
		}
	}
	return out
}

// GetObserverNeighborReport returns the observer's latest report, nil when it
// never sent one.
func (db *DB) GetObserverNeighborReport(observerID string) (*ObserverNeighborReport, error) {
	if !db.hasNeighborReports() {
		return nil, nil
	}
	var rep ObserverNeighborReport
	var reportedAt, selfScopes, selfDefault sql.NullString
	var queried sql.NullInt64
	var body string
	err := db.conn.QueryRow(`SELECT received_at, reported_at, neighbor_count, queried_neighbors,
			self_scopes, self_default_scope, neighbors_json
		FROM observer_neighbor_reports WHERE observer_id = ?`, observerID).
		Scan(&rep.ReceivedAt, &reportedAt, &rep.NeighborCount, &queried, &selfScopes, &selfDefault, &body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reportedAt.Valid {
		rep.ReportedAt = &reportedAt.String
	}
	if queried.Valid {
		q := int(queried.Int64)
		rep.QueriedNeighbors = &q
	}
	if selfScopes.Valid {
		rep.SelfScopes = &selfScopes.String
	}
	if selfDefault.Valid {
		rep.SelfDefaultScope = &selfDefault.String
	}
	rep.Neighbors = []ObserverNeighbor{}
	if err := json.Unmarshal([]byte(body), &rep.Neighbors); err != nil {
		return nil, err
	}
	db.nameNeighbors(rep.Neighbors)
	return &rep, nil
}

// nameNeighbors fills name and role from nodes, one query for the report.
func (db *DB) nameNeighbors(neighbors []ObserverNeighbor) {
	if len(neighbors) == 0 {
		return
	}
	args := make([]interface{}, len(neighbors))
	for i, n := range neighbors {
		args[i] = strings.ToLower(n.Pubkey)
	}
	rows, err := db.conn.Query(`SELECT public_key, name, role FROM nodes WHERE public_key IN (?`+
		strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	type nodeInfo struct{ name, role sql.NullString }
	byKey := map[string]nodeInfo{}
	for rows.Next() {
		var pk string
		var ni nodeInfo
		if rows.Scan(&pk, &ni.name, &ni.role) == nil {
			byKey[strings.ToLower(pk)] = ni
		}
	}
	for i := range neighbors {
		ni, ok := byKey[strings.ToLower(neighbors[i].Pubkey)]
		if !ok {
			continue
		}
		if ni.name.Valid && ni.name.String != "" {
			name := ni.name.String
			neighbors[i].Name = &name
		}
		if ni.role.Valid && ni.role.String != "" {
			role := ni.role.String
			neighbors[i].Role = &role
		}
	}
}
