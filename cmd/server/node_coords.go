package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/meshcore-analyzer/geofilter"
)

// Node coordinates (location privacy, B3): every read of nodes.lat / nodes.lon
// in cmd/server lives in this file. A position enters server memory through
// servedNodePos, which applies the location fuzzer (identity when the feature
// is off), so everything served or computed from it — node lists, observer
// positions, distances, neighbor graph, path inspect, planner, reach, route
// LOS — sees the approximate position only.
//
// The exceptions read the exact position for classification and never serve
// it or compute with it:
//
//	exactNodePositions    the geo_filter of /api/nodes (nodeVisibility)
//	GetNodePubkeysInArea  area membership (?area=), an operator polygon
//	GetNodesForGeoPrune   the admin geo-filter prune preview and confirm
//
// plus nodeInfo.exactLat/exactLon, which getAllNodes fills for the
// geo_filter of the node cache (nodeVisibility.infoRule). TestNodeCoordReads
// (node_coords_invariant_test.go) fails on any other SQL that selects lat/lon
// from nodes or inactive_nodes.

// setLocationFuzzer installs the fuzzer the coordinate reads apply (main right
// after OpenDB, NewServer). nil turns it off.
func (db *DB) setLocationFuzzer(f *locationFuzzer) {
	if db != nil {
		db.locFuzz.Store(f)
	}
}

// locationFuzzer is the installed fuzzer; nil (identity) when off.
func (db *DB) locationFuzzer() *locationFuzzer {
	if db == nil {
		return nil
	}
	return db.locFuzz.Load()
}

// servedNodePos is the position the server serves and computes with for the
// node pubkey whose stored position is (lat, lon). Unchanged when location
// privacy is off. When it is on, a half-NULL position is no position at all
// (both NULL), so neither coordinate is served exactly, and so is the
// position of a pubkey the fuzzer refuses (not 64 hex digits).
func (db *DB) servedNodePos(pubkey string, lat, lon sql.NullFloat64) (sql.NullFloat64, sql.NullFloat64) {
	f := db.locationFuzzer()
	if f == nil {
		return lat, lon
	}
	if !lat.Valid || !lon.Valid {
		return sql.NullFloat64{}, sql.NullFloat64{}
	}
	la, lo, ok := f.Fuzz(pubkey, lat.Float64, lon.Float64)
	if !ok {
		return sql.NullFloat64{}, sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: la, Valid: true}, sql.NullFloat64{Float64: lo, Valid: true}
}

// exactNodePos is a node's stored position, for the geo_filter only: never
// serve it or compute with it. ok is false when the position is NULL.
type exactNodePos struct {
	lat, lon float64
	ok       bool
}

// exactNodePositions returns the stored positions of the given nodes (keys
// as stored), keyed the same way, for the geo_filter of /api/nodes. A key
// with no row is absent. One query through the primary key.
func (db *DB) exactNodePositions(keys []string) (map[string]exactNodePos, error) {
	out := make(map[string]exactNodePos, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	keysJSON, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err := db.conn.Query("SELECT public_key, lat, lon FROM nodes WHERE public_key IN (SELECT value FROM json_each(?))", string(keysJSON))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pk, &lat, &lon); err != nil {
			return nil, err
		}
		out[pk] = exactNodePos{lat: lat.Float64, lon: lon.Float64, ok: lat.Valid && lon.Valid}
	}
	return out, rows.Err()
}

