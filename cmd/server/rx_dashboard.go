package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
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

// heardKeyResolverFor builds a nodeResolver for exactly the distinct heard_keys
// present in rows, resolving them all in one batched query instead of one query
// per key (the previous per-key resolver was N+1 on the read connection). Maps a
// heard_key to (pubkey, name) on a unique, non-hidden match; to (heardKey, "")
// otherwise. nil when there's no DB.
func (s *Server) heardKeyResolverFor(rows []coverageRow) nodeResolver {
	if s.db == nil || s.db.conn == nil {
		return nil
	}
	keys := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		if r.HeardKey != "" && !seen[r.HeardKey] {
			seen[r.HeardKey] = true
			keys = append(keys, r.HeardKey)
		}
	}
	resolved := s.batchResolveHeardKeys(keys)
	return func(heardKey string) (string, string) {
		if v, ok := resolved[heardKey]; ok {
			return v[0], v[1]
		}
		return heardKey, ""
	}
}

// batchResolveHeardKeys resolves many heard_keys (2-3 byte prefixes or full
// pubkeys) to their canonical (pubkey, name) in a single round-trip per chunk: a
// UNION ALL of one LIMIT-2 prefix lookup each, so per-key work stays bounded
// (2 rows) and the whole set costs one query, not N. A unique match returns
// [pubkey, name]; unknown / ambiguous / blacklisted / hidden-prefix keys (#15,
// #1181) return [heardKey, ""].
func (s *Server) batchResolveHeardKeys(keys []string) map[string][2]string {
	res := make(map[string][2]string, len(keys))
	valid := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if !hexPrefixRe.MatchString(k) {
			res[k] = [2]string{k, ""}
			continue
		}
		valid = append(valid, k)
	}
	// SQLITE_MAX_COMPOUND_SELECT is 500 by default; chunk well under it.
	const chunk = 200
	for i := 0; i < len(valid); i += chunk {
		end := i + chunk
		if end > len(valid) {
			end = len(valid)
		}
		batch := valid[i:end]
		parts := make([]string, len(batch))
		args := make([]interface{}, 0, len(batch)*2)
		for j, k := range batch {
			// Parameterized: the prefix flows in as bound args, never interpolated,
			// so this stays injection-safe regardless of how hexPrefixRe later
			// evolves. The per-prefix LIMIT 2 lives in a subquery because a bare
			// LIMIT on a UNION ALL term is a SQLite syntax error.
			parts[j] = "SELECT * FROM (SELECT ? AS pfx, public_key, COALESCE(name,'') AS nm FROM nodes WHERE public_key LIKE ? LIMIT 2)"
			args = append(args, k, k+"%")
		}
		rows, err := s.db.conn.Query(strings.Join(parts, " UNION ALL "), args...)
		if err != nil {
			// Don't fail the request, but don't fail silently either: a swallowed
			// error here presents as "every name is ambiguous" with no signal.
			log.Printf("WARN batchResolveHeardKeys: %v", err)
			for _, k := range batch {
				res[k] = [2]string{k, ""}
			}
			continue
		}
		type agg struct {
			pk, name string
			cnt      int
		}
		acc := map[string]*agg{}
		for rows.Next() {
			var pfx, pk, nm string
			if err := rows.Scan(&pfx, &pk, &nm); err != nil {
				continue
			}
			a := acc[pfx]
			if a == nil {
				a = &agg{}
				acc[pfx] = a
			}
			a.cnt++
			a.pk, a.name = pk, nm
		}
		rows.Close()
		for _, k := range batch {
			a := acc[k]
			if a != nil && a.cnt == 1 && !s.cfg.IsBlacklisted(a.pk) && !s.cfg.IsNameHidden(a.name) {
				res[k] = [2]string{a.pk, a.name}
			} else {
				res[k] = [2]string{k, ""}
			}
		}
	}
	return res
}

// resolveHeardKey resolves a single heard_key (2-3 byte prefix or full pubkey)
// to its canonical (pubkey, name), or (heardKey, "") when unknown / ambiguous /
// hidden. Thin wrapper over batchResolveHeardKeys so there is one code path.
func (s *Server) resolveHeardKey(heardKey string) (string, string) {
	v := s.batchResolveHeardKeys([]string{heardKey})[heardKey]
	if v[0] == "" && v[1] == "" {
		return heardKey, "" // empty/unresolved → echo the key
	}
	return v[0], v[1]
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
	fc := aggregateCoverage(rows, zoomToHexRes(z), s.heardKeyResolverFor(rows))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}

// --- Leaderboard (top mobile observers) ---

type LeaderObserver struct {
	Pubkey     string  `json:"pubkey"`
	Name       string  `json:"name"`
	Receptions int     `json:"receptions"`
	Nodes      int     `json:"nodes"`
	Cells      int     `json:"cells"` // distinct fixed-res hex cells covered
	Score      float64 `json:"score"` // frontier-weighted coverage score
}
type RxLeaderboardResp struct {
	Days      int              `json:"days"`
	Observers []LeaderObserver `json:"observers"`
}

// leaderboardHexRes is the fixed hex resolution used to bucket receptions into
// "cells visited" for the frontier-weighted score. ~150 m ground cells at our
// latitude: coarse enough that a parked node's GPS jitter stays in one cell,
// fine enough that real driving paints many. Independent of the coverage map's
// zoom-dependent render resolution so the ranking is stable across views.
const leaderboardHexRes = 13

