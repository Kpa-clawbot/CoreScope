package main

// Repeater planner — shared contracts (CTO-owned; implementation tickets must
// not change names, JSON tags or signatures here).
//
// Three GET endpoints under /api/planner/ (planner_api.go), OPT-IN via the
// "planner" config block. All terrain comes from SRTM 1° tiles fetched from
// AWS Open Data and kept in memory (terrain_srtm.go): no DB, no disk — the
// server stays read-only.
//
//	/api/planner/profile  ?pts=lat,lon,lat,lon[,...]           multi-point LOS chain
//	/api/planner/relay    ?a=lat,lon&b=lat,lon&ha=&hb=&hr=&range=  best relay for A→B
//	/api/planner/coverage ?bbox=s,w,n,e&sites=&range=&hr=&he=&baseline=&stale=
//
// Job protocol (all three): the server quantises the query to a cache key.
// 200 = finished result (cached ~10 min). 202 = PlannerJobStatus while the job
// runs (terrain download or compute); the client re-polls the SAME URL after
// retry_after_s. 400 bad input, 404 planner disabled, 429 per-IP limit, 503
// busy/terrain unavailable (Retry-After). A job nobody polls for 15 s is
// cancelled.

import "time"

// geoPoint / geoBBox are the internal coordinate types shared by every
// planner file. Degrees, WGS84.
type geoPoint struct{ Lat, Lon float64 }

type geoBBox struct{ S, W, N, E float64 }

// ─── Config ────────────────────────────────────────────────────────────────

// PlannerConfig is the "planner" block in config.json. OPT-IN: nil or
// enabled=false → endpoints 404, /api/config/client omits "planner".
type PlannerConfig struct {
	Enabled bool `json:"enabled"`
	// TileURL is the base of an SRTM "skadi" tile tree:
	// <TileURL>/N30/N30W098.hgt.gz. Default AWS Open Data elevation-tiles-prod.
	TileURL string `json:"tileUrl"`
	// TileCacheTiles caps resident downsampled tiles (~2.9 MB each).
	TileCacheTiles   int     `json:"tileCacheTiles"`
	DefaultRangeKm   float64 `json:"defaultRangeKm"`
	NewAntennaM      float64 `json:"newAntennaM"`
	ExistingAntennaM float64 `json:"existingAntennaM"`
}

const (
	defaultPlannerTileURL      = "https://s3.amazonaws.com/elevation-tiles-prod/skadi"
	defaultPlannerTileCache    = 24
	defaultPlannerRangeKm      = 30.0
	defaultPlannerNewAntennaM  = 10.0
	defaultPlannerExistAntenna = 5.0

	plannerMaxPoints    = 16    // profile: points per request
	plannerMaxLegKm     = 100.0 // profile/relay: max A→B leg
	plannerMaxAreaKm    = 100.0 // coverage: max bbox side after snapping
	plannerMaxSites     = 10    // coverage: max suggested sites
	plannerMaxNodes     = 300   // coverage: max repeaters/rooms considered
	plannerMaxCands     = 500   // coverage: max candidate sites
	plannerMaxEdgesOut  = 2000  // coverage: max edges in the response
	plannerMinClearance = 0.6   // "solid" link: ≥ 60 % first-Fresnel clearance
	plannerNodeMarginKm = 30.0  // nodes this far outside the bbox still count
	plannerGeoRadiusKm  = 50.0  // profile/relay points must be this close to a known node
)

// plannerRangesKm is the fixed set of allowed range values (security R6:
// quantised cache keys). The UI offers exactly these.
var plannerRangesKm = []float64{10, 15, 20, 30, 40, 50, 60}

// PlannerClientConfig is /api/config/client "planner"; present only when enabled.
type PlannerClientConfig struct {
	DefaultRangeKm   float64   `json:"defaultRangeKm"`
	RangesKm         []float64 `json:"rangesKm"`
	NewAntennaM      float64   `json:"newAntennaM"`
	ExistingAntennaM float64   `json:"existingAntennaM"`
	FrequencyMHz     float64   `json:"frequencyMhz"`
	KFactor          float64   `json:"kFactor"`
	MaxPoints        int       `json:"maxPoints"`
	MaxLegKm         float64   `json:"maxLegKm"`
	MaxAreaKm        float64   `json:"maxAreaKm"`
	MaxSites         int       `json:"maxSites"`
	MinClearance     float64   `json:"minClearance"`
}