func (s *PacketStore) getAllNodes() []nodeInfo {
	// Schema probe: try richest → leanest. Logs a one-shot warning at each
	// rung when we fall back to a thinner schema so operators see that a
	// column is missing and the new tiebreak features are degraded. See
	// #1197 (adversarial r1 #10). first_seen was added (#1627 r3) so callers
	// like /api/nodes/{pk}/reach can avoid a per-request single-row SELECT;
	// we fold it into the cached load (see getCachedNodesAndPM, 30s TTL) so
	// the 4 buildNodeInfoMap call sites don't each pay for a fresh scan.
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, COALESCE(advert_count, 0), COALESCE(first_seen, '') FROM nodes")
	hasLastSeen := true
	hasAdvertCount := true
	hasFirstSeen := true
	if err != nil {
		s.logSchemaDegradationOnce("nodes.first_seen missing — buildNodeInfoMap will not populate FirstSeen for /api/nodes/{pk}/reach and friends")
		hasFirstSeen = false
		rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, COALESCE(advert_count, 0) FROM nodes")
		if err != nil {
			s.logSchemaDegradationOnce("nodes.advert_count missing — tier-3/4 ObservationCount tiebreak degraded; resolveWithContext will fall back to lex-pubkey order")
			rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen FROM nodes")
			hasAdvertCount = false
			if err != nil {
				s.logSchemaDegradationOnce("nodes.last_seen missing — node freshness signal unavailable")
				rows, err = s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes")
				hasLastSeen = false
				if err != nil {
					return nil
				}
			}
		}
	}
	defer rows.Close()
	// With location privacy on, the stored position is kept beside the
	// served one for the geo_filter only (nodeInfo.geoFilterPos).
	fuzzed := s.db.locationFuzzer() != nil
	var nodes []nodeInfo
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		var lastSeen sql.NullString
		var advertCount sql.NullInt64
		var firstSeen sql.NullString
		if hasFirstSeen {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &advertCount, &firstSeen)
		} else if hasAdvertCount {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &advertCount)
		} else if hasLastSeen {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen)
		} else {
			rows.Scan(&pk, &name, &role, &lat, &lon)
		}
		n := nodeInfo{PublicKey: pk, Name: nullStrVal(name), Role: nullStrVal(role)}
		if hasFirstSeen && firstSeen.Valid {
			n.FirstSeen = firstSeen.String
		}
		if lat.Valid && lon.Valid {
			sLat, sLon := s.db.servedNodePos(pk, lat, lon)
			n.Lat = sLat.Float64
			n.Lon = sLon.Float64
			n.HasGPS = !(n.Lat == 0 && n.Lon == 0)
			if fuzzed {
				n.exactLat, n.exactLon, n.hasExact = lat.Float64, lon.Float64, true
			}
		}
		if hasLastSeen && lastSeen.Valid && lastSeen.String != "" {
			if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
				n.LastSeen = t
			} else if t, err := time.Parse("2006-01-02 15:04:05", lastSeen.String); err == nil {
				n.LastSeen = t
			}
		}
		if hasAdvertCount && advertCount.Valid {
			n.ObservationCount = int(advertCount.Int64)
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// bulkHealthNodes reads the most recently seen nodes for GetBulkHealth,
// keeping only areaNodes when set. With a region or area filter it reads up
// to 10000 rows so the caller does not under-fill after its exclusions;
// otherwise it stops at limit.
func (s *PacketStore) bulkHealthNodes(limit int, regionFilter bool, areaNodes map[string]bool) ([]bulkHealthNode, error) {
	filtered := regionFilter || areaNodes != nil
	queryLimit := limit
	if filtered {
		queryLimit = 10000
	}
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes ORDER BY last_seen DESC LIMIT ?", queryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []bulkHealthNode
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon)
		if areaNodes != nil && !areaNodes[pk] {
			continue
		}
		lat, lon = s.db.servedNodePos(pk, lat, lon)
		nodes = append(nodes, bulkHealthNode{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lat: nullFloat(lat), lon: nullFloat(lon),
		})
		if !filtered && len(nodes) >= limit {
			break
		}
	}
	return nodes, nil
}

// nodeSelectCols returns the SELECT column list for nodes queries.
// When hasDefaultScope is true, default_scope is appended as the last column.
func (db *DB) nodeSelectCols() string {
	cols := "public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c, foreign_advert"
	if db.hasDefaultScope {
		cols += ", default_scope"
	}
	// #1865: confirmed scopes appended after default_scope; scan order must match.
	if db.hasConfiguredScope {
		cols += ", configured_scope, configured_scope_at"
	}
	return cols
}

// nodeLocation is a node's served position and role, for observer rows. The
// fields keep the JSON-null semantics of nullFloat/nullStr (nil when NULL).
type nodeLocation struct {
	Lat, Lon, Role interface{}
}

// GetNodeLocationsByKeys returns location data only for the given public keys.
// This avoids fetching ALL nodes when only a few keys need to be matched.
func (db *DB) GetNodeLocationsByKeys(keys []string) map[string]nodeLocation {
	result := make(map[string]nodeLocation)
	if len(keys) == 0 {
		return result
	}
	placeholders := make([]string, len(keys))
	args := make([]interface{}, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = strings.ToLower(k)
	}
	// #1481 P0-3: drop LOWER(public_key) — that wrap is non-sargable and
	// forces a full scan. Nodes are stored lowercase already; we lowercase
	// args in Go above so a plain IN matches the index on public_key.
	query := "SELECT public_key, lat, lon, role FROM nodes WHERE public_key IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &lat, &lon, &role)
		lat, lon = db.servedNodePos(pk, lat, lon)
		result[strings.ToLower(pk)] = nodeLocation{Lat: nullFloat(lat), Lon: nullFloat(lon), Role: nullStr(role)}
	}
	return result
}

