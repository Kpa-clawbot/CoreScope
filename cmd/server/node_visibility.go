package main

import (
	"fmt"
	"log"

	"github.com/meshcore-analyzer/geofilter"
)

// nodeVisibility is the operator's node-hiding rules exactly as /api/nodes
// applies them: nodeBlacklist, hiddenNamePrefixes (#1181) and the geo_filter,
// which spares foreign-flagged nodes (#730). /api/nodes, the repeater planner
// and the route-LOS elevation proxy all decide with Visible, so a node one of
// them hides cannot surface through another (review #2391 point 4).
type nodeVisibility struct {
	cfg *Config          // nodeBlacklist and hiddenNamePrefixes; nil hides nothing
	geo *GeoFilterConfig // nil: no geo_filter
}

// nodeVisibility snapshots the current rules. The geo_filter is read under
// cfgMu (getGeoFilter); the blacklist and hidden prefixes are atomic reads in
// Config.
func (s *Server) nodeVisibility() nodeVisibility {
	if s == nil || s.cfg == nil {
		return nodeVisibility{}
	}
	return nodeVisibility{cfg: s.cfg, geo: s.getGeoFilter()}
}

// Visible reports whether /api/nodes lists the node: not blacklisted, name
// not hidden, and inside the geo_filter unless foreign. lat, lon = 0, 0 is
// "no position fix", which the geo_filter always passes
// (geofilter.PassesFilter).
func (v nodeVisibility) Visible(pubkey, name string, lat, lon float64, foreign bool) bool {
	if v.cfg.IsBlacklisted(pubkey) || v.cfg.IsNameHidden(name) {
		return false
	}
	return foreign || geofilter.PassesFilter(lat, lon, v.geo)
}

// filters reports whether any rule is configured. /api/nodes rewrites its
// total to the filtered page length only then.
func (v nodeVisibility) filters() bool {
	return v.geo != nil || (v.cfg != nil && (len(v.cfg.NodeBlacklist) > 0 || len(v.cfg.HiddenNamePrefixes) > 0))
}

// infoRule is Visible for rows of the node cache (nodeInfo), which carry no
// foreign flag: foreign holds the pubkeys flagged foreign_advert (nil: none).
func (v nodeVisibility) infoRule(foreign map[string]struct{}) func(n *nodeInfo) bool {
	return func(n *nodeInfo) bool {
		lat, lon := n.Lat, n.Lon
		if !n.HasGPS {
			lat, lon = 0, 0 // no fix, as getAllNodes stores a NULL or 0,0 position
		}
		_, f := foreign[n.PublicKey]
		return v.Visible(n.PublicKey, n.Name, lat, lon, f)
	}
}

// visibleNodeInfo returns the current rule for node-cache rows. It reads the
// foreign set from the DB when a geo_filter is set (the flag matters only
// then), so call it once per pass over the nodes, not per node. If that read
// fails no node is spared: the rule then hides more, never less.
func (s *Server) visibleNodeInfo() func(n *nodeInfo) bool {
	v := s.nodeVisibility()
	var foreign map[string]struct{}
	if v.geo != nil && s.db != nil && s.db.conn != nil {
		f, err := s.db.foreignNodeKeys()
		if err != nil {
			log.Printf("[visibility] foreign_advert read failed, foreign nodes outside the geo_filter stay hidden: %v", err)
		}
		foreign = f
	}
	return v.infoRule(foreign)
}

// nodeVisibilityGen changes whenever a rule changes: the blacklist and
// hidden-prefix generations, and the geo_filter, which setGeoFilter swaps as
// a whole pointer so its address identifies the version. The foreign flag is
// not part of it: the ingestor only ever sets it, so a stale rule can only
// hide a newly foreign node until its next rebuild, never show a hidden one.
func (s *Server) nodeVisibilityGen() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%p", s.cfg.BlacklistGeneration(), s.cfg.HiddenNamePrefixesGeneration(), s.getGeoFilter())
}

// foreignNodeKeys returns the pubkeys (as stored) of the nodes the ingestor
// flagged foreign_advert = 1 (#730; it writes only 0 or 1), through the
// partial index idx_nodes_foreign_advert. Read-only.
func (db *DB) foreignNodeKeys() (map[string]struct{}, error) {
	rows, err := db.conn.Query(`SELECT public_key FROM nodes WHERE foreign_advert = 1 AND public_key IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out[pk] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
