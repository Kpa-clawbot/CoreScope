package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
)

// routeMeta holds metadata for a single API route.
type routeMeta struct {
	Summary     string      `json:"summary"`
	Description string      `json:"description,omitempty"`
	Tag         string      `json:"tag"`
	Auth        bool        `json:"auth,omitempty"`
	QueryParams []paramMeta `json:"queryParams,omitempty"`
	// Response, when non-nil, is the OpenAPI schema object for the 200
	// application/json response body. Routes without it fall back to the
	// generic {"type":"object"} placeholder. Use schemaRef(...) to point
	// at a named entry in components/schemas (see componentSchemas).
	Response map[string]interface{} `json:"-"`
}

type paramMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type"` // "string", "integer", "boolean"
}

// routeDescriptions returns metadata for all known API routes.
// Key format: "METHOD /path/pattern"
func routeDescriptions() map[string]routeMeta {
	return map[string]routeMeta{
		// Config
		"GET /api/config/cache":      {Summary: "Get cache configuration", Tag: "config"},
		"GET /api/config/client":     {Summary: "Get client configuration", Tag: "config"},
		"GET /api/config/regions":    {Summary: "Get configured regions", Tag: "config"},
		"GET /api/config/theme":      {Summary: "Get theme configuration", Description: "Returns color maps, CSS variables, and theme defaults.", Tag: "config"},
		"GET /api/config/map":        {Summary: "Get map configuration", Tag: "config"},
		"GET /api/config/geo-filter": {Summary: "Get geo-filter configuration", Tag: "config"},

		// Admin / system
		"GET /api/health":      {Summary: "Health check", Description: "Returns server health, uptime, and memory stats.", Tag: "admin"},
		"GET /api/stats":       {Summary: "Network statistics", Description: "Returns aggregate stats (node counts, packet counts, observer counts). Cached for 10s.", Tag: "admin"},
		"GET /api/perf":        {Summary: "Performance statistics", Description: "Returns per-endpoint request timing and slow query log.", Tag: "admin"},
		"GET /api/mqtt/status": {Summary: "MQTT source status", Description: "Returns per-MQTT-source connection state and counters (lastConnectUnix, lastPacketUnix, packetsTotal, etc.). Broker URL passwords are masked. Sourced from the ingestor stats file; empty list when unavailable. (#1043)", Tag: "admin"},
		"POST /api/perf/reset": {Summary: "Reset performance stats", Tag: "admin", Auth: true},
		// "POST /api/admin/prune" removed in #1283 (ingestor owns prune).
		"GET /api/debug/affinity": {Summary: "Debug neighbor affinity scores", Tag: "admin", Auth: true},
		"GET /api/backup":         {Summary: "Download SQLite backup", Description: "Streams a consistent SQLite snapshot of the analyzer DB (VACUUM INTO). Response is application/octet-stream with attachment filename corescope-backup-<unix>.db.", Tag: "admin", Auth: true},

		// Packets
		"GET /api/packets": {Summary: "List packets", Description: "Returns decoded packets with filtering, sorting, and pagination.", Tag: "packets",
			QueryParams: []paramMeta{
				{Name: "limit", Description: "Max packets to return", Type: "integer"},
				{Name: "offset", Description: "Pagination offset", Type: "integer"},
				{Name: "sort", Description: "Sort field", Type: "string"},
				{Name: "order", Description: "Sort order (asc/desc)", Type: "string"},
				{Name: "type", Description: "Filter by packet type", Type: "string"},
				{Name: "observer", Description: "Filter by observer ID", Type: "string"},
				{Name: "timeRange", Description: "Time range filter (e.g. 1h, 24h, 7d)", Type: "string"},
				{Name: "search", Description: "Full-text search", Type: "string"},
				{Name: "groupByHash", Description: "Group duplicate packets by hash", Type: "boolean"},
			}},
		"POST /api/packets":              {Summary: "Ingest a packet", Description: "Submit a raw packet for decoding and storage.", Tag: "packets", Auth: true},
		"GET /api/packets/{id}":          {Summary: "Get packet detail", Tag: "packets"},
		"GET /api/packets/timestamps":    {Summary: "Get packet timestamp ranges", Tag: "packets"},
		"POST /api/packets/observations": {Summary: "Batch submit observations", Description: "Submit multiple observer sightings for existing packets.", Tag: "packets"},

		// Decode
		"POST /api/decode": {Summary: "Decode a raw packet", Description: "Decodes a hex-encoded packet without storing it.", Tag: "packets"},

		// Nodes
		"GET /api/nodes": {Summary: "List nodes", Description: "Returns all known mesh nodes with status and metadata. Repeater/room rows carry the issue #672 usefulness metrics (traffic_share_score, bridge_score, coverage_score, redundancy_score), the composite usefulness_score + usefulness_grade, and relay-activity counters. See the Node schema.", Tag: "nodes",
			Response: schemaRef("NodeListResponse"),
			QueryParams: []paramMeta{
				{Name: "role", Description: "Filter by node role", Type: "string"},
				{Name: "status", Description: "Filter by status (active/stale/offline)", Type: "string"},
				{Name: "geoFilter", Description: "Overrides the deployment's geo_filter node-list default for this one request: \"1\"/\"true\" excludes nodes outside the configured geo_filter (unless foreign_advert-tagged), \"0\"/\"false\" returns every node regardless. Any other value (including omitting it) uses the deployment default — geo_filter applies to the node list unless config.json sets geoFilterExemptNodeList=true.", Type: "string"},
				{Name: "hasScope", Description: "Issue #1862. \"true\" keeps only nodes that have ever transported at least one region-scoped (TRANSPORT_FLOOD/DIRECT) packet; \"false\" keeps only nodes that never have. Backed by the same relay-activity signal as the Scopes tab's \"Repeaters Never Relaying Any Scope\" section — pair with role=repeater to match its exact semantics.", Type: "string"},
				{Name: "hashRegion", Description: "Issue #1862. Comma-separated region scope name(s) (e.g. \"eu,be\"; leading \"#\" optional, case-insensitive) — keeps only nodes that have transported at least one of them. Combines with hasScope as AND, not OR.", Type: "string"},
			}},
		"GET /api/nodes/search":             {Summary: "Search nodes", Description: "Search nodes by name or public key prefix.", Tag: "nodes", QueryParams: []paramMeta{{Name: "q", Description: "Search query", Type: "string", Required: true}}},
		"GET /api/nodes/bulk-health":        {Summary: "Bulk node health", Description: "Returns health status for all nodes in one call.", Tag: "nodes"},
		"GET /api/nodes/network-status":     {Summary: "Network status summary", Description: "Returns counts of active, stale, and offline nodes.", Tag: "nodes"},
		"GET /api/nodes/{pubkey}":           {Summary: "Get node detail", Description: "Returns full detail for a single node by public key. For repeater/room nodes this includes the issue #672 usefulness axes + composite score/grade (see the Node schema).", Tag: "nodes", Response: schemaRef("NodeDetailResponse")},
		"GET /api/nodes/{pubkey}/health":    {Summary: "Get node health", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/paths":     {Summary: "Get node routing paths", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/analytics": {Summary: "Get node analytics", Description: "Per-node packet counts, timing, and RF stats.", Tag: "nodes"},
		"GET /api/nodes/{pubkey}/hop_analytics": {Summary: "Get node hop-count analytics", Description: "Issue #1812. For each recent transmission that passed through this node as a relay, its hop-count AT THIS NODE — the node's own 0-based index within the packet's resolved relay path, i.e. the number MeshCore firmware compares against flood_max/flood_max_advert/flood_max_unscoped in allowPacketForward. Deliberately not the same number as /analytics' hopDistribution field, which is path length to whichever observer reported the packet (a different, unrelated distance). Only transmissions with a resolved relay path are included.", Tag: "nodes", Response: schemaRef("NodeHopAnalyticsResponse"),
			QueryParams: []paramMeta{
				{Name: "days", Description: "Time window in days, 1-365.", Type: "integer"},
			}},
		"GET /api/nodes/{pubkey}/neighbors": {Summary: "Get node neighbors", Description: "Returns the queried node's first-hop neighbors with affinity scores and observation metadata (count, SNR, distance, observers). Ambiguous edges carry candidate pubkeys.", Tag: "nodes", Response: schemaRef("NodeNeighborsResponse")},

		// Analytics
		"GET /api/analytics/rf":              {Summary: "RF analytics", Description: "SNR/RSSI distributions and statistics.", Tag: "analytics"},
		"GET /api/analytics/topology":        {Summary: "Network topology", Description: "Hop-count distribution and route analysis.", Tag: "analytics"},
		"GET /api/analytics/channels":        {Summary: "Channel analytics", Description: "Message counts and activity per channel.", Tag: "analytics"},
		"GET /api/analytics/distance":        {Summary: "Distance analytics", Description: "Geographic distance calculations between nodes.", Tag: "analytics"},
		"GET /api/analytics/hash-sizes":      {Summary: "Hash size analysis", Description: "Distribution of hash prefix sizes across the network.", Tag: "analytics"},
		"GET /api/analytics/hash-collisions": {Summary: "Hash collision detection", Description: "Identifies nodes sharing hash prefixes.", Tag: "analytics"},
		"GET /api/analytics/subpaths":        {Summary: "Subpath analysis", Description: "Common routing subpaths through the mesh.", Tag: "analytics"},
		"GET /api/analytics/subpaths-bulk":   {Summary: "Bulk subpath analysis", Tag: "analytics"},
		"GET /api/analytics/subpath-detail":  {Summary: "Subpath detail", Tag: "analytics"},
		"GET /api/analytics/neighbor-graph":  {Summary: "Neighbor graph", Description: "Full neighbor affinity graph for visualization.", Tag: "analytics"},
		"GET /api/analytics/wardriving": {Summary: "Wardriving channel analytics", Description: "Activity/entry-point/coverage/signal/session analytics for the #wardriving channel (or another channel via ?channel=): message volume over time, top senders, path[0] entry-point hash-prefix tallies (resolve names via /api/resolve-hops), per-observer coverage (observer's known IATA-derived coordinates, not the sender's — MeshMapper's wardriving messages normally carry an anonymous session token, not live GPS), average SNR/RSSI over the same time buckets as the activity series, each sender's messages grouped into distinct sessions/runs (split on a 15-minute gap, each with an AirtimeMs field — LoRa Time-on-Air × distinct relaying repeaters, same formula as the Overview tab's Relay Airtime Share, omitted in DB-only mode), and any senders who explicitly shared their own position (some clients append plaintext \"<lat>,<lon>\" after the token — a deliberate choice by that sender, confirmed empirically, not something CoreScope infers). Cached 30s per window+channel.", Tag: "analytics",
			QueryParams: []paramMeta{
				{Name: "window", Description: "Time window: 1h, 24h (default), or 7d", Type: "string"},
				{Name: "channel", Description: "Channel name to analyze (default #wardriving)", Type: "string"},
			}},
		"GET /api/analytics/hop-depth": {Summary: "Network-wide hop-depth analytics", Description: "Answers three flood-containment questions in one pass over resolved relay paths, using the same 0-based per-node path-index hop count as /api/nodes/{pubkey}/hop_analytics (issue #1812), not the unrelated observer-distance hopDistribution field: (1) does scoped (TRANSPORT_FLOOD/TRANSPORT_DIRECT) traffic actually travel fewer hops network-wide than unscoped (plain FLOOD, non-advert) traffic, (2) which repeater/room nodes are relaying unscoped flood traffic that already traveled far (high hops, a stronger containment-problem signal) vs merely locally (low hops), and (3) is that containment trending better or worse over the window (timeSeries). Plain DIRECT traffic never undergoes flood propagation and is excluded throughout. Cached 30s per window.", Tag: "analytics",
			QueryParams: []paramMeta{
				{Name: "window", Description: "Time window: 1h, 24h (default), or 7d", Type: "string"},
			},
			Response: schemaRef("HopDepthAnalyticsResponse")},
		"GET /api/analytics/wardriving/sender-messages": {Summary: "Wardriving sender message drill-down", Description: "Individual #wardriving messages from one sender (drill-down behind Top Senders/Sessions): each message's entry-point path (path[0] first, resolve names via /api/resolve-hops), per-observer SNR/RSSI, and lat/lon when that message carried an explicit shared position. Pass since+until (RFC3339) to scope to one session's exact range; otherwise window covers the sender's whole activity in that period. Capped at 200 messages, most-recent-first. Not cached.", Tag: "analytics",
			QueryParams: []paramMeta{
				{Name: "sender", Description: "Sender display name to look up (required, exact match)", Type: "string"},
				{Name: "channel", Description: "Channel name (default #wardriving)", Type: "string"},
				{Name: "window", Description: "Time window when since/until aren't given: 1h, 24h (default), or 7d", Type: "string"},
				{Name: "since", Description: "RFC3339 start time — overrides window when paired with until", Type: "string"},
				{Name: "until", Description: "RFC3339 end time — overrides window when paired with since", Type: "string"},
			}},

		// Channels
		"GET /api/channels":                 {Summary: "List channels", Description: "Returns known mesh channels with message counts.", Tag: "channels"},
		"GET /api/channels/{hash}/messages": {Summary: "Get channel messages", Description: "Returns messages for a specific channel.", Tag: "channels"},

		// Observers
		"GET /api/observers":                 {Summary: "List observers", Description: "Returns all known packet observers/gateways.", Tag: "observers"},
		"GET /api/observers/{id}":            {Summary: "Get observer detail", Tag: "observers"},
		"GET /api/observers/{id}/metrics":    {Summary: "Get observer metrics", Description: "Packet rates, uptime, and performance metrics.", Tag: "observers"},
		"GET /api/observers/{id}/analytics":  {Summary: "Get observer analytics", Tag: "observers"},
		"GET /api/observers/metrics/summary": {Summary: "Observer metrics summary", Description: "Aggregate metrics across all observers.", Tag: "observers"},

		// Misc
		"GET /api/resolve-hops":  {Summary: "Resolve hop path", Description: "Resolves hash prefixes in a hop path to node names. Returns affinity scores and best candidates.", Tag: "nodes", QueryParams: []paramMeta{{Name: "hops", Description: "Comma-separated hop hash prefixes", Type: "string", Required: true}}},
		"GET /api/traces/{hash}": {Summary: "Get packet traces", Description: "Returns all observer sightings for a packet hash.", Tag: "packets"},
		"GET /api/packets/{hash}/path": {Summary: "Get a packet's full geographic flood spread", Description: "Resolves EVERY distinct station that observed a packet to its own branch: hop count (from that station's deepest observation) plus, where resolvable, each relay's name/role/lat/lon in path order and the station's own position (from its configured IATA code, like the Wardriving tab). A station heard more than once (later flood copies via longer routes) contributes only its deepest observation. Lat/lon are null for any hop that has never advertised a GPS position -- callers should draw a gap, not guess. Backs the Channels tab's ping-bot \"View path\" map link.", Tag: "packets",
			Response: schemaRef("PacketPathResponse")},
		"GET /api/iata-coords":       {Summary: "Get IATA airport coordinates", Description: "Returns lat/lon for known airport codes (used for observer positioning).", Tag: "config"},
		"GET /api/audio-lab/buckets": {Summary: "Audio lab frequency buckets", Description: "Returns frequency bucket data for audio analysis.", Tag: "analytics"},
	}
}

// schemaRef returns an OpenAPI $ref pointing at a named component schema.
func schemaRef(name string) map[string]interface{} {
	return map[string]interface{}{"$ref": "#/components/schemas/" + name}
}

// componentSchemas returns the reusable OpenAPI schemas surfaced under
// components/schemas. The Node schema documents the per-node usefulness
// metrics (issue #672) that the /api/nodes handlers attach to repeater/room
// rows — previously these were set on the wire but undocumented (issue
// #672 / E). Score axes are bounded [0,1]; usefulness_score is the weighted
// composite and usefulness_grade its A–F letter.
func componentSchemas() map[string]interface{} {
	score01 := func(desc string) map[string]interface{} {
		// "double" matches the Go float64 wire type (some linters flag "float").
		return map[string]interface{}{
			"type": "number", "format": "double", "minimum": 0, "maximum": 1,
			"description": desc,
		}
	}
	str := func(desc string) map[string]interface{} {
		m := map[string]interface{}{"type": "string"}
		if desc != "" {
			m["description"] = desc
		}
		return m
	}
	return map[string]interface{}{
		"Node": map[string]interface{}{
			"type": "object",
			// additionalProperties:true — the node object carries more fields
			// than documented here (e.g. foreign, default_scope, hash-size and
			// multi-byte enrichment); only the stable + #672 fields are spelled
			// out. The #672 usefulness fields are emitted only by a server that
			// has shipped issue #672 (PR #1762); on an older server they are
			// simply absent.
			"additionalProperties": true,
			"description":          "A mesh node. Repeater and room nodes additionally carry the issue #672 usefulness metrics and relay-activity fields below; those fields are absent on other roles. NOTE: coverage_score, redundancy_score and usefulness_grade ship only with the #672 4-axis scorer (PR #1762) and are absent on every build without it; until that lands usefulness_score is aliased to traffic_share_score. Only traffic_share_score and bridge_score ship today.",
			"properties": map[string]interface{}{
				"public_key":               str("Node public key (hex)."),
				"name":                     str("Node display name (most recent advert name)."),
				"role":                     str("Node role (e.g. repeater, room, client, sensor)."),
				"lat":                      map[string]interface{}{"type": "number", "nullable": true},
				"lon":                      map[string]interface{}{"type": "number", "nullable": true},
				"last_seen":                str("RFC3339 timestamp of the most recent observation."),
				"first_seen":               str("RFC3339 timestamp of the first observation."),
				"advert_count":             map[string]interface{}{"type": "integer"},
				"flood_advert_count_7d":    map[string]interface{}{"type": "integer", "description": "Distinct FLOOD adverts originated in the last 7 days (zero-hop adverts excluded). Present on the node detail endpoint."},
				"battery_mv":               map[string]interface{}{"type": "integer", "nullable": true},
				"temperature_c":            map[string]interface{}{"type": "number", "nullable": true},
				"relay_active":             map[string]interface{}{"type": "boolean", "description": "Repeater/room only: relayed traffic within the active window."},
				"relay_count_1h":           map[string]interface{}{"type": "integer", "description": "Repeater/room only: relay-hop appearances in the last hour."},
				"relay_count_24h":          map[string]interface{}{"type": "integer", "description": "Repeater/room only: relay-hop appearances in the last 24 hours."},
				"unscoped_relay_count_24h": map[string]interface{}{"type": "integer", "description": "Repeater/room only: subset of relay_count_24h that were unscoped floods (route_type FLOOD). A well-configured repeater sets flood.max.unscoped 0, so a non-trivial count flags a base-config problem."},
				"last_relayed":             str("Repeater/room only: RFC3339 time this node last appeared as a relay hop."),
				"relay_window_hours":       map[string]interface{}{"type": "integer", "description": "Repeater/room only, /api/nodes/{pubkey} detail endpoint only: width (hours) of the relay-activity window the relay_count_* values cover."},
				"traffic_share_score":      score01("#672 Traffic axis: share of non-advert traffic relayed through this repeater. Repeater/room only."),
				"bridge_score":             score01("#672 Bridge axis: normalized betweenness centrality (chokepoint importance). Repeater/room only."),
				"coverage_score":           score01("#672 Coverage axis: normalized harmonic reach centrality (how much of the mesh the node can reach). Repeater/room only."),
				"redundancy_score":         score01("#672 Redundancy axis: normalized articulation-point criticality — 1 means removing the node fragments the mesh, 0 means alternate paths exist. Repeater/room only."),
				"usefulness_score":         score01("#672 composite usefulness = 0.30·bridge + 0.25·coverage + 0.25·redundancy + 0.20·traffic. Until the 4-axis scorer ships (PR #1762) this is aliased to traffic_share_score. Repeater/room only."),
				"usefulness_grade": map[string]interface{}{
					"type": "string", "enum": []string{"A", "B", "C", "D", "F"},
					"description": "Letter grade derived from usefulness_score. Repeater/room only.",
				},
			},
		},
		"NodeListResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"nodes":  map[string]interface{}{"type": "array", "items": schemaRef("Node")},
				"total":  map[string]interface{}{"type": "integer", "description": "Total nodes matching the query after filtering."},
				"counts": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "integer"}, "description": "Per-role node counts."},
			},
		},
		"NodeDetailResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"node":          schemaRef("Node"),
				"recentAdverts": map[string]interface{}{"type": "array", "items": schemaRef("NodeAdvert"), "description": "Up to 20 most recent transmissions from this node (newest first)."},
			},
		},
		"NodeAdvert": map[string]interface{}{
			"type":                 "object",
			"description":          "A recent transmission/advert from a node (the /api/packets transmission shape). Only the commonly-used fields are documented.",
			"additionalProperties": true,
			"properties": map[string]interface{}{
				"id":           map[string]interface{}{"type": "integer"},
				"hash":         str("Transmission content hash."),
				"payload_type": map[string]interface{}{"type": "integer", "description": "MeshCore payload type."},
				"first_seen":   str("RFC3339 time the transmission was first observed."),
				"from_pubkey":  str("Originating node public key."),
			},
		},
		"CandidateEntry": map[string]interface{}{
			"type":        "object",
			"description": "A candidate pubkey offered when a neighbor edge is ambiguous.",
			"properties": map[string]interface{}{
				"pubkey": str("Candidate node public key (hex)."), "name": str("Candidate node display name."), "role": str("Candidate node role (e.g. repeater, room)."),
			},
		},
		"NeighborEntry": map[string]interface{}{
			"type":        "object",
			"description": "One neighbor of the queried node, with affinity score and observation metadata.",
			"properties": map[string]interface{}{
				"pubkey":         map[string]interface{}{"type": "string", "nullable": true, "description": "Resolved neighbor public key, or null when only a hop prefix is known."},
				"prefix":         str("Raw hop hash prefix that established this edge."),
				"name":           map[string]interface{}{"type": "string", "nullable": true},
				"role":           map[string]interface{}{"type": "string", "nullable": true},
				"count":          map[string]interface{}{"type": "integer", "description": "Total observations supporting this neighborship."},
				"score":          score01("Affinity score: count saturation × recency decay × observer-diversity confidence."),
				"counts_by_mode": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "integer"}, "description": "#1638: observation counts keyed by hash-prefix mode in bytes (1/2/3; 0 = legacy/unknown)."},
				"first_seen":     str(""),
				"last_seen":      str(""),
				"avg_snr":        map[string]interface{}{"type": "number", "nullable": true},
				"distance_km":    map[string]interface{}{"type": "number", "nullable": true},
				"observers":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"ambiguous":      map[string]interface{}{"type": "boolean"},
				"unresolved":     map[string]interface{}{"type": "boolean"},
				"candidates":     map[string]interface{}{"type": "array", "items": schemaRef("CandidateEntry")},
			},
		},
		"NodeNeighborsResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"node":               str("The queried node's public key."),
				"neighbors":          map[string]interface{}{"type": "array", "items": schemaRef("NeighborEntry")},
				"total_observations": map[string]interface{}{"type": "integer"},
			},
		},
		"HopAnalyticsPacket": map[string]interface{}{
			"type":        "object",
			"description": "One transmission that passed through the queried node as a relay hop (issue #1812).",
			"properties": map[string]interface{}{
				"hash":      str("Packet hash."),
				"tsMs":      map[string]interface{}{"type": "integer", "description": "First-seen timestamp, Unix milliseconds."},
				"hops":      map[string]interface{}{"type": "integer", "description": "0-based index of the queried node within the packet's resolved relay path — the value MeshCore firmware compares against flood_max in allowPacketForward. NOT distance to the reporting observer."},
				"transport": map[string]interface{}{"type": "string", "enum": []string{"flood", "flood_advert", "flood_unscoped", "direct", "unknown"}, "description": "Which firmware flood.max* knob (if any) caps this packet's hop count."},
				"scoped":    map[string]interface{}{"type": "boolean", "description": "Whether the transmission carried a region scope (TRANSPORT_FLOOD/TRANSPORT_DIRECT)."},
			},
		},
		"NodeHopAnalyticsResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"packets": map[string]interface{}{"type": "array", "items": schemaRef("HopAnalyticsPacket")},
			},
		},
		"HopDepthBucket": map[string]interface{}{
			"type":        "object",
			"description": "How many relay-hop instances (network-wide) saw a given hop count.",
			"properties": map[string]interface{}{
				"hops":  map[string]interface{}{"type": "integer", "description": "0-based hop index."},
				"count": map[string]interface{}{"type": "integer"},
			},
		},
		"RepeaterUnscopedHopDepth": map[string]interface{}{
			"type":        "object",
			"description": "One repeater/room's hop-count profile across the unscoped (plain FLOOD, non-advert) traffic it has relayed.",
			"properties": map[string]interface{}{
				"publicKey":  str("Node's public key."),
				"name":       str("Node's display name, or its public key if unnamed."),
				"count":      map[string]interface{}{"type": "integer", "description": "Number of unscoped relay-hop instances at this node."},
				"minHops":    map[string]interface{}{"type": "integer"},
				"medianHops": map[string]interface{}{"type": "number"},
				"maxHops":    map[string]interface{}{"type": "integer"},
			},
		},
		"HopDepthTimePoint": map[string]interface{}{
			"type":        "object",
			"description": "One time bucket's scoped/unscoped median hop depth (5min/1h/6h buckets for 1h/24h/7d windows, same bucketing as ScopeStatsResponse.timeSeries).",
			"properties": map[string]interface{}{
				"t":                 str("Bucket start, RFC3339 UTC."),
				"scopedMedianHop":   map[string]interface{}{"type": "integer", "nullable": true, "description": "Median hop depth of scoped traffic in this bucket, or null if there was none (0 is a valid median, so absence isn't the same as zero)."},
				"unscopedMedianHop": map[string]interface{}{"type": "integer", "nullable": true, "description": "Median hop depth of unscoped traffic in this bucket, or null if there was none."},
			},
		},
		"HopDepthAnalyticsResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"window":             str("Time window this response covers: 1h, 24h, or 7d."),
				"scopedHopDepth":     map[string]interface{}{"type": "array", "items": schemaRef("HopDepthBucket"), "description": "Hop-depth histogram for scoped (TRANSPORT_FLOOD/TRANSPORT_DIRECT) traffic."},
				"unscopedHopDepth":   map[string]interface{}{"type": "array", "items": schemaRef("HopDepthBucket"), "description": "Hop-depth histogram for unscoped (plain FLOOD) traffic."},
				"unscopedByRepeater": map[string]interface{}{"type": "array", "items": schemaRef("RepeaterUnscopedHopDepth"), "description": "Per-repeater/room breakdown of unscoped hop depth, sorted by count descending."},
				"timeSeries":         map[string]interface{}{"type": "array", "items": schemaRef("HopDepthTimePoint"), "description": "Scoped/unscoped median hop depth over time within the window — is containment trending better or worse."},
			},
		},
		"PacketPathPoint": map[string]interface{}{
			"type":        "object",
			"description": "One hop's position along a packet's resolved relay path.",
			"properties": map[string]interface{}{
				"publicKey": str("Node public key (hex)."),
				"name":      str("Node display name, or its public key if unnamed."),
				"role":      str("Node role (e.g. repeater, room), when known."),
				"lat":       map[string]interface{}{"type": "number", "nullable": true, "description": "Null when this node has never advertised a GPS position."},
				"lon":       map[string]interface{}{"type": "number", "nullable": true},
			},
		},
		"PacketPathObserver": map[string]interface{}{
			"type":        "object",
			"description": "The station that produced a given branch's observation of a packet path, positioned from its configured IATA code.",
			"properties": map[string]interface{}{
				"name": str("Observer display name."),
				"iata": str("Observer's configured IATA airport code, when set."),
				"lat":  map[string]interface{}{"type": "number", "nullable": true},
				"lon":  map[string]interface{}{"type": "number", "nullable": true},
			},
		},
		"PacketPathBranch": map[string]interface{}{
			"type":        "object",
			"description": "One station's own route to a packet: how far it traveled to reach them (from that observation's raw hop count, independent of how much of it resolved) and, where resolvable, each hop's position in path order.",
			"properties": map[string]interface{}{
				"hops":     map[string]interface{}{"type": "integer", "description": "Hop count for this station's deepest observation, taken from the raw path length -- present even when none of it resolved."},
				"points":   map[string]interface{}{"type": "array", "items": schemaRef("PacketPathPoint"), "description": "The resolvable portion of the relay path in hop order. Can be shorter than hops, or empty, when some/all hops never resolved."},
				"observer": schemaRef("PacketPathObserver"),
				"snr":      map[string]interface{}{"type": "number", "nullable": true, "description": "SNR of this station's deepest observation."},
			},
		},
		"PacketPathResponse": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"hash":     str("The packet hash this path was resolved for."),
				"branches": map[string]interface{}{"type": "array", "items": schemaRef("PacketPathBranch"), "description": "One branch per distinct station that observed the packet, each kept at that station's own deepest observation, sorted deepest-first -- shows the full flood spread, not just the single farthest route."},
			},
		},
	}
}

