package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type observerComparisonPeer struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Total    int      `json:"total"`
	Shared   int      `json:"shared"`
	SNRPairs int      `json:"snrPairs"`
	SNRDelta *float64 `json:"snrDelta"`
}

type observerComparisonResponse struct {
	ReferenceTotal int                      `json:"referenceTotal"`
	Peers          []observerComparisonPeer `json:"peers"`
}

func (s *Server) handleObserverComparison(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		writeError(w, http.StatusBadRequest, "Reference observer is required")
		return
	}
	key, receiver := "idx", "observer_idx"
	end := time.Now().Unix() + 1 // Include the current observation second.
	start := end - 24*60*60
	window := "timestamp >= ? AND timestamp < ?"
	args := []any{start, end}
	if !s.db.isV3 {
		key, receiver = "id", "observer_id"
		// Legacy rows can store Unix seconds or ISO text, including offsets.
		// Seek by local date first, then check exact UTC seconds inside that
		// bounded range. An offset can put either boundary on the adjacent date.
		window = "(" + window + " OR (timestamp >= ? AND timestamp < ? AND unixepoch(timestamp) BETWEEN ? AND ?))"
		firstDate := time.Unix(start, 0).UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		afterDate := time.Unix(end, 0).UTC().AddDate(0, 0, 2).Format(time.DateOnly)
		args = append(args, firstDate, afterDate, start, end-1)
	}
	var refKey any
	ctx := r.Context()
	err := s.db.conn.QueryRowContext(ctx, "SELECT "+key+" FROM observers WHERE id=? AND COALESCE(inactive,0)=0", ref).Scan(&refKey)
	if errors.Is(err, sql.ErrNoRows) || s.cfg.IsObserverBlacklisted(ref) {
		writeError(w, http.StatusNotFound, "Observer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count each transmission once per receiver. Pair their mean non-null SNR
	// values so extra receptions of one transmission cannot give it more weight.
	query := `WITH received AS MATERIALIZED (
		SELECT transmission_id AS tx, ` + receiver + ` AS receiver, AVG(snr) AS snr
		FROM observations WHERE ` + window + ` AND direction IS NOT 'tx'
		GROUP BY transmission_id, ` + receiver + `
	), baseline AS MATERIALIZED (
		SELECT tx, snr FROM received WHERE receiver = ?
	), totals AS (
		SELECT p.receiver, COUNT(*) AS total, COUNT(b.tx) AS shared,
			COUNT(p.snr-b.snr) AS pairs, AVG(p.snr-b.snr) AS delta
		FROM received p LEFT JOIN baseline b ON b.tx=p.tx GROUP BY p.receiver
	)
	SELECT ob.id, COALESCE(NULLIF(ob.name,''),ob.id) AS label, COALESCE(t.total,0),
		COALESCE(t.shared,0) AS shared_count, COALESCE(t.pairs,0), t.delta
	FROM observers ob LEFT JOIN totals t ON t.receiver=ob.` + key + `
	WHERE COALESCE(ob.inactive,0)=0`
	args = append(args, refKey)
	if codes := normalizeRegionCodes(r.URL.Query().Get("region")); len(codes) != 0 {
		regions, _ := json.Marshal(codes)
		query += ` AND (ob.id=? OR UPPER(TRIM(ob.iata)) IN (SELECT value FROM json_each(?)))`
		args = append(args, ref, string(regions))
	}
	rows, err := s.db.conn.QueryContext(ctx, query+" ORDER BY shared_count DESC, label COLLATE NOCASE, ob.id", args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	resp := observerComparisonResponse{Peers: []observerComparisonPeer{}}
	for rows.Next() {
		var peer observerComparisonPeer
		if err := rows.Scan(&peer.ID, &peer.Name, &peer.Total, &peer.Shared, &peer.SNRPairs, &peer.SNRDelta); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if peer.ID == ref {
			resp.ReferenceTotal = peer.Total
		} else if !s.cfg.IsObserverBlacklisted(peer.ID) {
			resp.Peers = append(resp.Peers, peer)
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, resp)
}