// ─── Wire types ────────────────────────────────────────────────────────────

// PlannerJobStatus is the 202 body while a job runs.
type PlannerJobStatus struct {
	State       string  `json:"state"`    // queued | loading_terrain | computing
	Progress    float64 `json:"progress"` // 0..1
	RetryAfterS int     `json:"retry_after_s"`
}

// PlannerProfileResponse — /api/planner/profile. legs[i] is pts[i]→pts[i+1].
type PlannerProfileResponse struct {
	Source string              `json:"source"` // "SRTM (AWS Open Data)"
	Legs   []PlannerProfileLeg `json:"legs"`
	// Warnings, e.g. terrain missing for some tiles (read as sea level).
	Warnings []string `json:"warnings,omitempty"`
}

// PlannerProfileLeg: ElevM holds terrain metres at evenly spaced
// great-circle fractions t=i/(len-1), both ends included; the length varies
// with leg distance (planProfileSamples). Error codes: too_long,
// out_of_area (point not near the mesh), no_terrain.
type PlannerProfileLeg struct {
	DistanceKm float64   `json:"distance_km"`
	ElevM      []float64 `json:"elev_m,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// PlannerLink summarises one predicted link. ClearancePct = 100 × worst
// first-Fresnel clearance (negative = terrain crosses the line). Verdict is
// one of clear | clear60 | marginal | blocked (public/los.js VERDICTS).
// ElevM (optional) is the terrain profile decimated to ≤ 200 samples.
type PlannerLink struct {
	DistanceKm   float64   `json:"distance_km"`
	ClearancePct float64   `json:"clearance_pct"`
	Verdict      string    `json:"verdict"`
	WorstKm      float64   `json:"worst_km"`
	ObstructionM float64   `json:"obstruction_m"`
	ElevM        []float64 `json:"elev_m,omitempty"`
}

// PlannerRelayResponse — /api/planner/relay.
type PlannerRelayResponse struct {
	Source   string               `json:"source"`
	Direct   PlannerLink          `json:"direct"`
	Existing []PlannerRelayOption `json:"existing"` // known repeaters/rooms, ranked
	Open     []PlannerRelayOption `json:"open"`     // terrain sites, ranked
	Params   PlannerRelayParams   `json:"params"`
	Warnings []string             `json:"warnings"`
}

type PlannerRelayParams struct {
	A             geoPointJSON `json:"a"`
	B             geoPointJSON `json:"b"`
	AntennaAM     float64      `json:"ha"`
	AntennaBM     float64      `json:"hb"`
	RelayAntennaM float64      `json:"hr"`
	// ExistingAntennaM: assumed mast height of existing relay nodes (config).
	ExistingAntennaM float64 `json:"he"`
	RangeKm          float64 `json:"range_km"`
	FrequencyMHz     float64 `json:"frequency_mhz"`
	KFactor          float64 `json:"k_factor"`
}

// PlannerRelayOption: BottleneckPct = min(LegA, LegB).ClearancePct. Options
// are ranked by bottleneck, then total distance. Up to 5 per list; LegA/LegB
// carry ElevM so the UI can draw both profiles without another request.
type PlannerRelayOption struct {
	Kind          string      `json:"kind"` // existing | open
	PubKey        string      `json:"pubkey,omitempty"`
	Name          string      `json:"name,omitempty"`
	Lat           float64     `json:"lat"`
	Lon           float64     `json:"lon"`
	ElevM         float64     `json:"elev_m"`
	AntennaM      float64     `json:"antenna_m"`
	LegA          PlannerLink `json:"leg_a"`
	LegB          PlannerLink `json:"leg_b"`
	BottleneckPct float64     `json:"bottleneck_pct"`
}

type geoPointJSON struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type geoBBoxJSON struct {
	S float64 `json:"s"`
	W float64 `json:"w"`
	N float64 `json:"n"`
	E float64 `json:"e"`
}

// PlannerPlanResponse — /api/planner/coverage (repeater backbone plan).
//
// Objective (lexicographic, greedy, "suggested, not optimal"):
//  1. fewer backbone components (isolated repeaters count as components),
//     ties → larger largest component;
//  2. fewer cut vertices (single points of failure), weighted by the size of
//     the smaller side they separate;
//  3. more in-area repeaters reached at solid quality.
//
// Status: improved | connected (backbone was already one component; sites are
// redundancy picks) | no_gain (no candidate improves anything).
type PlannerPlanResponse struct {
	Area       geoBBoxJSON       `json:"area"`
	Params     PlannerPlanParams `json:"params"`
	Source     string            `json:"source"`
	Status     string            `json:"status"`
	Baseline   PlannerSummary    `json:"baseline"`
	Final      PlannerSummary    `json:"final"`
	Nodes      []PlannerNodeOut  `json:"nodes"`
	Edges      []PlannerEdgeOut  `json:"edges"`
	Sites      []PlannerSite     `json:"sites"`
	Warnings   []string          `json:"warnings"`
	Truncated  bool              `json:"truncated"`
	ComputedAt time.Time         `json:"computed_at"`
}

type PlannerPlanParams struct {
	Sites            int     `json:"sites"`
	RangeKm          float64 `json:"range_km"`
	NewAntennaM      float64 `json:"hr"`
	ExistingAntennaM float64 `json:"he"`
	Baseline         string  `json:"baseline"` // predicted | observed
	IncludeStale     bool    `json:"stale"`
	FrequencyMHz     float64 `json:"frequency_mhz"`
	KFactor          float64 `json:"k_factor"`
	MinClearance     float64 `json:"min_clearance"`
}

type PlannerSummary struct {
	Repeaters        int `json:"repeaters"` // in-area repeaters/rooms counted
	Components       int `json:"components"`
	LargestComponent int `json:"largest_component"`
	Isolated         int `json:"isolated"`
	CutVertices      int `json:"cut_vertices"`
	StaleExcluded    int `json:"stale_excluded"`
}

// PlannerNodeOut — only fields /api/nodes already exposes (security R21).
type PlannerNodeOut struct {
	PubKey    string  `json:"pubkey"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Stale     bool    `json:"stale"`
	InArea    bool    `json:"in_area"`
	Component int     `json:"component"` // baseline component id (0 = largest)
}