// scanNodeRow scans a node row. When hasDefaultScope is true the SELECT must
// include default_scope as the last column.
func (db *DB) scanNodeRow(rows *sql.Rows) map[string]interface{} {
	var pk string
	var name, role, lastSeen, firstSeen sql.NullString
	var lat, lon sql.NullFloat64
	var advertCount int
	var batteryMv sql.NullInt64
	var temperatureC sql.NullFloat64
	var foreign sql.NullInt64
	var defaultScope sql.NullString
	var configuredScope, configuredScopeAt sql.NullString

	scanArgs := []interface{}{&pk, &name, &role, &lat, &lon, &lastSeen, &firstSeen, &advertCount, &batteryMv, &temperatureC, &foreign}
	if db.hasDefaultScope {
		scanArgs = append(scanArgs, &defaultScope)
	}
	if db.hasConfiguredScope {
		scanArgs = append(scanArgs, &configuredScope, &configuredScopeAt)
	}
	if err := rows.Scan(scanArgs...); err != nil {
		return nil
	}
	lat, lon = db.servedNodePos(pk, lat, lon)
	m := map[string]interface{}{
		"public_key":             pk,
		"name":                   nullStr(name),
		"role":                   nullStr(role),
		"lat":                    nullFloat(lat),
		"lon":                    nullFloat(lon),
		"last_seen":              nullStr(lastSeen),
		"first_seen":             nullStr(firstSeen),
		"advert_count":           advertCount,
		"last_heard":             nullStr(lastSeen),
		"hash_size":              nil,
		"hash_size_inconsistent": false,
		"foreign":                foreign.Valid && foreign.Int64 != 0,
	}
	if batteryMv.Valid {
		m["battery_mv"] = int(batteryMv.Int64)
	} else {
		m["battery_mv"] = nil
	}
	if temperatureC.Valid {
		m["temperature_c"] = temperatureC.Float64
	} else {
		m["temperature_c"] = nil
	}
	if db.hasDefaultScope {
		m["default_scope"] = nullStr(defaultScope)
	}
	if db.hasConfiguredScope {
		m["configured_scope"] = nullStr(configuredScope)
		m["configured_scope_at"] = nullStr(configuredScopeAt)
	}
	return m
}

// GetNodePubkeysInArea returns public keys of nodes whose GPS coordinates
// fall inside the given area polygon or bounding box. Classification by the
// exact position (an operator-defined area, like the geo_filter): the
// positions never leave this function.
func (db *DB) GetNodePubkeysInArea(entry AreaEntry) ([]string, error) {
	rows, err := db.conn.Query("SELECT public_key, lat, lon FROM nodes WHERE lat IS NOT NULL AND lon IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gf := &geofilter.Config{
		Polygon: entry.Polygon,
		LatMin:  entry.LatMin,
		LatMax:  entry.LatMax,
		LonMin:  entry.LonMin,
		LonMax:  entry.LonMax,
	}

	var result []string
	for rows.Next() {
		var pk string
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pk, &lat, &lon); err != nil {
			continue
		}
		if !lat.Valid || !lon.Valid {
			continue
		}
		// Skip (0,0) — PassesFilter allows it but these nodes have no real GPS.
		if lat.Float64 == 0 && lon.Float64 == 0 {
			continue
		}
		if geofilter.PassesFilter(lat.Float64, lon.Float64, gf) {
			result = append(result, pk)
		}
	}
	return result, rows.Err()
}

// NodeForGeoPrune holds the minimal fields needed for geo-filter pruning.
type NodeForGeoPrune struct {
	PubKey string
	Name   string
	Lat    *float64
	Lon    *float64
}

// GetNodesForGeoPrune returns all nodes with their coordinates for geo-filter evaluation.
// Read-only — safe on the server's mode=ro handle. Exact positions: the
// geo-filter prune preview is admin-only and classifies by the geo_filter.
func (db *DB) GetNodesForGeoPrune() ([]NodeForGeoPrune, error) {
	rows, err := db.conn.Query("SELECT public_key, name, lat, lon FROM nodes ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []NodeForGeoPrune
	for rows.Next() {
		var pk string
		var name sql.NullString
		var lat, lon sql.NullFloat64
		if err := rows.Scan(&pk, &name, &lat, &lon); err != nil {
			continue
		}
		n := NodeForGeoPrune{PubKey: pk, Name: name.String}
		if lat.Valid {
			v := lat.Float64
			n.Lat = &v
		}
		if lon.Valid {
			v := lon.Float64
			n.Lon = &v
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}