// leaderboardScanCap bounds how many rows the leaderboard aggregates in memory.
// The endpoint is unauthenticated (only requireClientRxCoverage), and the Go-side
// rarity weighting can't push the GROUP BY into SQLite, so without a cap a wide
// window on a busy network would stream the whole table into maps. At the cap we
// log and return a partial (best-effort) ranking rather than OOM (#review r2).
const leaderboardScanCap = 500000

// rxLeaderboard ranks mobile observers by frontier-weighted cell coverage over
// the time window. Each distinct cell an observer covers contributes
// 1/(observers covering that cell): a cell only they reached weighs 1.0, a cell
// shared by N observers weighs 1/N. This rewards expanding the map's edge and is
// spam-proof — a stationary node covers exactly one cell regardless of how many
// receptions it logs. Bucketing + the rarity weight can't be expressed in SQL,
// so we aggregate the window's rows in Go (bounded by leaderboardScanCap).
func (s *Server) rxLeaderboard(ctx context.Context, days, limit int) ([]LeaderObserver, error) {
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	// Name preference: the node's advertised name, else the companion's
	// self-reported name (client_observers), else empty (UI shows the prefix).
	// Hard LIMIT bounds memory; ORDER BY rx_at DESC so a truncated window keeps
	// the most recent receptions.
	rows, err := s.db.conn.QueryContext(ctx, `
		SELECT cr.rx_pubkey, COALESCE(NULLIF(n.name,''), NULLIF(co.name,''), ''),
		       cr.lat, cr.lon, cr.heard_key
		FROM client_receptions cr
		LEFT JOIN nodes n ON n.public_key = cr.rx_pubkey
		LEFT JOIN client_observers co ON co.pubkey = cr.rx_pubkey
		WHERE cr.rx_at >= ?
		ORDER BY cr.rx_at DESC
		LIMIT ?`, since, leaderboardScanCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		name       string
		receptions int
		cells      map[string]struct{}
		nodes      map[string]struct{}
	}
	obsAgg := map[string]*agg{}
	cellObservers := map[string]map[string]struct{}{} // cell -> set of rx_pubkey

	scanned := 0
	for rows.Next() {
		// Honour client cancellation/timeout on the long scan (checked in batches
		// to avoid a per-row context mutex on up to 500k rows).
		if scanned&2047 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var pk, name, heardKey string
		var lat, lon float64
		if err := rows.Scan(&pk, &name, &lat, &lon, &heardKey); err != nil {
			return nil, err
		}
		scanned++
		a := obsAgg[pk]
		if a == nil {
			a = &agg{name: name, cells: map[string]struct{}{}, nodes: map[string]struct{}{}}
			obsAgg[pk] = a
		}
		a.receptions++
		a.nodes[heardKey] = struct{}{}
		cell := hexCellAt(lat, lon, leaderboardHexRes)
		a.cells[cell] = struct{}{}
		set := cellObservers[cell]
		if set == nil {
			set = map[string]struct{}{}
			cellObservers[cell] = set
		}
		set[pk] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if scanned >= leaderboardScanCap {
		log.Printf("[rx-leaderboard] scan hit cap %d over %dd window; ranking is partial (most-recent rows)", leaderboardScanCap, days)
	}

	// Per-cell observer counts EXCLUDING blacklisted contributors, so an operator
	// of a blacklisted node parked in a cell can't silently dilute everyone else's
	// frontier weight (#review r2). Name-hidden (not blacklisted) observers are
	// legitimate contributors and still count.
	cellCount := make(map[string]int, len(cellObservers))
	for cell, set := range cellObservers {
		n := 0
		for pk := range set {
			if !s.cfg.IsObserverBlacklisted(pk) && !s.cfg.IsBlacklisted(pk) {
				n++
			}
		}
		cellCount[cell] = n
	}

	out := make([]LeaderObserver, 0, len(obsAgg))
	for pk, a := range obsAgg {
		var score float64
		for cell := range a.cells {
			if c := cellCount[cell]; c > 0 {
				score += 1.0 / float64(c)
			}
		}
		out = append(out, LeaderObserver{
			Pubkey:     pk,
			Name:       a.name,
			Receptions: a.receptions,
			Nodes:      len(a.nodes),
			Cells:      len(a.cells),
			Score:      score,
		})
	}

	// Rank by frontier score; ties broken by raw receptions then pubkey so the
	// order is deterministic (keeps same-location fixtures stable).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Receptions != out[j].Receptions {
			return out[i].Receptions > out[j].Receptions
		}
		return out[i].Pubkey < out[j].Pubkey
	})

	// Identity hiding parity (#1727 r2): drop observer-blacklisted contributors,
	// blank node-blacklisted / hidden-prefix names, cap at limit. nil cfg ⇒ no-ops.
	filtered := make([]LeaderObserver, 0, limit)
	for _, o := range out {
		if s.cfg.IsObserverBlacklisted(o.Pubkey) {
			continue
		}
		if s.cfg.IsBlacklisted(o.Pubkey) || s.cfg.IsNameHidden(o.Name) {
			o.Name = ""
		}
		filtered = append(filtered, o)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
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
	obs, err := s.rxLeaderboard(r.Context(), days, limit)
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
