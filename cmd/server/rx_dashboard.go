package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// scanCoverageRows reads (lat,lon,snr,rssi,heard_key,rx_at) rows into coverageRow values.
func scanCoverageRows(rows *sql.Rows) ([]coverageRow, error) {
	out := []coverageRow{}
	for rows.Next() {
		var lat, lon float64
		var snr sql.NullFloat64
		var rssi sql.NullInt64
		var heardKey, rxAt sql.NullString
		if err := rows.Scan(&lat, &lon, &snr, &rssi, &heardKey, &rxAt); err != nil {
			return nil, err
		}
		cr := coverageRow{Lat: lat, Lon: lon, HeardKey: strings.ToLower(heardKey.String), RxAt: rxAt.String}
		if snr.Valid {
			v := snr.Float64
			cr.SNR = &v
		}
		if rssi.Valid {
			v := int(rssi.Int64)
			cr.RSSI = &v
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// heardKeyResolver returns a request-scoped, memoized nodeResolver. It maps a heard_key
// to (pubkey, name) on a unique match — so the same node heard under different prefix
// lengths collapses into one entry — and to (heardKey, "") when unknown or ambiguous.
func (s *Server) heardKeyResolver() nodeResolver {
	if s.db == nil || s.db.conn == nil {
		return nil
	}
	type kv struct{ key, name string }
	cache := map[string]kv{}
	return func(heardKey string) (string, string) {
		if v, ok := cache[heardKey]; ok {
			return v.key, v.name
		}
		key, name := s.resolveHeardKey(heardKey)
		cache[heardKey] = kv{key, name}
		return key, name
	}
}

// resolveHeardKey resolves a heard_key (2-3 byte prefix or full pubkey) to a canonical
// (pubkey, name) on a unique match. Unknown or ambiguous (>1 match) keys return the
// heard_key itself with an empty name. LIMIT 2 is enough to tell unique from ambiguous.
func (s *Server) resolveHeardKey(heardKey string) (string, string) {
	if heardKey == "" || !hexPrefixRe.MatchString(heardKey) {
		return heardKey, ""
	}
	rows, err := s.db.conn.Query(`SELECT public_key, COALESCE(name,'') FROM nodes WHERE public_key LIKE ? LIMIT 2`, heardKey+"%")
	if err != nil {
		return heardKey, ""
	}
	defer rows.Close()
	var pks, names []string
	for rows.Next() {
		var pk, n string
		if err := rows.Scan(&pk, &n); err != nil {
			return heardKey, ""
		}
		pks = append(pks, pk)
		names = append(names, n)
	}
	if len(pks) == 1 {
		// Same identity-hiding parity as /api/nodes/resolve (#15, #1181): don't
		// surface a blacklisted or hidden-prefix node's name in coverage tooltips.
		if s.cfg.IsBlacklisted(pks[0]) || s.cfg.IsNameHidden(names[0]) {
			return heardKey, ""
		}
		return pks[0], names[0]
	}
	return heardKey, ""
}

// queryCoverageFiltered returns coverage rows within a bbox, optionally filtered
// by heard node (prefix/pubkey), contributing client (rx_pubkey), and time window
// (days; 0 = all time). Powers the global and per-observer coverage maps.
func (s *Server) queryCoverageFiltered(node, rx string, days int, b bbox) ([]coverageRow, error) {
	where := []string{"lat BETWEEN ? AND ?", "lon BETWEEN ? AND ?"}
	args := []interface{}{b.MinLat, b.MaxLat, b.MinLon, b.MaxLon}
	if node != "" {
		// Sargable heard_key IN-list (see coverageHeardKeyCandidates) so the
		// (heard_key, …) composite index is used instead of a substr() scan (#5).
		cands := coverageHeardKeyCandidates(node)
		where = append(where, "heard_key IN ("+sqlPlaceholders(len(cands))+")")
		for _, c := range cands {
			args = append(args, c)
		}
	}
	if rx != "" {
		where = append(where, "rx_pubkey = ?")
		args = append(args, strings.ToLower(rx))
	}
	if days > 0 {
		since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
		where = append(where, "rx_at >= ?")
		args = append(args, since)
	}
	rows, err := s.db.conn.Query("SELECT lat, lon, snr, rssi, heard_key, rx_at FROM client_receptions WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCoverageRows(rows)
}

// handleRxCoverage serves global (or per-observer via ?rx=) coverage as GeoJSON
// hexbins, over a time window. ?node= also works (same as the per-node endpoint).
// requireClientRxCoverage writes a 404 and returns false when the opt-in
// client-RX coverage feature is disabled, so the coverage endpoints read as
// "not found" instead of serving data on deployments that haven't enabled it.
func (s *Server) requireClientRxCoverage(w http.ResponseWriter, r *http.Request) bool {
	// Routes are registered unconditionally, so guard against a nil server/cfg
	// (e.g. handlers exercised in isolation) rather than panicking (#4).
	// ClientRxCoverageEnabled is itself nil-receiver-safe.
	if s == nil || s.cfg == nil || !s.cfg.ClientRxCoverageEnabled() {
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) handleRxCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRxCoverage(w, r) {
		return
	}
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	days := clampDays(atoiDefault(r.URL.Query().Get("days"), 7))
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	rows, err := s.queryCoverageFiltered(r.URL.Query().Get("node"), r.URL.Query().Get("rx"), days, b)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	fc := aggregateCoverage(rows, zoomToHexRes(z), s.heardKeyResolver())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}

// --- Leaderboard (top mobile observers) ---

type LeaderObserver struct {
	Pubkey     string `json:"pubkey"`
	Name       string `json:"name"`
	Receptions int    `json:"receptions"`
	Nodes      int    `json:"nodes"`
}
type RxLeaderboardResp struct {
	Days      int              `json:"days"`
	Observers []LeaderObserver `json:"observers"`
}

func (s *Server) rxLeaderboard(days, limit int) ([]LeaderObserver, error) {
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	// Name preference: the node's advertised name, else the companion's
	// self-reported name (client_observers), else empty (UI shows the prefix).
	rows, err := s.db.conn.Query(`
		SELECT cr.rx_pubkey, COALESCE(NULLIF(n.name,''), NULLIF(co.name,''), ''), COUNT(*), COUNT(DISTINCT cr.heard_key)
		FROM client_receptions cr
		LEFT JOIN nodes n ON n.public_key = cr.rx_pubkey
		LEFT JOIN client_observers co ON co.pubkey = cr.rx_pubkey
		WHERE cr.rx_at >= ?
		GROUP BY cr.rx_pubkey
		ORDER BY COUNT(*) DESC
		LIMIT ?`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LeaderObserver{}
	for rows.Next() {
		var o LeaderObserver
		if err := rows.Scan(&o.Pubkey, &o.Name, &o.Receptions, &o.Nodes); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Server) handleRxLeaderboard(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRxCoverage(w, r) {
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	days := clampDays(atoiDefault(r.URL.Query().Get("days"), 7))
	limit := atoiDefault(r.URL.Query().Get("limit"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	obs, err := s.rxLeaderboard(days, limit)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(RxLeaderboardResp{Days: days, Observers: obs})
}

func atoiDefault(s string, d int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return d
}