// buildOpenAPISpec constructs an OpenAPI 3.0 spec by walking the mux router.
func buildOpenAPISpec(router *mux.Router, version string) map[string]interface{} {
	descriptions := routeDescriptions()

	// Collect routes from the router
	type routeInfo struct {
		path    string
		method  string
		authReq bool
	}
	var routes []routeInfo

	router.Walk(func(route *mux.Route, router *mux.Router, ancestors []*mux.Route) error {
		path, err := route.GetPathTemplate()
		if err != nil {
			return nil
		}
		if !strings.HasPrefix(path, "/api/") {
			return nil
		}
		// Skip the spec/docs endpoints themselves
		if path == "/api/spec" || path == "/api/docs" {
			return nil
		}
		methods, err := route.GetMethods()
		if err != nil {
			return nil
		}
		for _, m := range methods {
			routes = append(routes, routeInfo{path: path, method: m})
		}
		return nil
	})

	// Sort routes for deterministic output
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})

	// Build paths object
	paths := make(map[string]interface{})
	tagSet := make(map[string]bool)

	for _, ri := range routes {
		key := ri.method + " " + ri.path
		meta, hasMeta := descriptions[key]

		// Convert mux path params {name} to OpenAPI {name} (same format, convenient)
		openAPIPath := ri.path

		// Documented routes can declare a concrete 200 response schema;
		// everything else falls back to the generic object placeholder.
		respSchema := map[string]interface{}{"type": "object"}
		if hasMeta && meta.Response != nil {
			respSchema = meta.Response
		}

		// Build operation
		op := map[string]interface{}{
			"summary": func() string {
				if hasMeta {
					return meta.Summary
				}
				return ri.path
			}(),
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "Success",
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": respSchema,
						},
					},
				},
			},
		}

		if hasMeta {
			if meta.Description != "" {
				op["description"] = meta.Description
			}
			if meta.Tag != "" {
				op["tags"] = []string{meta.Tag}
				tagSet[meta.Tag] = true
			}
			if meta.Auth {
				op["security"] = []map[string]interface{}{
					{"ApiKeyAuth": []string{}},
				}
			}

			// Add query parameters
			if len(meta.QueryParams) > 0 {
				params := make([]interface{}, 0, len(meta.QueryParams))
				for _, qp := range meta.QueryParams {
					p := map[string]interface{}{
						"name":     qp.Name,
						"in":       "query",
						"required": qp.Required,
						"schema":   map[string]interface{}{"type": qp.Type},
					}
					if qp.Description != "" {
						p["description"] = qp.Description
					}
					params = append(params, p)
				}
				op["parameters"] = params
			}
		}

		// Extract path parameters from {name} patterns
		pathParams := extractPathParams(openAPIPath)
		if len(pathParams) > 0 {
			existing, _ := op["parameters"].([]interface{})
			for _, pp := range pathParams {
				existing = append(existing, map[string]interface{}{
					"name":     pp,
					"in":       "path",
					"required": true,
					"schema":   map[string]interface{}{"type": "string"},
				})
			}
			op["parameters"] = existing
		}

		// Add to paths
		methodLower := strings.ToLower(ri.method)
		if _, ok := paths[openAPIPath]; !ok {
			paths[openAPIPath] = make(map[string]interface{})
		}
		paths[openAPIPath].(map[string]interface{})[methodLower] = op
	}

	// Build tags array (sorted)
	tagOrder := []string{"admin", "analytics", "channels", "config", "nodes", "observers", "packets"}
	tagDescriptions := map[string]string{
		"admin":     "Server administration and diagnostics",
		"analytics": "Network analytics and statistics",
		"channels":  "Mesh channel operations",
		"config":    "Server configuration",
		"nodes":     "Mesh node operations",
		"observers": "Packet observer/gateway operations",
		"packets":   "Packet capture and decoding",
	}
	var tags []interface{}
	for _, t := range tagOrder {
		if tagSet[t] {
			tags = append(tags, map[string]interface{}{
				"name":        t,
				"description": tagDescriptions[t],
			})
		}
	}

	spec := map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":       "CoreScope API",
			"description": "MeshCore network analyzer — packet capture, node tracking, and mesh analytics.",
			"version":     version,
			"license": map[string]interface{}{
				"name": "MIT",
			},
		},
		"paths": paths,
		"tags":  tags,
		"components": map[string]interface{}{
			"securitySchemes": map[string]interface{}{
				"ApiKeyAuth": map[string]interface{}{
					"type": "apiKey",
					"in":   "header",
					"name": "X-API-Key",
				},
			},
			"schemas": componentSchemas(),
		},
	}

	return spec
}

// extractPathParams returns parameter names from a mux-style path like /api/nodes/{pubkey}.
func extractPathParams(path string) []string {
	var params []string
	for {
		start := strings.Index(path, "{")
		if start == -1 {
			break
		}
		end := strings.Index(path[start:], "}")
		if end == -1 {
			break
		}
		params = append(params, path[start+1:start+end])
		path = path[start+end+1:]
	}
	return params
}

// handleOpenAPISpec serves the OpenAPI 3.0 spec as JSON.
// The router is injected via RegisterRoutes storing it on the Server.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	spec := buildOpenAPISpec(s.router, s.version)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode spec: %v", err), http.StatusInternalServerError)
	}
}

// handleSwaggerUI serves a minimal Swagger UI page.
func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, swaggerUIHTML)
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>CoreScope API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    html { box-sizing: border-box; overflow-y: scroll; }
    *, *:before, *:after { box-sizing: inherit; }
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: '/api/spec',
      dom_id: '#swagger-ui',
      deepLinking: true,
      presets: [
        SwaggerUIBundle.presets.apis,
        SwaggerUIBundle.SwaggerUIStandalonePreset
      ],
      layout: 'BaseLayout'
    });
  </script>
</body>
</html>`