// PlannerEdgeOut: A/B index Nodes. Class: observed | solid | marginal.
type PlannerEdgeOut struct {
	A            int     `json:"a"`
	B            int     `json:"b"`
	Class        string  `json:"class"`
	DistanceKm   float64 `json:"distance_km"`
	ClearancePct float64 `json:"clearance_pct"`
}

// PlannerSite is one greedy pick. Reason: joins | redundancy | reach.
type PlannerSite struct {
	Step     int               `json:"step"` // 1-based greedy order
	Lat      float64           `json:"lat"`
	Lon      float64           `json:"lon"`
	ElevM    float64           `json:"elev_m"`
	AntennaM float64           `json:"antenna_m"`
	Reason   string            `json:"reason"`
	Gain     PlannerGain       `json:"gain"`
	After    PlannerSummary    `json:"after"` // backbone summary after this pick
	Links    []PlannerSiteLink `json:"links"`
	// JoinsComponents lists the baseline component ids this site connects.
	JoinsComponents []int `json:"joins_components"`
}

type PlannerGain struct {
	ComponentsJoined   int `json:"components_joined"`
	CutVerticesRemoved int `json:"cut_vertices_removed"`
	RepeatersReached   int `json:"repeaters_reached"`
}

// PlannerSiteLink: Node indexes Nodes, or -1 when the peer is an earlier
// site (PeerStep = its step). Class: solid | marginal.
type PlannerSiteLink struct {
	Node         int     `json:"node"`
	PeerStep     int     `json:"peer_step,omitempty"`
	DistanceKm   float64 `json:"distance_km"`
	ClearancePct float64 `json:"clearance_pct"`
	Class        string  `json:"class"`
}
