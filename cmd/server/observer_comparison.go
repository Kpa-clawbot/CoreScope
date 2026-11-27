package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

type observerComparisonPeer struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Total    int      `json:"total"`
	Shared   int      `json:"shared"`
	SNRPairs int      `json:"snrPairs"`
	SNRDelta *float64 `json:"snrDelta"`
	iata     string
}

type observerComparisonResponse struct {
	ReferenceTotal int                      `json:"referenceTotal"`
	Peers          []observerComparisonPeer `json:"peers"`
}

const (
	comparisonTTL          = 30 * time.Second
	comparisonCacheSize    = 16
	comparisonScanLimit    = 2 // Leave half of the four-reader pool available.
	comparisonQueryTimeout = 30 * time.Second
)

var errComparisonBusy = errors.New("other reference comparisons are running")

type comparisonLoad struct {
	done    chan struct{}
	result  *observerComparisonResponse
	err     error
	expires time.Time
}

type observerComparisonCache struct {
	mu      sync.Mutex
	entries *lru[string, *comparisonLoad]
	loading map[string]*comparisonLoad
}

func (s *Server) handleObserverComparison(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" {
		writeError(w, http.StatusBadRequest, "Reference observer is required")
		return
	}
	if s.cfg.IsObserverBlacklisted(ref) {
		writeError(w, http.StatusNotFound, "Observer not found")
		return
	}
	// Check the indexed active reference even on a hit: a deactivated pin
	// must stop working immediately, independent of the traffic snapshot.
	var active int
	err := s.db.conn.QueryRowContext(r.Context(), "SELECT 1 FROM observers WHERE id=? AND COALESCE(inactive,0)=0", ref).Scan(&active)
	var fleet *observerComparisonResponse
	if err == nil {
		fleet, err = s.cachedObserverComparison(r.Context(), ref)
	}
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "Observer not found")
	case errors.Is(err, errComparisonBusy):
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusTooManyRequests, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, fleet.visiblePeers(normalizeRegionCodes(q.Get("region")), s.cfg))
	}
}

func (fleet *observerComparisonResponse) visiblePeers(codes []string, cfg *Config) *observerComparisonResponse {
	if len(codes) == 0 && (cfg == nil || len(cfg.ObserverBlacklist) == 0) {
		return fleet
	}
	regions := make(map[string]bool, len(codes))
	for _, code := range codes {
		regions[code] = true
	}
	resp := &observerComparisonResponse{ReferenceTotal: fleet.ReferenceTotal, Peers: []observerComparisonPeer{}}
	for _, p := range fleet.Peers {
		if (len(regions) == 0 || regions[p.iata]) && !cfg.IsObserverBlacklisted(p.ID) {
			resp.Peers = append(resp.Peers, p)
		}
	}
	return resp
}

// One completion channel per cold reference, shared by all region requests.
// A disconnected caller leaves immediately without canceling other readers
// or leaving a private channel in the flight. Excess distinct pins retry.
func (s *Server) cachedObserverComparison(ctx context.Context, ref string) (*observerComparisonResponse, error) {
	c := &s.comparisons
	c.mu.Lock()
	if c.entries == nil {
		c.entries = newLRU[string, *comparisonLoad](comparisonCacheSize, 0, nil)
		c.loading = make(map[string]*comparisonLoad)
	}
	if hit, ok := c.entries.Get(ref); ok && time.Now().Before(hit.expires) {
		c.mu.Unlock()
		return hit.result, nil
	}
	load := c.loading[ref]
	if load == nil {
		if len(c.loading) >= comparisonScanLimit {
			c.mu.Unlock()
			return nil, errComparisonBusy
		}
		load = &comparisonLoad{done: make(chan struct{})}
		c.loading[ref] = load
		go func() {
			// A region switch can disconnect the first caller. The shared
			// scan still fills the cache and has its own bounded SQL lifetime.
			scanCtx, cancel := context.WithTimeout(context.Background(), comparisonQueryTimeout)
			defer cancel()
			load.result, load.err = s.queryObserverComparison(scanCtx, ref)
			c.mu.Lock()
			if load.err == nil {
				load.expires = time.Now().Add(comparisonTTL)
				c.entries.Put(ref, load)
			}
			delete(c.loading, ref)
			close(load.done)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-load.done:
		return load.result, load.err
	}
}

func (s *Server) queryObserverComparison(ctx context.Context, ref string) (*observerComparisonResponse, error) {
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

	// Count each transmission once per receiver. Pair their mean non-null SNR
	// values so extra receptions of one transmission cannot give it more weight.
	query := `WITH received AS MATERIALIZED (
		SELECT transmission_id AS tx, ` + receiver + ` AS receiver, AVG(snr) AS snr
		FROM observations WHERE ` + window + ` AND direction IS NOT 'tx'
		GROUP BY transmission_id, ` + receiver + `
	), baseline AS MATERIALIZED (
		SELECT tx, snr FROM received WHERE receiver = (SELECT ` + key + ` FROM observers WHERE id=?)
	), totals AS (
		SELECT p.receiver, COUNT(*) AS total, COUNT(b.tx) AS shared,
			COUNT(p.snr-b.snr) AS pairs, AVG(p.snr-b.snr) AS delta
		FROM received p LEFT JOIN baseline b ON b.tx=p.tx GROUP BY p.receiver
	)
	SELECT ob.id, COALESCE(NULLIF(ob.name,''),ob.id) AS label, COALESCE(t.total,0),
		COALESCE(t.shared,0) AS shared_count, COALESCE(t.pairs,0), t.delta, COALESCE(ob.iata,'')
	FROM observers ob LEFT JOIN totals t ON t.receiver=ob.` + key + `
	WHERE COALESCE(ob.inactive,0)=0 ORDER BY shared_count DESC, label COLLATE NOCASE, ob.id`
	rows, err := s.db.conn.QueryContext(ctx, query, append(args, ref)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &observerComparisonResponse{Peers: []observerComparisonPeer{}}
	found := false
	for rows.Next() {
		var peer observerComparisonPeer
		if err := rows.Scan(&peer.ID, &peer.Name, &peer.Total, &peer.Shared, &peer.SNRPairs, &peer.SNRDelta, &peer.iata); err != nil {
			return nil, err
		}
		if peer.ID == ref {
			found = true
			resp.ReferenceTotal = peer.Total
		} else {
			peer.iata = strings.ToUpper(strings.TrimSpace(peer.iata))
			resp.Peers = append(resp.Peers, peer)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, sql.ErrNoRows
	}
	return resp, nil
}
