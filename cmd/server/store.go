package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// payloadTypeNames maps payload_type int → human-readable name (firmware-standard).
// Must stay in sync with the canonical map in cmd/ingestor/decoder.go and
// cmd/server/decoder.go. Source of truth: firmware/src/Packet.h:19-32.
var payloadTypeNames = map[int]string{
	0: "REQ", 1: "RESPONSE", 2: "TXT_MSG", 3: "ACK", 4: "ADVERT",
	5: "GRP_TXT", 6: "GRP_DATA", 7: "ANON_REQ", 8: "PATH", 9: "TRACE",
	10: "MULTIPART", 11: "CONTROL", 15: "RAW_CUSTOM",
}

// StoreTx is an in-memory transmission with embedded observations.
type StoreTx struct {
	ID               int
	RawHex           string
	Hash             string
	FirstSeen        string
	RouteType        *int
	PayloadType      *int
	DecodedJSON      string
	Observations     []*StoreObs
	ObservationCount int
	// Display fields from longest-path observation
	ObserverID   string
	ObserverName string
	ObserverIATA string
	SNR          *float64
	RSSI         *float64
	PathJSON     string
	Direction    string
	LatestSeen           string // max observation timestamp (or FirstSeen if no observations)
	UniqueObserverCount  int    // cached count of distinct observer IDs
	// Cached parsed fields (set once, read many)
	// pathMu guards parsedPath/pathParsed. txGetParsedPath lazily populates
	// them and is reachable from read queries holding only s.mu.RLock(), so
	// two concurrent readers could otherwise race-write these fields
	// (review item #4). Writers that explicitly (re)set pathParsed do so
	// under s.mu.Lock() which already excludes readers, but they take
	// pathMu too for consistency / race-detector cleanliness.
	pathMu        sync.Mutex
	parsedPath    []string               // cached parsePathJSON result
	pathParsed    bool                   // whether parsedPath has been set
	decodedOnce   sync.Once              // guards parsedDecoded
	parsedDecoded map[string]interface{} // cached json.Unmarshal of DecodedJSON
	// Dedup map: "observerID|pathJSON" → true for O(1) duplicate checks
	obsKeys     map[string]bool
	observerSet map[string]bool // unique observer IDs (for UniqueObserverCount)
}

// StoreObs is a lean in-memory observation (no duplication of transmission fields).
type StoreObs struct {
	ID             int
	TransmissionID int
	ObserverID     string
	ObserverName   string
	ObserverIATA   string
	Direction      string
	SNR            *float64
	RSSI           *float64
	Score          *int
	PathJSON       string
	RawHex         string
	Timestamp      string
}

// ParsedDecoded returns the parsed DecodedJSON map, caching the result.
// Thread-safe via sync.Once — the first call parses, subsequent calls return cached.
func (tx *StoreTx) ParsedDecoded() map[string]interface{} {
	tx.decodedOnce.Do(func() {
		if tx.DecodedJSON != "" {
			json.Unmarshal([]byte(tx.DecodedJSON), &tx.parsedDecoded)
		}
	})
	return tx.parsedDecoded
}

// PacketStore holds all transmissions in memory with indexes for fast queries.
//
// Lock ordering
// =============
// PacketStore uses several mutexes. To prevent deadlocks, locks MUST be
// acquired in the order listed below. Never acquire a higher-numbered lock
// while holding a lower-numbered one.
//
//  1. mu            (sync.RWMutex) — guards the core packet data: packets,
//     indexes (byHash, byTxID, byObsID, byObserver, byNode,
//     byPathHop, byPayloadType), counters, and loaded flag.
//
//  2. cacheMu       (sync.Mutex)  — guards analytics response caches:
//     rfCache, topoCache, hashCache, collisionCache, chanCache,
//     distCache, subpathCache, and their TTLs/hit counters.
//     Also guards rate-limited invalidation state
//     (lastInvalidated, pendingInv).
//
//  3. channelsCacheMu (sync.Mutex) — guards the short-lived GetChannels
//     cache (channelsCacheKey/Exp/Res).
//
//  4. groupedCacheMu (sync.Mutex)  — guards the short-lived
//     QueryGroupedPackets cache.
//
//  5. regionObsMu   (sync.Mutex)  — guards the region→observer mapping
//     cache (regionObsCache, regionObsCacheTime).
//
//  6. hashSizeInfoMu (sync.Mutex)  — guards the cached hash-size-info
//     result (hashSizeInfoCache). Acquired independently or
//     under mu (in EvictStale).
//
// Nesting that occurs today:
//   - IngestNew:               mu → cacheMu → channelsCacheMu  (1 → 2 → 3, OK)
//   - IngestObservations:      mu → cacheMu                    (1 → 2, OK)
//   - RunEviction/EvictStale:  mu → cacheMu → channelsCacheMu  (1 → 2 → 3, OK)
//   - RunEviction/EvictStale:  mu → hashSizeInfoMu             (1 → 6, OK)
//   - invalidateCachesFor:     cacheMu → channelsCacheMu       (2 → 3, OK)
//
// All other locks are acquired independently (no nesting).
// When adding new lock acquisitions, respect this ordering.
type PacketStore struct {
	mu            sync.RWMutex
	db            *DB
	packets       []*StoreTx                 // sorted by first_seen ASC (oldest first; newest at tail)
	byHash        map[string]*StoreTx        // hash → *StoreTx
	byTxID        map[int]*StoreTx           // transmission_id → *StoreTx
	byObsID       map[int]*StoreObs          // observation_id → *StoreObs
	maxTxID       int                        // highest transmission_id in store
	maxObsID      int                        // highest observation_id in store
	byObserver    map[string][]*StoreObs     // observer_id → observations
	byNode        map[string][]*StoreTx      // pubkey → transmissions
	nodeHashes    map[string]map[string]bool // pubkey → Set<hash>
	byPathHop     map[string][]*StoreTx      // lowercase hop/pubkey → transmissions with that hop in path
	relayTimes    map[string][]int64         // lowercase pubkey → sorted unix-millis of relay events (full pubkeys only)
	byPayloadType map[int][]*StoreTx         // payload_type → transmissions
	loaded        bool
	totalObs      int
	insertCount   int64
	queryCount    int64
	// Response caches (separate mutex to avoid contention with store RWMutex)
	cacheMu           sync.Mutex
	rfCache           map[string]*cachedResult // region → cached RF result
	topoCache         map[string]*cachedResult // region → cached topology result
	hashCache         map[string]*cachedResult // region → cached hash-sizes result
	collisionCache    map[string]*cachedResult // cached hash-collisions result keyed by region ("" = global)
	chanCache         map[string]*cachedResult // region → cached channels result
	distCache         map[string]*cachedResult // region → cached distance result
	subpathCache      map[string]*cachedResult // params → cached subpaths result
	rfCacheTTL        time.Duration
	collisionCacheTTL time.Duration
	// Steady-state analytics recomputers (issue #1240). Each holds the
	// latest snapshot for the default region="" / zero-window query of
	// an analytics endpoint in an atomic.Value, refreshed by a
	// background goroutine on a fixed interval. When set, the matching
	// GetAnalytics* function serves from Load() instead of running the
	// on-request compute path. Region/window variants still go through
	// the legacy TTL cache (compute-on-miss).
	analyticsRecomputerMu sync.RWMutex
	recompTopology        *analyticsRecomputer
	recompRF              *analyticsRecomputer
	recompDistance        *analyticsRecomputer
	recompChannels        *analyticsRecomputer
	recompHashCollisions  *analyticsRecomputer
	recompHashSizes       *analyticsRecomputer
	recompRoles           *analyticsRecomputer
	recompObserversClockSkew *analyticsRecomputer
	recompNodesClockSkew     *analyticsRecomputer
	cacheHits    int64
	cacheMisses  int64
	// Rate-limited invalidation (fixes #533: caches cleared faster than hit)
	lastInvalidated time.Time
	pendingInv      *cacheInvalidation // accumulated dirty flags during cooldown
	invCooldown     time.Duration      // minimum time between invalidations
	// Short-lived cache for QueryGroupedPackets (avoids repeated full sort)
	groupedCacheMu    sync.Mutex
	groupedCacheKey   string
	groupedCacheExp   time.Time
	groupedCacheTxs   []*StoreTx // sorted by LatestSeen DESC
	groupedCacheTotal int
	// Short-lived cache for GetChannels (avoids repeated full scan + JSON unmarshal)
	channelsCacheMu  sync.Mutex
	channelsCacheKey string
	channelsCacheExp time.Time
	channelsCacheRes []map[string]interface{}
	// Cached region → observer ID mapping (30s TTL, avoids repeated DB queries)
	regionObsMu        sync.Mutex
	regionObsCache     map[string]map[string]bool
	regionObsCacheTime time.Time
	// Cached area key → node pubkey set (30s per-key TTL)
	areaNodeMu         sync.RWMutex
	areaNodeCache      map[string]map[string]bool
	areaNodeCacheTimes map[string]time.Time
	// Full server config — needed for Areas map in resolveAreaNodes.
	config *Config
	// Cached node list + prefix map (rebuilt on demand, shared across analytics)
	nodeCache     []nodeInfo
	nodePM        *prefixMap
	nodeCacheTime time.Time
	// Per-store dedupe set for one-shot schema-degradation warnings. Field
	// (not package-level) so each test gets a fresh state — see #1199 item 5.
	schemaDegradationLogged sync.Map
	// Precomputed subpath index: raw comma-joined hops → occurrence count.
	// Built during Load(), incrementally updated on ingest. Avoids full
	// packet iteration at query time (O(unique_subpaths) vs O(total_packets)).
	spIndex      map[string]int        // "hop1,hop2" → count
	spTxIndex    map[string][]*StoreTx // "hop1,hop2" → transmissions containing this subpath
	spTotalPaths int                   // transmissions with paths >= 2 hops
	// Atomic snapshot of spIndex+spTotalPaths for lock-free reads in
	// GetAnalyticsSubpathsBulk. Refreshed under s.mu.Lock() whenever spIndex
	// changes so readers never copy the map under RLock.
	spIndexSnap  atomic.Value // stores *spIndexSnapshot
	// Precomputed distance analytics: hop distances and path totals
	// computed during Load() and incrementally updated on ingest.
	distHops  []distHopRecord
	distPaths []distPathRecord
	// By-tx lookup maps for O(1) eviction during incremental updates.
	distHopsByTx  map[int][]int
	distPathsByTx map[int][]int

	// Cached GetNodeHashSizeInfo result — recomputed at most once every 30s.
	// hashSizeInFlt is non-nil while a recompute is in progress; concurrent
	// callers block on it and reuse the result (same pattern as rebuildInFlt).
	hashSizeInfoMu    sync.Mutex
	hashSizeInfoCache map[string]*hashSizeNodeInfo
	hashSizeInfoAt    time.Time
	hashSizeInFlt     chan struct{} // nil when no recompute in flight

	// Cached relay stats batch result — recomputed at most once every 300s
	// or when byPathHop changes (see invalidateRelayStatsCache).
	relayStatsCacheMu     sync.Mutex
	relayStatsCache       map[string]RepeaterNodeStats
	relayStatsCacheAt     time.Time
	relayStatsCacheWindow float64
	relayStatsCacheSig    string

	// Cached multi-byte capability map (pubkey → entry), recomputed every 30s.
	// multiByteCapInFlt is non-nil while a recompute is in progress.
	multiByteCapCache  map[string]*MultiByteCapEntry
	multiByteCapAt     time.Time
	multiByteCapInFlt  chan struct{} // nil when no recompute in flight

	// Cached per-pubkey relay info + usefulness score maps (#1257). These
	// fold the previously per-node GetRepeaterRelayInfo /
	// GetRepeaterUsefulnessScore loop in handleNodes into one O(N) pass
	// per 15s TTL window — eliminating N RLock acquisitions and N×
	// timestamp parses of the same byPathHop entries per request.
	repeaterEnrichMu       sync.Mutex
	repeaterRelayCache     map[string]RepeaterRelayInfo
	repeaterRelayCacheWin  float64
	repeaterRelayAt        time.Time
	repeaterUsefulCache    map[string]float64
	repeaterUsefulAt       time.Time

	// Steady-state recomputer for the two caches above (#1262). When
	// started, an initial sync compute prewarms the caches so the very
	// first /api/nodes?limit=2000 from live.js's SPA bootstrap hits a
	// populated cache instead of paying the 15.7s on-thread rebuild.
	repeaterEnrichRecompMu      sync.Mutex
	repeaterEnrichRecompStarted bool
	repeaterEnrichRecompStop    chan struct{}
	repeaterEnrichRecompDone    chan struct{}

	// Bridge axis (issue #672 axis 2 of 4): atomic snapshot of pubkey
	// → 0..1 betweenness-centrality score over the current neighbor
	// graph. Populated by the bridge recomputer (bridge_recomputer.go);
	// nil until the first compute lands. Read path is a single atomic
	// pointer load — no lock contention with the per-request enrichment
	// path in handleNodes (same discipline as #1248).
	bridgeScoreMap atomic.Pointer[map[string]float64]

	// Precomputed distinct advert pubkey count (refcounted for eviction correctness).
	// Updated incrementally during Load/Ingest/Evict — avoids JSON parsing in GetPerfStoreStats.
	advertPubkeys map[string]int // pubkey → number of advert packets referencing it

	// Debounce map for touchRelayLastSeen: pubkey → last time we wrote last_seen to DB.
	// Limits DB writes to at most 1 per node per 5 minutes.
	lastSeenTouched map[string]time.Time

	// Resolved path membership index: xxhash → []txID (forward) and txID → []hashes (reverse).
	// Replaces per-StoreTx/StoreObs ResolvedPath []*string field (#800).
	resolvedPubkeyIndex           map[uint64][]int  // hash(pubkey) → []txID
	resolvedPubkeyReverse         map[int][]uint64  // txID → []hashes indexed under
	useResolvedPathIndex          bool              // feature flag (default true, off path = conservative)
	maxResolvedPubkeyIndexEntries int               // hard cap for size warning (0 = use default 5M)
	apiResolvedPathLRU            map[int][]*string // obsID → resolved path (LRU cache for API)
	lruOrder                      []int             // FIFO order for LRU eviction
	lruMu                         sync.RWMutex      // guards apiResolvedPathLRU + lruOrder

	// Persisted neighbor graph for hop resolution at ingest time.
	// Accessed via atomic.Pointer because async rebuilds (path_inspect.go
	// ensureNeighborGraph) and ingest-time readers race on the pointer
	// (issue #1203 sub-fix).
	graph atomic.Pointer[NeighborGraph]

	// Singleflight state for ensureNeighborGraph. These were package-globals
	// in #1203 r0 — moved to per-store fields (PR #1208 review) so parallel
	// tests with independent *PacketStore values don't share rebuild state
	// (cross-store deadlock/skip risk under -race).
	rebuildMu    sync.Mutex
	rebuildInFlt chan struct{} // nil when no rebuild is in flight

	// Path inspector score cache (issue #944).
	inspectMu    sync.RWMutex
	inspectCache map[string]*inspectCachedResult

	// Clock skew detection engine.
	clockSkew *ClockSkewEngine

	// Async backfill state: set after backfillResolvedPathsAsync completes.
	backfillComplete atomic.Bool
	// Progress tracking for async backfill (total pending and processed so far).
	backfillTotal     atomic.Int64 // set once at start of async backfill
	backfillProcessed atomic.Int64

	// Bounded cold load: oldest packet timestamp loaded into memory.
	// Empty string means all data is in memory (no limit applied).
	oldestLoaded string

	// Hot startup atomic gates — see contract below.
	//
	// Contract / ordering invariant (PR #1187):
	//   * hashMigrationComplete (set by migrateContentHashesAsync) gates
	//     content-hash–dependent code paths (e.g. dedup correctness on the
	//     write side). Set true ONLY after the migration loop finishes.
	//   * backgroundLoadDone is set true exactly once, after
	//     loadBackgroundChunks finishes its loop AND its post-load index
	//     rebuild. It gates "hot startup has finished filling
	//     retentionHours of data into memory" (used by /api/perf and the
	//     UI's hot-load banner). It says nothing about success — see
	//     backgroundLoadFailed for the success/failure signal.
	//   * backgroundLoadFailed is set true ONLY if at least one chunk
	//     errored during background load. /api/perf surfaces it so
	//     operators can distinguish "done & full" from "done & partial".
	//     Read order MUST be: load backgroundLoadDone first; only if true
	//     is backgroundLoadFailed meaningful.
	// 0 = disabled (current behavior). Background loader fills the rest.
	hotStartupHours        float64
	backgroundLoadDone     atomic.Bool
	backgroundLoadFailed   atomic.Bool
	backgroundLoadProgress atomic.Int64 // 0–100 percent complete

	// Async hash migration state: set after migrateContentHashesAsync completes.
	hashMigrationComplete atomic.Bool

	// Eviction config and stats
	retentionHours  float64        // 0 = unlimited
	maxMemoryMB     int            // 0 = unlimited (packet store memory budget)
	evicted         int64          // total packets evicted
	trackedBytes    int64          // running total of estimated packet store memory
	memoryEstimator func() float64 // injectable for tests; nil = use runtime.ReadMemStats (stats only)
}

type cachedResult struct {
	data      map[string]interface{}
	expiresAt time.Time
}

// cacheTTLSec extracts a duration from the cacheTTL config map.
// Values may be float64 (from JSON) or int. Returns false if key is missing or non-positive.
func cacheTTLSec(m map[string]interface{}, key string) (time.Duration, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	var sec float64
	switch n := v.(type) {
	case float64:
		sec = n
	case int:
		sec = float64(n)
	case int64:
		sec = float64(n)
	default:
		return 0, false
	}
	if sec <= 0 {
		return 0, false
	}
	return time.Duration(sec * float64(time.Second)), true
}

// NewPacketStore creates a new empty packet store backed by db.
// cacheTTLs is the optional cacheTTL map from config.json; keys are strings, values are seconds.
func NewPacketStore(db *DB, cfg *PacketStoreConfig, cacheTTLs ...map[string]interface{}) *PacketStore {
	ps := &PacketStore{
		db:            db,
		packets:       make([]*StoreTx, 0, 65536),
		byHash:        make(map[string]*StoreTx, 65536),
		byTxID:        make(map[int]*StoreTx, 65536),
		byObsID:       make(map[int]*StoreObs, 65536),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		byPathHop:     make(map[string][]*StoreTx),
		relayTimes:    make(map[string][]int64),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		rfCache:       make(map[string]*cachedResult),
		topoCache:     make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),

		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		// #1239: 60 seconds by default. rfCacheTTL is the shared TTL for
		// the RF, topology, distance, hash-sizes, subpath, and channel
		// analytics caches. Distance analytics IS viewed live during
		// active analysis sessions (operators won't tolerate a 5-min
		// lag), so the bump from 15s → 60s smooths the most-frequent
		// cold-miss churn during heavy ingest without freezing data.
		// Override via cacheTTL.analyticsRF in config.json (also
		// propagates to distance / topology / hash-sizes / etc.).
		rfCacheTTL:           60 * time.Second,
		collisionCacheTTL:    3600 * time.Second,
		invCooldown:          300 * time.Second,
		spIndex:              make(map[string]int, 4096),
		spTxIndex:            make(map[string][]*StoreTx, 4096),
		advertPubkeys:        make(map[string]int),
		lastSeenTouched:      make(map[string]time.Time),
		clockSkew:            NewClockSkewEngine(),
		useResolvedPathIndex: true,
		areaNodeCache:      make(map[string]map[string]bool),
		areaNodeCacheTimes: make(map[string]time.Time),
	}
	ps.initResolvedPathIndex()
	if cfg != nil {
		ps.retentionHours = cfg.RetentionHours
		ps.maxMemoryMB = cfg.MaxMemoryMB
		ps.maxResolvedPubkeyIndexEntries = cfg.MaxResolvedPubkeyIndexEntries
		if cfg.HotStartupHours > 0 {
			h := cfg.HotStartupHours
			if ps.retentionHours > 0 && h > ps.retentionHours {
				log.Printf("[store] warning: hotStartupHours (%g) > retentionHours (%g) — clamping", h, ps.retentionHours)
				h = ps.retentionHours
			}
			ps.hotStartupHours = h
		}
	}
	// Wire cacheTTL config values to server-side cache durations.
	if len(cacheTTLs) > 0 && cacheTTLs[0] != nil {
		ct := cacheTTLs[0]
		if v, ok := cacheTTLSec(ct, "analyticsHashSizes"); ok {
			ps.collisionCacheTTL = v
		}
		if v, ok := cacheTTLSec(ct, "analyticsRF"); ok {
			ps.rfCacheTTL = v
		}
		if v, ok := cacheTTLSec(ct, "invalidationDebounce"); ok {
			ps.invCooldown = v
		}
	}
	return ps
}

// Load reads transmissions + observations from SQLite into memory.
// When maxMemoryMB > 0, loads only the newest N transmissions that fit
// within the memory budget, avoiding OOM on large databases.
func (s *PacketStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t0 := time.Now()

	// Count total transmissions for logging.
	var totalInDB int
	if err := s.db.conn.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&totalInDB); err != nil {
		totalInDB = -1 // non-fatal
	}

	// Calculate max packets to load based on memory budget.
	var maxPackets int64
	if s.maxMemoryMB > 0 {
		// Use a typical packet with ~10 observations as the estimate.
		avgBytes := int64(1000) // conservative floor
		if sample := estimateStoreTxBytesTypical(10); sample > avgBytes {
			avgBytes = sample
		}
		maxPackets = (int64(s.maxMemoryMB) * 1048576) / avgBytes
		if maxPackets < 1000 {
			maxPackets = 1000 // minimum 1000 packets
		}
	}
	// maxPackets == 0 means unlimited

	var loadSQL string
	rpCol := ""
	if s.db.hasResolvedPath {
		rpCol = ",\n\t\t\t\to.resolved_path"
	}
	obsRawHexCol := ""
	if s.db.hasObsRawHex {
		obsRawHexCol = ", o.raw_hex"
	}

	// Build WHERE conditions: retention cutoff (mirrors Evict logic) + optional memory-cap limit.
	// When hotStartupHours > 0, use it as the initial cutoff (smaller window = fast startup).
	//
	// PR #1187 r2 #7: compute the hot cutoff ONCE here and reuse the same
	// string for both the SQL filter below AND for s.oldestLoaded later.
	// Two separate time.Now().UTC() calls produced microsecond skew at chunk
	// borders, so the SQL window and oldestLoaded could disagree.
	var loadConditions []string
	hotCutoffHours := s.retentionHours
	if s.hotStartupHours > 0 {
		hotCutoffHours = s.hotStartupHours
	}
	var hotCutoffStr string
	if hotCutoffHours > 0 {
		hotCutoffStr = time.Now().UTC().Add(-time.Duration(hotCutoffHours * float64(time.Hour))).Format(time.RFC3339)
		loadConditions = append(loadConditions, fmt.Sprintf("t.first_seen >= '%s'", hotCutoffStr))
	}
	if maxPackets > 0 {
		loadConditions = append(loadConditions, fmt.Sprintf(
			"t.id IN (SELECT id FROM transmissions ORDER BY first_seen DESC LIMIT %d)", maxPackets))
	}
	filterClause := ""
	if len(loadConditions) > 0 {
		filterClause = "\n\t\t\tWHERE " + strings.Join(loadConditions, "\n\t\t\t  AND ")
	}

	if s.db.isV3 {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRawHexCol + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	} else {
		loadSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRawHexCol + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.id = o.observer_id` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(loadSQL)
	if err != nil {
		return err
	}
	defer rows.Close()

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString
		var resolvedPathStr sql.NullString

		scanArgs := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &observerIATA, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs = append(scanArgs, &obsRawHex)
		}
		if s.db.hasResolvedPath {
			scanArgs = append(scanArgs, &resolvedPathStr)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			log.Printf("[store] scan error: %v", err)
			continue
		}

		hashStr := nullStrVal(hash)
		tx := s.byHash[hashStr]
		if tx == nil {
			tx = &StoreTx{
				ID:          txID,
				RawHex:      nullStrVal(rawHex),
				Hash:        hashStr,
				FirstSeen:   nullStrVal(firstSeen),
				LatestSeen:  nullStrVal(firstSeen),
				RouteType:   nullIntPtr(routeType),
				PayloadType: nullIntPtr(payloadType),
				DecodedJSON: nullStrVal(decodedJSON),
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			s.byHash[hashStr] = tx
			s.packets = append(s.packets, tx)
			s.byTxID[txID] = tx
			if txID > s.maxTxID {
				s.maxTxID = txID
			}
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
			s.trackedBytes += estimateStoreTxBytes(tx)
			atomic.AddInt64(&s.insertCount, 1)
		}

		if obsID.Valid {
			oid := int(obsID.Int64)
			obsIDStr := nullStrVal(observerID)
			obsPJ := nullStrVal(pathJSON)

			// Dedup: skip if same observer + same path already loaded (O(1) map lookup)
			dk := obsIDStr + "|" + obsPJ
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   nullStrVal(observerName),
				ObserverIATA:   nullStrVal(observerIATA),
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				RawHex:         nullStrVal(obsRawHex),
				Timestamp:      normalizeTimestamp(nullStrVal(obsTimestamp)),
			}

			// Decode-window: extract resolved pubkeys for index, don't store on struct.
			rpStr := nullStrVal(resolvedPathStr)
			if rpStr != "" {
				rp := unmarshalResolvedPath(rpStr)
				pks := extractResolvedPubkeys(rp)
				// Feed decode-window consumers for this observation's pubkeys
				if len(pks) > 0 {
					// addToByNode for relay nodes
					for _, pk := range pks {
						s.addToByNode(tx, pk)
					}
					// touchRelayLastSeen handled in post-load pass
					// byPathHop resolved-key entries (#1164: helper invalidates relay stats cache).
					s.addResolvedPubkeysToPathHopIndex(tx, pks, hopsSeen)
					// resolvedPubkeyIndex
					s.addToResolvedPubkeyIndex(tx.ID, pks)
				}
			}

			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}

			s.byObsID[oid] = obs
			if oid > s.maxObsID {
				s.maxObsID = oid
			}

			if obsIDStr != "" {
				s.byObserver[obsIDStr] = append(s.byObserver[obsIDStr], obs)
			}

			s.totalObs++
			s.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	// Post-load: pick best observation (longest path) for each transmission,
	// then re-index so relay hops from resolved_path land in byNode.
	// indexByNode was called earlier (on StoreTx creation) before observations
	// were appended, so tx.ResolvedPath was nil at that point — call it again
	// now that pickBestObservation has propagated the best path.
	for _, tx := range s.packets {
		pickBestObservation(tx)
		s.indexByNode(tx)
	}

	// Build precomputed subpath index for O(1) analytics queries
	s.buildSubpathIndex()

	// Build path-hop index for O(1) node path lookups
	s.buildPathHopIndex()

	// Precompute distance analytics (hop distances, path totals)
	s.buildDistanceIndex()

	// Track oldest loaded timestamp for future SQL fallback queries.
	// When hotStartupHours > 0 use the SAME cutoff string that was used in
	// the load SQL (PR #1187 r2 #7) — recomputing time.Now().UTC() here
	// produced microsecond skew vs. the SQL filter at chunk boundaries.
	if s.hotStartupHours > 0 {
		s.oldestLoaded = hotCutoffStr
	} else if len(s.packets) > 0 {
		s.oldestLoaded = s.packets[0].FirstSeen
	}

	s.loaded = true
	elapsed := time.Since(t0)
	// s.mu.Lock is held here (Load holds it for its whole body), so read
	// trackedBytes directly via the lock-free converter — calling
	// trackedMemoryMB() would re-acquire RLock and deadlock.
	if maxPackets > 0 && totalInDB > len(s.packets) {
		log.Printf("[store] Loaded %d/%d transmissions (%d observations) in %v — bounded by %dMB budget (tracked ~%.0fMB, heap ~%.0fMB)",
			len(s.packets), totalInDB, s.totalObs, elapsed, s.maxMemoryMB, trackedBytesToMB(s.trackedBytes), s.estimatedMemoryMB())
	} else {
		log.Printf("[store] Loaded %d transmissions (%d observations) in %v (tracked ~%.0fMB, heap ~%.0fMB)",
			len(s.packets), s.totalObs, elapsed, trackedBytesToMB(s.trackedBytes), s.estimatedMemoryMB())
	}
	return nil
}

// loadChunk queries a [from, to) time window from SQLite without holding the
// write lock, builds local data structures, then merges them into the store
// under s.mu.Lock(). It is the building block for the background loader.
//
// The chunk is assumed to be older than the data already in the store, so
// localPackets are prepended to s.packets.
//
// byPayloadType is updated here incrementally. byPathHop, spIndex, and
// distHops are NOT updated here — the caller (loadBackgroundChunks) rebuilds
// those once after all chunks are merged.
func (s *PacketStore) loadChunk(from, to time.Time) error {
	fromStr := from.UTC().Format(time.RFC3339)
	toStr := to.UTC().Format(time.RFC3339)

	// Build the same SQL as Load() but with a [from, to) window.
	rpCol := ""
	if s.db.hasResolvedPath {
		rpCol = ",\n\t\t\t\to.resolved_path"
	}
	obsRawHexCol := ""
	if s.db.hasObsRawHex {
		obsRawHexCol = ", o.raw_hex"
	}

	const filterClause = "\n\t\t\tWHERE t.first_seen >= ? AND t.first_seen < ?"

	var chunkSQL string
	if s.db.isV3 {
		chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRawHexCol + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	} else {
		chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRawHexCol + rpCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.id = o.observer_id` + filterClause + `
			ORDER BY t.first_seen ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(chunkSQL, fromStr, toStr)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Local data structures — built without holding the lock.
	localByHash := make(map[string]*StoreTx)
	localPackets := make([]*StoreTx, 0)
	localByTxID := make(map[int]*StoreTx)
	localByObsID := make(map[int]*StoreObs)
	localByObserver := make(map[string][]*StoreObs)
	var localTotalObs int
	var localTrackedBytes int64
	var localMaxTxID int
	var localMaxObsID int

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsID sql.NullInt64
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString
		var resolvedPathStr sql.NullString

		scanArgs := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsID, &observerID, &observerName, &observerIATA, &direction,
			&snr, &rssi, &score, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs = append(scanArgs, &obsRawHex)
		}
		if s.db.hasResolvedPath {
			scanArgs = append(scanArgs, &resolvedPathStr)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			log.Printf("[store] loadChunk scan error: %v", err)
			continue
		}

		hashStr := nullStrVal(hash)
		tx := localByHash[hashStr]
		if tx == nil {
			tx = &StoreTx{
				ID:          txID,
				RawHex:      nullStrVal(rawHex),
				Hash:        hashStr,
				FirstSeen:   nullStrVal(firstSeen),
				LatestSeen:  nullStrVal(firstSeen),
				RouteType:   nullIntPtr(routeType),
				PayloadType: nullIntPtr(payloadType),
				DecodedJSON: nullStrVal(decodedJSON),
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			localByHash[hashStr] = tx
			localPackets = append(localPackets, tx)
			localByTxID[txID] = tx
			if txID > localMaxTxID {
				localMaxTxID = txID
			}
			localTrackedBytes += estimateStoreTxBytes(tx)
		}

		if obsID.Valid {
			oid := int(obsID.Int64)
			obsIDStr := nullStrVal(observerID)
			obsPJ := nullStrVal(pathJSON)

			dk := obsIDStr + "|" + obsPJ
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: txID,
				ObserverID:     obsIDStr,
				ObserverName:   nullStrVal(observerName),
				ObserverIATA:   nullStrVal(observerIATA),
				Direction:      nullStrVal(direction),
				SNR:            nullFloatPtr(snr),
				RSSI:           nullFloatPtr(rssi),
				Score:          nullIntPtr(score),
				PathJSON:       obsPJ,
				RawHex:         nullStrVal(obsRawHex),
				Timestamp:      normalizeTimestamp(nullStrVal(obsTimestamp)),
			}

			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}

			localByObsID[oid] = obs
			if oid > localMaxObsID {
				localMaxObsID = oid
			}
			if obsIDStr != "" {
				localByObserver[obsIDStr] = append(localByObserver[obsIDStr], obs)
			}
			localTotalObs++
			localTrackedBytes += estimateStoreObsBytes(obs)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Pick best observation for each local packet before merging.
	for _, tx := range localPackets {
		pickBestObservation(tx)
	}

	if len(localPackets) == 0 {
		return nil
	}

	// PR #1187 r3 MUST-FIX 1: index↔slice consistency.
	//
	// The previous (r2 #6, commit 2ec762aa) merge order was:
	//   1) prepend s.packets in one critical section
	//   2) populate s.byHash/byTxID/byObsID/byObserver/byNode/byPayloadType
	//      in separate per-batch critical sections
	//   3) bump counters in a third section
	//
	// Any RLock-holding reader between steps observed packets present in
	// s.packets but missing from s.byHash → silent partial data loss in
	// GetPacketByHash and the QueryPackets hash/node fast-paths.
	//
	// We invert the order: build the indexes FIRST in bounded per-batch
	// critical sections, then under a SINGLE final critical section
	// prepend s.packets AND bump counters AND advance maxIDs. The
	// invariant "every tx in s.packets is in s.byHash/s.byTxID" holds at
	// every RLock instant — readers either see the old state, or an
	// intermediate state where indexes contain a superset of s.packets
	// (which is harmless: nothing in s.packets dangles), or the fully
	// merged new state.
	const mergeBatchSize = 500

	for batchStart := 0; batchStart < len(localPackets); batchStart += mergeBatchSize {
		batchEnd := batchStart + mergeBatchSize
		if batchEnd > len(localPackets) {
			batchEnd = len(localPackets)
		}
		batch := localPackets[batchStart:batchEnd]

		// Build the per-batch index views outside the lock so the
		// critical section below is O(batch), not O(total local).
		batchHashes := make(map[string]*StoreTx, len(batch))
		batchTxIDs := make(map[int]*StoreTx, len(batch))
		batchObsIDs := make(map[int]*StoreObs)
		batchByObserver := make(map[string][]*StoreObs)
		for _, tx := range batch {
			batchHashes[tx.Hash] = tx
			batchTxIDs[tx.ID] = tx
			for _, o := range tx.Observations {
				batchObsIDs[o.ID] = o
				if o.ObserverID != "" {
					batchByObserver[o.ObserverID] = append(batchByObserver[o.ObserverID], o)
				}
			}
		}

		s.mu.Lock()
		newObsIDs := make(map[int]bool, len(batchObsIDs))
		for k := range batchObsIDs {
			if s.byObsID[k] == nil {
				newObsIDs[k] = true
			}
		}
		for k, v := range batchHashes {
			if s.byHash[k] == nil {
				s.byHash[k] = v
			}
		}
		for k, v := range batchTxIDs {
			if s.byTxID[k] == nil {
				s.byTxID[k] = v
			}
		}
		for k, v := range batchObsIDs {
			if newObsIDs[k] {
				s.byObsID[k] = v
			}
		}
		for observerID, obsList := range batchByObserver {
			for _, o := range obsList {
				if newObsIDs[o.ID] {
					s.byObserver[observerID] = append(s.byObserver[observerID], o)
				}
			}
		}
		for _, tx := range batch {
			s.indexByNode(tx)
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
		}
		s.mu.Unlock()
		runtime.Gosched()
	}

	// Final atomic step: now that every tx in localPackets is fully
	// indexed, publish it into s.packets and bump counters in one short
	// critical section. After this point the new state is fully visible;
	// before it readers see the old slice (which is still fully indexed).
	s.mu.Lock()
	s.packets = append(localPackets, s.packets...)
	s.totalObs += localTotalObs
	s.trackedBytes += localTrackedBytes
	atomic.AddInt64(&s.insertCount, int64(len(localPackets)))
	if localMaxTxID > s.maxTxID {
		s.maxTxID = localMaxTxID
	}
	if localMaxObsID > s.maxObsID {
		s.maxObsID = localMaxObsID
	}
	s.mu.Unlock()

	log.Printf("[store] background chunk [%s, %s) merged: %d tx, %d obs", fromStr, toStr, len(localPackets), localTotalObs)
	return nil
}

// loadBackgroundChunks fills the remaining retentionHours window by loading
// daily chunks from oldestLoaded back to the retention cutoff. After all
// chunks are merged it rebuilds analytics indexes once. Chunk errors are
// handled by advancing past the failed window so the loop always terminates.
func (s *PacketStore) loadBackgroundChunks() {
	// When hotStartupHours == 0, Load() already loaded the full retention window
	// (or all history when retentionHours == 0), so there is nothing to fill.
	if s.hotStartupHours <= 0 {
		s.backgroundLoadDone.Store(true)
		return
	}

	var target time.Time
	var totalHours float64

	if s.retentionHours > 0 {
		// Bounded retention: fill from the hot cutoff back to retentionHours ago.
		totalHours = s.retentionHours - s.hotStartupHours
		if totalHours <= 0 {
			s.backgroundLoadDone.Store(true)
			return
		}
		target = time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour)))
	} else {
		// Unlimited retention (retentionHours == 0) with hotStartupHours > 0:
		// Load() only loaded the hot window; fill from oldestLoaded back to the
		// earliest record in the DB so history is not silently dropped.
		var oldestTS string
		if err := s.db.conn.QueryRow("SELECT COALESCE(MIN(first_seen), '') FROM transmissions").Scan(&oldestTS); err != nil || oldestTS == "" {
			log.Printf("[store] background loader: could not determine oldest record, skipping fill")
			s.backgroundLoadDone.Store(true)
			return
		}
		t, err := time.Parse(time.RFC3339, oldestTS)
		if err != nil {
			log.Printf("[store] background loader: bad oldest first_seen %q: %v, skipping fill", oldestTS, err)
			s.backgroundLoadDone.Store(true)
			return
		}
		// Target one second before the oldest record so the loop includes it.
		target = t.Add(-time.Second)
		// Estimate total hours for progress reporting.
		totalHours = s.hotStartupHours
		s.mu.RLock()
		if ol := s.oldestLoaded; ol != "" {
			if olT, err := time.Parse(time.RFC3339, ol); err == nil {
				totalHours = olT.Sub(target).Hours()
			}
		}
		s.mu.RUnlock()
		log.Printf("[store] background loader: unlimited retention with hotStartupHours=%g — filling history back to %s", s.hotStartupHours, oldestTS)
	}

	var chunksLoaded float64
	var chunkErrors int
	totalChunks := math.Ceil(totalHours / 24)

	for {
		s.mu.RLock()
		oldest := s.oldestLoaded
		s.mu.RUnlock()

		if oldest == "" {
			break
		}
		chunkEnd, err := time.Parse(time.RFC3339, oldest)
		if err != nil {
			log.Printf("[store] background loader: bad oldestLoaded %q: %v", oldest, err)
			break
		}
		if !chunkEnd.After(target) {
			break
		}

		chunkStart := chunkEnd.Add(-24 * time.Hour)
		if chunkStart.Before(target) {
			chunkStart = target
		}

		chunkStartStr := chunkStart.Format(time.RFC3339)
		if err := s.loadChunk(chunkStart, chunkEnd); err != nil {
			chunkErrors++
			log.Printf("[store] background chunk [%s, %s) error: %v — advancing past it",
				chunkStartStr, chunkEnd.Format(time.RFC3339), err)
		}
		// Always advance oldestLoaded to chunkStart so the loop terminates,
		// even when the chunk was empty (loadChunk skips the update when 0 packets).
		s.mu.Lock()
		if s.oldestLoaded > chunkStartStr || s.oldestLoaded == "" {
			s.oldestLoaded = chunkStartStr
		}
		s.mu.Unlock()

		chunksLoaded++
		if totalChunks > 0 {
			pct := int64(chunksLoaded / totalChunks * 100)
			if pct > 100 {
				pct = 100
			}
			s.backgroundLoadProgress.Store(pct)
		}

		// Yield between chunks so ingest goroutines can acquire s.mu.Lock()
		// without being starved. The chosen tradeoff is brief lock-holds per
		// chunk rather than a configurable sleep; background fill is
		// best-effort and queries fall back to SQL while it runs.
		runtime.Gosched()
	}

	// Rebuild analytics indexes once after all chunks are merged.
	s.mu.Lock()
	s.buildSubpathIndex()
	s.buildPathHopIndex()
	s.buildDistanceIndex()
	s.mu.Unlock()

	s.backgroundLoadDone.Store(true)
	if chunkErrors > 0 {
		s.backgroundLoadFailed.Store(true)
	}
	s.backgroundLoadProgress.Store(100)

	s.mu.RLock()
	totalPkts := len(s.packets)
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if chunkErrors > 0 {
		log.Printf("[store] background load done with %d chunk error(s): %d packets in memory, oldestLoaded=%s", chunkErrors, totalPkts, oldest)
	} else {
		log.Printf("[store] background load complete: %d packets in memory, oldestLoaded=%s", totalPkts, oldest)
	}
}

// pickBestObservation selects the observation with the longest path
// and sets it as the transmission's display observation.
func pickBestObservation(tx *StoreTx) {
	if len(tx.Observations) == 0 {
		return
	}
	best := tx.Observations[0]
	bestLen := pathLen(best.PathJSON)
	for _, obs := range tx.Observations[1:] {
		l := pathLen(obs.PathJSON)
		if l > bestLen {
			best = obs
			bestLen = l
		}
	}
	tx.ObserverID = best.ObserverID
	tx.ObserverName = best.ObserverName
	tx.ObserverIATA = best.ObserverIATA
	tx.SNR = best.SNR
	tx.RSSI = best.RSSI
	tx.PathJSON = best.PathJSON
	tx.Direction = best.Direction
	tx.pathParsed = false // invalidate cached parsed path
}

// pathLen returns the number of hops in a path_json array without a full JSON
// unmarshal (review item #8). It scans the string counting top-level array
// elements; on anything that does not look like a well-formed JSON array it
// falls back to json.Unmarshal so behavior matches the previous implementation
// exactly (0 for malformed input).
func pathLen(pathJSON string) int {
	if pathJSON == "" {
		return 0
	}
	s := strings.TrimSpace(pathJSON)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		// Not a plain array literal — defer to the strict parser.
		var hops []interface{}
		if json.Unmarshal([]byte(pathJSON), &hops) != nil {
			return 0
		}
		return len(hops)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return 0 // "[]"
	}
	count := 1
	depth := 0
	inStr := false
	escaped := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inStr {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case ',':
			if depth == 0 {
				count++
			}
		}
	}
	if depth != 0 || inStr {
		// Malformed — match strict-parser behavior.
		var hops []interface{}
		if json.Unmarshal([]byte(pathJSON), &hops) != nil {
			return 0
		}
		return len(hops)
	}
	return count
}

// indexByNode extracts pubkeys from decoded_json and indexes the transmission.
// indexByNode indexes a transmission under all pubkeys found in its decoded
// JSON. Resolved path pubkeys are handled separately via the decode-window.
// Returns true if any genuinely new node was discovered.
func (s *PacketStore) indexByNode(tx *StoreTx) bool {
	// Track which pubkeys have been indexed for this packet to avoid duplicates.
	indexed := make(map[string]bool)
	foundNew := false

	// Index by decoded JSON fields (pubKey, destPubKey, srcPubKey).
	if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") {
		if decoded := tx.ParsedDecoded(); decoded != nil {
			for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
				if v, ok := decoded[field].(string); ok && v != "" {
					if s.addToByNode(tx, v) {
						foundNew = true
					}
					indexed[v] = true
				}
			}
		}
	}

	return foundNew
}

// addToByNode adds tx to byNode[pubkey] with dedup via nodeHashes.
// Returns true if this is a genuinely new node (pubkey not seen before).
func (s *PacketStore) addToByNode(tx *StoreTx, pubkey string) bool {
	isNew := s.nodeHashes[pubkey] == nil
	if isNew {
		s.nodeHashes[pubkey] = make(map[string]bool)
	}
	if s.nodeHashes[pubkey][tx.Hash] {
		return false
	}
	s.nodeHashes[pubkey][tx.Hash] = true
	s.byNode[pubkey] = append(s.byNode[pubkey], tx)
	return isNew
}

// touchRelayLastSeen updates last_seen in the DB for relay nodes that appear
// in resolved paths. Debounced to at most 1 write per node per 5 minutes.
// resolvedPubkeys is the pre-extracted list from the decode window.
// Must be called under s.mu write lock (reads/writes lastSeenTouched).
func (s *PacketStore) touchRelayLastSeen(resolvedPubkeys []string, now time.Time) {
	if s.db == nil || len(resolvedPubkeys) == 0 {
		return
	}
	const debounceInterval = 5 * time.Minute

	ts := now.UTC().Format(time.RFC3339)
	seen := make(map[string]bool, len(resolvedPubkeys))
	for _, pk := range resolvedPubkeys {
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		if last, ok := s.lastSeenTouched[pk]; ok && now.Sub(last) < debounceInterval {
			continue
		}
		if err := s.db.TouchNodeLastSeen(pk, ts); err == nil {
			s.lastSeenTouched[pk] = now
		}
	}
}

// trackAdvertPubkey increments the advertPubkeys refcount for ADVERT packets.
// Must be called under s.mu write lock.
func (s *PacketStore) trackAdvertPubkey(tx *StoreTx) {
	if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return
	}
	pk := ""
	if v, ok := d["pubKey"].(string); ok {
		pk = v
	} else if v, ok := d["public_key"].(string); ok {
		pk = v
	}
	if pk != "" {
		s.advertPubkeys[pk]++
	}
}

// untrackAdvertPubkey decrements the advertPubkeys refcount for ADVERT packets.
// Must be called under s.mu write lock.
func (s *PacketStore) untrackAdvertPubkey(tx *StoreTx) {
	if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return
	}
	// Use the sync.Once-cached parse instead of re-unmarshaling DecodedJSON,
	// matching trackAdvertPubkey (review item #9).
	d := tx.ParsedDecoded()
	if d == nil {
		return
	}
	pk := ""
	if v, ok := d["pubKey"].(string); ok {
		pk = v
	} else if v, ok := d["public_key"].(string); ok {
		pk = v
	}
	if pk != "" {
		if s.advertPubkeys[pk] <= 1 {
			delete(s.advertPubkeys, pk)
		} else {
			s.advertPubkeys[pk]--
		}
	}
}

// maxQueryLimit caps the page size any caller can request. The desktop
// packets table legitimately asks for 50000; anything larger is abuse and
// would amplify memory (one map built per row). Offset/limit clamping below
// uses this as the hard ceiling.
const maxQueryLimit = 50000

// QueryPackets returns filtered, paginated packets from memory.
func (s *PacketStore) QueryPackets(q PacketQuery) *PacketResult {
	// SQL fallback: if the query window predates the in-memory window, delegate
	// to the DB layer which covers the full SQLite retention period.
	s.mu.RLock()
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if oldest != "" {
		needsSQL := (q.Since != "" && q.Since < oldest) ||
			(q.Until != "" && q.Until < oldest)
		if needsSQL {
			if result, err := s.db.QueryPackets(q); err == nil {
				return result
			} else {
				log.Printf("[store] QueryPackets SQL fallback failed: %v — using in-memory", err)
			}
		}
	}
	atomic.AddInt64(&s.queryCount, 1)

	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	if q.Region != "" && !q.regionObserversResolvedSet {
		q.regionObserversResolved = s.resolveRegionObservers(q.Region)
		q.regionObserversResolvedSet = true
	}

	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > maxQueryLimit {
		q.Limit = maxQueryLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Order == "" {
		q.Order = "DESC"
	}

	// Phase 1: under RLock, filter and collect the ordered page of tx pointers
	// plus the txIDs needed for the resolved-path prefetch.
	s.mu.RLock()
	results := s.filterPackets(q)
	total := len(results)

	// results is oldest-first (ASC). For DESC (default) read backwards from the tail;
	// for ASC read forwards. Both are O(page_size) — no sort copy needed.
	start := q.Offset
	if start >= total {
		s.mu.RUnlock()
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	pageSize := q.Limit
	if start+pageSize > total {
		pageSize = total - start
	}

	pageTxs := make([]*StoreTx, 0, pageSize)
	if q.Order == "ASC" {
		pageTxs = append(pageTxs, results[start:start+pageSize]...)
	} else {
		// DESC: newest items are at the tail; page 0 = last pageSize items reversed
		endIdx := total - start
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			pageTxs = append(pageTxs, results[i])
		}
	}
	pageTxIDs := make([]int, len(pageTxs))
	for i, tx := range pageTxs {
		pageTxIDs[i] = tx.ID
	}
	s.mu.RUnlock()

	// Phase 2: batch-fetch every observation's resolved_path for the whole page
	// in a single chunked SQL query, eliminating the per-packet N+1 (review
	// item #3). Done without s.mu held (it takes lruMu; contract is s.mu→lruMu).
	rpByTx := s.prefetchResolvedPathsForTxs(pageTxIDs)

	// Phase 3: re-acquire RLock to build the response maps (txToMap reads
	// tx.Observations / parsedPath, mutated by ingest under the write lock).
	s.mu.RLock()
	defer s.mu.RUnlock()

	packets := make([]map[string]interface{}, 0, len(pageTxs))
	for _, tx := range pageTxs {
		packets = append(packets, s.txToMapWithRPCached(tx, rpByTx[tx.ID], q.ExpandObservations))
	}
	return &PacketResult{Packets: packets, Total: total}
}

// QueryGroupedPackets returns transmissions grouped by hash (already 1:1).
func (s *PacketStore) QueryGroupedPackets(q PacketQuery) *PacketResult {
	s.mu.RLock()
	oldest := s.oldestLoaded
	s.mu.RUnlock()
	if oldest != "" {
		needsSQL := (q.Since != "" && q.Since < oldest) ||
			(q.Until != "" && q.Until < oldest)
		if needsSQL {
			if result, err := s.db.QueryGroupedPackets(q); err == nil {
				return result
			} else {
				log.Printf("[store] QueryGroupedPackets SQL fallback failed: %v — using in-memory", err)
			}
		}
	}
	atomic.AddInt64(&s.queryCount, 1)

	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > maxQueryLimit {
		q.Limit = maxQueryLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}

	// Cache key covers all filter dimensions. Empty key = no filters.
	cacheKey := q.Since + "|" + q.Until + "|" + q.Region + "|" + q.Area + "|" + q.Node + "|" + q.Hash + "|" + q.Observer + "|" + q.Channel
	if q.Type != nil {
		cacheKey += fmt.Sprintf("|t%d", *q.Type)
	}
	if q.Route != nil {
		cacheKey += fmt.Sprintf("|r%d", *q.Route)
	}

	// Return cached sorted list if still fresh (3s TTL)
	s.groupedCacheMu.Lock()
	if s.groupedCacheTxs != nil && s.groupedCacheKey == cacheKey && time.Now().Before(s.groupedCacheExp) {
		cachedTxs := s.groupedCacheTxs
		cachedTotal := s.groupedCacheTotal
		s.groupedCacheMu.Unlock()
		return groupedTxsToPage(cachedTxs, cachedTotal, q.Offset, q.Limit)
	}
	s.groupedCacheMu.Unlock()

	// Collect StoreTx pointers under read lock; sort outside it.
	s.mu.RLock()
	results := s.filterPackets(q)
	txs := make([]*StoreTx, len(results))
	copy(txs, results)
	s.mu.RUnlock()

	total := len(txs)

	// Full sort by LatestSeen DESC so the cached slice supports all page offsets.
	sort.Slice(txs, func(i, j int) bool {
		return txs[i].LatestSeen > txs[j].LatestSeen
	})

	// Cache the sorted StoreTx slice (not maps) — lightweight and reusable for any page.
	s.groupedCacheMu.Lock()
	s.groupedCacheTxs = txs
	s.groupedCacheTotal = total
	s.groupedCacheKey = cacheKey
	s.groupedCacheExp = time.Now().Add(3 * time.Second)
	s.groupedCacheMu.Unlock()

	return groupedTxsToPage(txs, total, q.Offset, q.Limit)
}

// pagePacketResult returns a window of a PacketResult without re-allocating the slice.
func pagePacketResult(r *PacketResult, offset, limit int) *PacketResult {
	total := r.Total
	if offset >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return &PacketResult{Packets: r.Packets[offset:end], Total: total}
}

// groupedTxsToPage builds map representations only for the requested page of sorted StoreTx pointers.
// This avoids allocating maps for all 30K+ transmissions when only 50 are needed.
func groupedTxsToPage(txs []*StoreTx, total, offset, limit int) *PacketResult {
	if offset >= len(txs) {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	end := offset + limit
	if end > len(txs) {
		end = len(txs)
	}
	page := txs[offset:end]

	packets := make([]map[string]interface{}, len(page))
	for i, tx := range page {
		// #1189 R2: compute distinct IATA set across all observations.
		// Frontend uses this in the default collapsed view to show CROSS-region
		// reception at a glance — see groupedObserverIataBadgesHtml in
		// public/packets.js.
		distinctIatas := storeTxDistinctIatas(tx)
		m := map[string]interface{}{
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"count":             tx.ObservationCount,
			"observer_count":    tx.UniqueObserverCount,
			"observation_count": tx.ObservationCount,
			"latest":            strOrNil(tx.LatestSeen),
			"observer_id":       strOrNil(tx.ObserverID),
			"observer_name":     strOrNil(tx.ObserverName),
			"observer_iata":     strOrNil(tx.ObserverIATA),
			"distinct_iatas":    distinctIatas,
			"path_json":         strOrNil(tx.PathJSON),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"route_type":        intPtrOrNil(tx.RouteType),
			"raw_hex":           strOrNil(tx.RawHex),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"snr":               floatPtrOrNil(tx.SNR),
			"rssi":              floatPtrOrNil(tx.RSSI),
		}
		// resolved_path omitted for grouped view (cold path, not worth SQL round-trip)
		packets[i] = m
	}

	return &PacketResult{Packets: packets, Total: total}
}

// storeTxDistinctIatas (#1189 R2) returns a sorted, deduped list of observer
// IATA codes for a StoreTx, excluding empty values. Returns an empty
// (non-nil) []string when the tx has no IATA'd observations so JSON
// serialization stays consistent across the in-memory store and SQL
// fallback paths (db.go's parseDistinctIatasCSV does the same).
func storeTxDistinctIatas(tx *StoreTx) []string {
	if tx == nil {
		return []string{}
	}
	seen := make(map[string]bool)
	// Include the header observer's IATA (some hot-path StoreTx records the
	// chosen observer fields directly without re-populating Observations).
	if tx.ObserverIATA != "" {
		seen[tx.ObserverIATA] = true
	}
	for _, o := range tx.Observations {
		if o != nil && o.ObserverIATA != "" {
			seen[o.ObserverIATA] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GetStoreStats returns aggregate counts (packet data from memory, node/observer from DB).
func (s *PacketStore) GetStoreStats() (*Stats, error) {
	s.mu.RLock()
	txCount := len(s.packets)
	obsCount := s.totalObs
	s.mu.RUnlock()

	st := &Stats{
		TotalTransmissions: txCount,
		TotalPackets:       txCount,
		TotalObservations:  obsCount,
	}

	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	onlineCutoff := time.Now().UTC().Add(-600 * time.Second).Format(time.RFC3339)
	// observations.timestamp is INTEGER (Unix epoch seconds). Use integer
	// cutoffs so idx_observations_timestamp is used for range scans.
	oneHourAgo := time.Now().Add(-1 * time.Hour).Unix()
	oneDayAgo := time.Now().Add(-24 * time.Hour).Unix()

	// Run node/observer counts and observation counts concurrently (2 queries instead of 5).
	var wg sync.WaitGroup
	var nodeErr, obsErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		nodeErr = s.db.conn.QueryRow(
			`SELECT
				(SELECT COUNT(*) FROM nodes WHERE last_seen > ?) AS active_nodes,
				(SELECT COUNT(*) FROM nodes) AS all_nodes,
				(SELECT COUNT(*) FROM observers WHERE inactive IS NULL OR inactive = 0) AS observers,
				(SELECT COUNT(*) FROM observers WHERE (inactive IS NULL OR inactive = 0) AND last_seen > ?) AS online_observers`,
			sevenDaysAgo, onlineCutoff,
		).Scan(&st.TotalNodes, &st.TotalNodesAllTime, &st.TotalObservers, &st.OnlineObservers)
	}()
	go func() {
		defer wg.Done()
		obsErr = s.db.conn.QueryRow(
			`SELECT
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN timestamp > ? THEN 1 ELSE 0 END), 0)
			FROM observations WHERE timestamp > ?`,
			oneHourAgo, oneDayAgo, oneDayAgo,
		).Scan(&st.PacketsLastHour, &st.PacketsLast24h)
	}()
	wg.Wait()

	if nodeErr != nil {
		return st, nodeErr
	}
	if obsErr != nil {
		return st, obsErr
	}

	return st, nil
}

// GetPerfStoreStats returns packet store statistics for /api/perf.
func (s *PacketStore) GetPerfStoreStats() map[string]interface{} {
	s.mu.RLock()
	totalLoaded := len(s.packets)
	totalObs := s.totalObs
	hashIdx := len(s.byHash)
	txIdx := len(s.byTxID)
	obsIdx := len(s.byObsID)
	observerIdx := len(s.byObserver)
	nodeIdx := len(s.byNode)
	pathHopIdx := len(s.byPathHop)
	ptIdx := len(s.byPayloadType)
	oldestLoaded := s.oldestLoaded
	retentionHours := s.retentionHours
	maxMemoryMB := s.maxMemoryMB
	hotStartupHours := s.hotStartupHours

	// Distinct advert pubkey count — precomputed incrementally (see trackAdvertPubkey).
	advertByObsCount := len(s.advertPubkeys)
	// Snapshot trackedBytes under the lock — ingest writers mutate it under
	// s.mu.Lock, so reading it after RUnlock would be a data race (#8).
	trackedBytes := s.trackedBytes
	s.mu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(trackedBytesToMB(trackedBytes)*10) / 10

	evicted := atomic.LoadInt64(&s.evicted)

	return map[string]interface{}{
		"totalLoaded":            totalLoaded,
		"totalObservations":      totalObs,
		"evicted":                evicted,
		"inserts":                atomic.LoadInt64(&s.insertCount),
		"queries":                atomic.LoadInt64(&s.queryCount),
		"inMemory":               totalLoaded,
		"sqliteOnly":             false,
		"retentionHours":         retentionHours,
		"maxMemoryMB":            maxMemoryMB,
		"oldestLoaded":           oldestLoaded,
		"estimatedMB":            estimatedMB,
		"trackedMB":              trackedMB,
		"hotStartupHours":        hotStartupHours,
		"backgroundLoadComplete": s.backgroundLoadDone.Load(),
		"backgroundLoadFailed":   s.backgroundLoadFailed.Load(),
		"backgroundLoadProgress": s.backgroundLoadProgress.Load(),
		"indexes": map[string]interface{}{
			"byHash":           hashIdx,
			"byTxID":           txIdx,
			"byObsID":          obsIdx,
			"byObserver":       observerIdx,
			"byNode":           nodeIdx,
			"byPathHop":        pathHopIdx,
			"byPayloadType":    ptIdx,
			"advertByObserver": advertByObsCount,
		},
	}
}

// GetCacheStats returns RF cache hit/miss statistics.
func (s *PacketStore) GetCacheStats() map[string]interface{} {
	s.cacheMu.Lock()
	size := len(s.rfCache) + len(s.topoCache) + len(s.hashCache) + len(s.chanCache) + len(s.distCache) + len(s.subpathCache)
	hits := s.cacheHits
	misses := s.cacheMisses
	s.cacheMu.Unlock()

	var hitRate float64
	if hits+misses > 0 {
		hitRate = math.Round(float64(hits)/float64(hits+misses)*1000) / 10
	}

	return map[string]interface{}{
		"size":       size,
		"hits":       hits,
		"misses":     misses,
		"staleHits":  0,
		"recomputes": misses,
		"hitRate":    hitRate,
	}
}

// GetCacheStatsTyped returns cache stats as a typed struct.
func (s *PacketStore) GetCacheStatsTyped() CacheStats {
	s.cacheMu.Lock()
	size := len(s.rfCache) + len(s.topoCache) + len(s.hashCache) + len(s.chanCache) + len(s.distCache) + len(s.subpathCache)
	hits := s.cacheHits
	misses := s.cacheMisses
	s.cacheMu.Unlock()

	var hitRate float64
	if hits+misses > 0 {
		hitRate = math.Round(float64(hits)/float64(hits+misses)*1000) / 10
	}

	return CacheStats{
		Entries:    size,
		Hits:       hits,
		Misses:     misses,
		StaleHits:  0,
		Recomputes: misses,
		HitRate:    hitRate,
	}
}

// cacheInvalidation flags indicate what kind of data changed during ingestion.
// Used by invalidateCachesFor to selectively clear only affected caches.
type cacheInvalidation struct {
	hasNewObservations  bool // new SNR/RSSI data → rfCache
	hasNewPaths         bool // new/changed path data → topoCache, distCache, subpathCache
	hasNewTransmissions bool // new transmissions → hashCache
	hasNewNodes         bool // genuinely new node pubkey discovered → collisionCache
	hasChannelData      bool // new GRP_TXT (payload_type 5) → chanCache
	eviction            bool // data removed → all caches
}

// invalidateCachesFor selectively clears only the analytics caches affected
// by the kind of data that changed. To prevent continuous ingestion from
// defeating caching entirely (issue #533), invalidation is rate-limited:
// if called within invCooldown of the last invalidation, the flags are
// accumulated in pendingInv and applied on the next call after cooldown.
func (s *PacketStore) invalidateCachesFor(inv cacheInvalidation) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Eviction bypasses rate-limiting — data was removed, caches must clear.
	if inv.eviction {
		s.rfCache = make(map[string]*cachedResult)
		s.topoCache = make(map[string]*cachedResult)
		s.hashCache = make(map[string]*cachedResult)
		s.collisionCache = make(map[string]*cachedResult)
		s.chanCache = make(map[string]*cachedResult)
		s.distCache = make(map[string]*cachedResult)
		s.subpathCache = make(map[string]*cachedResult)
		s.channelsCacheMu.Lock()
		s.channelsCacheRes = nil
		s.channelsCacheMu.Unlock()
		s.lastInvalidated = time.Now()
		s.pendingInv = nil
		return
	}

	now := time.Now()
	if now.Sub(s.lastInvalidated) < s.invCooldown {
		// Within cooldown — accumulate dirty flags
		if s.pendingInv == nil {
			s.pendingInv = &cacheInvalidation{}
		}
		s.pendingInv.hasNewObservations = s.pendingInv.hasNewObservations || inv.hasNewObservations
		s.pendingInv.hasNewPaths = s.pendingInv.hasNewPaths || inv.hasNewPaths
		s.pendingInv.hasNewTransmissions = s.pendingInv.hasNewTransmissions || inv.hasNewTransmissions
		s.pendingInv.hasNewNodes = s.pendingInv.hasNewNodes || inv.hasNewNodes
		s.pendingInv.hasChannelData = s.pendingInv.hasChannelData || inv.hasChannelData
		return
	}

	// Cooldown expired — merge any pending flags and apply
	if s.pendingInv != nil {
		inv.hasNewObservations = inv.hasNewObservations || s.pendingInv.hasNewObservations
		inv.hasNewPaths = inv.hasNewPaths || s.pendingInv.hasNewPaths
		inv.hasNewTransmissions = inv.hasNewTransmissions || s.pendingInv.hasNewTransmissions
		inv.hasNewNodes = inv.hasNewNodes || s.pendingInv.hasNewNodes
		inv.hasChannelData = inv.hasChannelData || s.pendingInv.hasChannelData
		s.pendingInv = nil
	}

	s.applyCacheInvalidation(inv)
	s.lastInvalidated = now
}

// applyCacheInvalidation performs the actual cache clearing. Must be called
// with cacheMu held.
func (s *PacketStore) applyCacheInvalidation(inv cacheInvalidation) {
	if inv.hasNewObservations {
		s.rfCache = make(map[string]*cachedResult)
	}
	if inv.hasNewPaths {
		s.topoCache = make(map[string]*cachedResult)
		s.distCache = make(map[string]*cachedResult)
		s.subpathCache = make(map[string]*cachedResult)
	}
	if inv.hasNewTransmissions {
		s.hashCache = make(map[string]*cachedResult)
	}
	if inv.hasNewNodes {
		s.collisionCache = make(map[string]*cachedResult)
	}
	if inv.hasChannelData {
		s.chanCache = make(map[string]*cachedResult)
		s.channelsCacheMu.Lock()
		s.channelsCacheRes = nil
		s.channelsCacheMu.Unlock()
	}
}

// GetPerfStoreStatsTyped returns packet store stats as a typed struct.
func (s *PacketStore) GetPerfStoreStatsTyped() PerfPacketStoreStats {
	s.mu.RLock()
	totalLoaded := len(s.packets)
	totalObs := s.totalObs
	hashIdx := len(s.byHash)
	observerIdx := len(s.byObserver)
	nodeIdx := len(s.byNode)

	advertByObsCount := len(s.advertPubkeys)
	// Snapshot trackedBytes under the lock — ingest writers mutate it under
	// s.mu.Lock, so reading it after RUnlock would be a data race (#8). This
	// aligns GetPerfStoreStatsTyped with the snapshot-under-lock pattern used
	// by GetStoreStats / GetPerfStoreStats.
	trackedBytes := s.trackedBytes
	s.mu.RUnlock()

	estimatedMB := math.Round(s.estimatedMemoryMB()*10) / 10
	trackedMB := math.Round(trackedBytesToMB(trackedBytes)*10) / 10

	var avgBytesPerPacket int64
	if totalLoaded > 0 {
		avgBytesPerPacket = trackedBytes / int64(totalLoaded)
	}

	return PerfPacketStoreStats{
		TotalLoaded:       totalLoaded,
		TotalObservations: totalObs,
		Evicted:           int(atomic.LoadInt64(&s.evicted)),
		Inserts:           atomic.LoadInt64(&s.insertCount),
		Queries:           atomic.LoadInt64(&s.queryCount),
		InMemory:          totalLoaded,
		SqliteOnly:        false,
		MaxPackets:        2386092,
		EstimatedMB:       estimatedMB,
		TrackedMB:         trackedMB,
		AvgBytesPerPacket: avgBytesPerPacket,
		MaxMB:             s.maxMemoryMB,
		Indexes: PacketStoreIndexes{
			ByHash:           hashIdx,
			ByObserver:       observerIdx,
			ByNode:           nodeIdx,
			AdvertByObserver: advertByObsCount,
		},
		HotStartupHours:        s.hotStartupHours,
		BackgroundLoadComplete: s.backgroundLoadDone.Load(),
		BackgroundLoadFailed:   s.backgroundLoadFailed.Load(),
		BackgroundLoadProgress: s.backgroundLoadProgress.Load(),
	}
}

// GetTransmissionByID returns a transmission by its DB ID, formatted as a map.
func (s *PacketStore) GetTransmissionByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byTxID[id]
	if tx == nil {
		return nil
	}
	return s.txToMapWithRP(tx, true)
}

// GetPacketByHash returns a transmission by content hash.
func (s *PacketStore) GetPacketByHash(hash string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return nil
	}
	return s.txToMapWithRP(tx, true)
}

// GetPacketByID returns an observation (enriched with transmission fields) by observation ID.
func (s *PacketStore) GetPacketByID(id int) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	obs := s.byObsID[id]
	if obs == nil {
		return nil
	}
	return s.enrichObs(obs)
}

// GetObservationsForHash returns all observations for a hash, enriched with transmission fields.
func (s *PacketStore) GetObservationsForHash(hash string) []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx := s.byHash[strings.ToLower(hash)]
	if tx == nil {
		return []map[string]interface{}{}
	}

	result := make([]map[string]interface{}, 0, len(tx.Observations))
	for _, obs := range tx.Observations {
		result = append(result, s.enrichObs(obs))
	}
	return result
}

// GetTimestamps returns transmission first_seen timestamps after since, in ASC order.
func (s *PacketStore) GetTimestamps(since string) []string {
	// Snapshot the slice header under a brief RLock (O(1)). Elements are
	// *StoreTx pointers that are never mutated after ingest, so reading them
	// outside the lock is safe — same pattern as GetAnalyticsSubpathsBulk's
	// snapPackets. Avoids holding RLock for the full O(n) scan, which would
	// block the ingest writer and cascade delays to other readers.
	s.mu.RLock()
	snap := s.packets
	s.mu.RUnlock()

	// packets sorted oldest-first — scan from tail until we reach items older than since
	var result []string
	for i := len(snap) - 1; i >= 0; i-- {
		tx := snap[i]
		if tx.FirstSeen <= since {
			break
		}
		result = append(result, tx.FirstSeen)
	}
	// result is currently newest-first; reverse to return ASC order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// QueryMultiNodePackets filters packets matching any of the given pubkeys.
func (s *PacketStore) QueryMultiNodePackets(pubkeys []string, limit, offset int, order, since, until string) *PacketResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(pubkeys) == 0 {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: 0}
	}
	if limit <= 0 {
		limit = 50
	}

	resolved := make([]string, len(pubkeys))
	for i, pk := range pubkeys {
		resolved[i] = s.db.resolveNodePubkey(pk)
	}

	// Use byNode index instead of scanning all packets (O(indexed) vs O(all×pubkeys×json)).
	hashSet := make(map[string]bool)
	var filtered []*StoreTx
	for _, pk := range resolved {
		for _, tx := range s.byNode[pk] {
			if hashSet[tx.Hash] {
				continue
			}
			if since != "" && tx.FirstSeen < since {
				continue
			}
			if until != "" && tx.FirstSeen > until {
				continue
			}
			hashSet[tx.Hash] = true
			filtered = append(filtered, tx)
		}
	}
	// Sort oldest-first to match pagination expectations (same as s.packets order).
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].FirstSeen < filtered[j].FirstSeen
	})

	total := len(filtered)

	// filtered is oldest-first (built by iterating s.packets forward).
	// Apply same DESC/ASC pagination logic as QueryPackets.
	if offset >= total {
		return &PacketResult{Packets: []map[string]interface{}{}, Total: total}
	}
	pageSize := limit
	if offset+pageSize > total {
		pageSize = total - offset
	}

	packets := make([]map[string]interface{}, 0, pageSize)
	if order == "ASC" {
		for _, tx := range filtered[offset : offset+pageSize] {
			packets = append(packets, s.txToMapWithRP(tx))
		}
	} else {
		endIdx := total - offset
		startIdx := endIdx - pageSize
		if startIdx < 0 {
			startIdx = 0
		}
		for i := endIdx - 1; i >= startIdx; i-- {
			packets = append(packets, s.txToMapWithRP(filtered[i]))
		}
	}
	return &PacketResult{Packets: packets, Total: total}
}

// IngestNewFromDB loads new transmissions from SQLite into memory and returns
// broadcast-ready maps plus the new max transmission ID.
func (s *PacketStore) IngestNewFromDB(sinceID, limit int) ([]map[string]interface{}, int) {
	if limit <= 0 {
		limit = 100
	}

	// NOTE: The SQL query intentionally does NOT select resolved_path from the DB.
	// New ingests always resolve fresh using the current prefix map and neighbor graph.
	// On restart, Load() handles reading persisted resolved_path values. (review item #7)
	var querySQL string
	obsRHCol := ""
	if s.db.hasObsRawHex {
		obsRHCol = ", o.raw_hex"
	}
	if s.db.isV3 {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRHCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	} else {
		querySQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
				t.payload_type, t.payload_version, t.decoded_json,
				o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRHCol + `
			FROM transmissions t
			LEFT JOIN observations o ON o.transmission_id = t.id
			LEFT JOIN observers obs ON obs.id = o.observer_id
			WHERE t.id > ?
			ORDER BY t.id ASC, o.timestamp DESC`
	}

	rows, err := s.db.conn.Query(querySQL, sinceID)
	if err != nil {
		log.Printf("[store] ingest query error: %v", err)
		return nil, sinceID
	}
	defer rows.Close()

	// Scan into temp structures
	type tempRow struct {
		txID                                                               int
		rawHex, hash, firstSeen, decodedJSON                               string
		routeType, payloadType                                             *int
		obsID                                                              *int
		observerID, observerName, observerIATA, direction, pathJSON, obsTS string
		obsRawHex                                                          string
		snr, rssi                                                          *float64
		score                                                              *int
	}

	var tempRows []tempRow
	txCount := 0
	lastTxID := sinceID

	for rows.Next() {
		var txID int
		var rawHex, hash, firstSeen, decodedJSON sql.NullString
		var routeType, payloadType, payloadVersion sql.NullInt64
		var obsIDVal sql.NullInt64
		var observerID, observerName, observerIATA, direction, pathJSON, obsTimestamp sql.NullString
		var snrVal, rssiVal sql.NullFloat64
		var scoreVal sql.NullInt64
		var obsRawHex sql.NullString

		scanArgs2 := []interface{}{&txID, &rawHex, &hash, &firstSeen, &routeType, &payloadType,
			&payloadVersion, &decodedJSON,
			&obsIDVal, &observerID, &observerName, &observerIATA, &direction,
			&snrVal, &rssiVal, &scoreVal, &pathJSON, &obsTimestamp}
		if s.db.hasObsRawHex {
			scanArgs2 = append(scanArgs2, &obsRawHex)
		}
		if err := rows.Scan(scanArgs2...); err != nil {
			continue
		}

		if txID != lastTxID {
			txCount++
			if txCount > limit {
				break
			}
			lastTxID = txID
		}

		tr := tempRow{
			txID:         txID,
			rawHex:       nullStrVal(rawHex),
			hash:         nullStrVal(hash),
			firstSeen:    nullStrVal(firstSeen),
			decodedJSON:  nullStrVal(decodedJSON),
			routeType:    nullIntPtr(routeType),
			payloadType:  nullIntPtr(payloadType),
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			observerIATA: nullStrVal(observerIATA),
			direction:    nullStrVal(direction),
			pathJSON:     nullStrVal(pathJSON),
			obsTS:        nullStrVal(obsTimestamp),
			obsRawHex:    nullStrVal(obsRawHex),
			snr:          nullFloatPtr(snrVal),
			rssi:         nullFloatPtr(rssiVal),
			score:        nullIntPtr(scoreVal),
		}
		if obsIDVal.Valid {
			oid := int(obsIDVal.Int64)
			tr.obsID = &oid
		}
		tempRows = append(tempRows, tr)
	}

	if len(tempRows) == 0 {
		return nil, sinceID
	}

	// Refresh the node cache BEFORE acquiring s.mu so a 30s cache-miss does
	// not run a full-table SELECT (getAllNodes) while the store write lock is
	// held, freezing all readers during the SQLite scan.
	// getCachedNodesAndPM only takes cacheMu (never s.mu), so this is safe.
	_, cachedPM := s.getCachedNodesAndPM()

	// Now lock and merge into store
	s.mu.Lock()
	defer s.mu.Unlock()

	newMaxID := sinceID
	broadcastTxs := make(map[int]*StoreTx) // track new transmissions for broadcast
	hasNewNodes := false                   // track genuinely new node pubkeys
	var broadcastOrder []int

	// Cache is already warm from the pre-lock call above; this returns instantly.
	_, cachedPM = s.getCachedNodesAndPM()
	// Hoist atomic graph.Load() out of the per-row loop too (PR #1208
	// carmack #1) — one Load per ingest call, not one per row.
	cachedGraph := s.graph.Load()

	// Decode-window tracking: resolved pubkeys per-tx for touchRelayLastSeen,
	// and resolved paths per-obs for broadcast/persist.
	var broadcastRP map[int][]*string        // obsID → resolved path (for broadcast/persist)
	allResolvedPKs := make(map[int][]string) // txID → all resolved pubkeys (for touchRelayLastSeen)

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for _, r := range tempRows {
		if r.txID > newMaxID {
			newMaxID = r.txID
		}

		tx := s.byHash[r.hash]
		if tx == nil {
			tx = &StoreTx{
				ID:          r.txID,
				RawHex:      r.rawHex,
				Hash:        r.hash,
				FirstSeen:   r.firstSeen,
				LatestSeen:  r.firstSeen,
				RouteType:   r.routeType,
				PayloadType: r.payloadType,
				DecodedJSON: r.decodedJSON,
				obsKeys:     make(map[string]bool),
				observerSet: make(map[string]bool),
			}
			s.byHash[r.hash] = tx
			s.packets = append(s.packets, tx) // oldest-first; new items go to tail
			s.byTxID[r.txID] = tx
			if r.txID > s.maxTxID {
				s.maxTxID = r.txID
			}
			if s.indexByNode(tx) {
				hasNewNodes = true
			}
			if tx.PayloadType != nil {
				pt := *tx.PayloadType
				// Append to maintain oldest-first order (matches Load ordering)
				// so GetChannelMessages reverse iteration stays correct
				s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
			}
			s.trackAdvertPubkey(tx)
			s.trackedBytes += estimateStoreTxBytes(tx)
			atomic.AddInt64(&s.insertCount, 1)

			if _, exists := broadcastTxs[r.txID]; !exists {
				broadcastTxs[r.txID] = tx
				broadcastOrder = append(broadcastOrder, r.txID)
			}
		}

		if r.obsID != nil {
			oid := *r.obsID
			// Dedup (O(1) map lookup)
			dk := r.observerID + "|" + r.pathJSON
			if tx.obsKeys == nil {
				tx.obsKeys = make(map[string]bool)
				tx.observerSet = make(map[string]bool)
			}
			if tx.obsKeys[dk] {
				continue
			}

			obs := &StoreObs{
				ID:             oid,
				TransmissionID: r.txID,
				ObserverID:     r.observerID,
				ObserverName:   r.observerName,
				ObserverIATA:   r.observerIATA,
				Direction:      r.direction,
				SNR:            r.snr,
				RSSI:           r.rssi,
				Score:          r.score,
				PathJSON:       r.pathJSON,
				RawHex:         r.obsRawHex,
				Timestamp:      normalizeTimestamp(r.obsTS),
			}

			// Resolve path at ingest time using neighbor graph — decode-window discipline:
			// decode once, feed consumers, never store on struct.
			var resolvedPubkeys []string
			var rpForBroadcast []*string
			if r.pathJSON != "" && r.pathJSON != "[]" && cachedPM != nil {
				rpForBroadcast = resolvePathForObs(r.pathJSON, r.observerID, tx, cachedPM, cachedGraph)
				resolvedPubkeys = extractResolvedPubkeys(rpForBroadcast)
				// Feed decode-window consumers: addToByNode + resolvedPubkeyIndex
				for _, pk := range resolvedPubkeys {
					s.addToByNode(tx, pk)
				}
				s.addToResolvedPubkeyIndex(tx.ID, resolvedPubkeys)
				// byPathHop resolved-key entries (#1164: helper invalidates relay stats cache).
				s.addResolvedPubkeysToPathHopIndex(tx, resolvedPubkeys, hopsSeen)
			}
			// Stash rpForBroadcast for later broadcast/persist (keyed by obs ID)
			if rpForBroadcast != nil {
				if broadcastRP == nil {
					broadcastRP = make(map[int][]*string)
				}
				broadcastRP[*r.obsID] = rpForBroadcast
			}
			// Collect resolved pubkeys per-tx for touchRelayLastSeen
			if len(resolvedPubkeys) > 0 {
				allResolvedPKs[r.txID] = append(allResolvedPKs[r.txID], resolvedPubkeys...)
			}

			tx.Observations = append(tx.Observations, obs)
			tx.obsKeys[dk] = true
			if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
				tx.observerSet[obs.ObserverID] = true
				tx.UniqueObserverCount++
			}
			tx.ObservationCount++
			if obs.Timestamp > tx.LatestSeen {
				tx.LatestSeen = obs.Timestamp
			}
			s.byObsID[oid] = obs
			if oid > s.maxObsID {
				s.maxObsID = oid
			}
			if r.observerID != "" {
				s.byObserver[r.observerID] = append(s.byObserver[r.observerID], obs)
			}
			s.totalObs++
			s.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	// Pick best observation for new transmissions
	for _, tx := range broadcastTxs {
		pickBestObservation(tx)
	}

	// Phase 2 of #660: update last_seen in DB for relay nodes seen in resolved_path.
	now := time.Now()
	for txID := range broadcastTxs {
		if pks, ok := allResolvedPKs[txID]; ok {
			s.touchRelayLastSeen(pks, now)
		}
	}

	// Incrementally update precomputed subpath index with new transmissions
	for _, tx := range broadcastTxs {
		if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
			s.spTotalPaths++
		}
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	if len(broadcastTxs) > 0 {
		s.refreshSpIndexSnap()
	}
	if len(broadcastTxs) > 0 {
		s.invalidateRelayStatsCache()
	}

	// Incrementally update precomputed distance index with new transmissions
	if len(broadcastTxs) > 0 {
		allNodes, pm := s.getCachedNodesAndPM()
		nodeByPk := make(map[string]*nodeInfo, len(allNodes))
		repeaterSet := make(map[string]bool)
		for i := range allNodes {
			n := &allNodes[i]
			nodeByPk[n.PublicKey] = n
			if strings.Contains(strings.ToLower(n.Role), "repeater") {
				repeaterSet[n.PublicKey] = true
			}
		}
		// Per-tx hop resolver: cache reused across txs, context rebound per
		// tx via setContext (#1197 perf fix).
		resolveHop, setContext := s.hopResolverPerTx(pm)
		for _, tx := range broadcastTxs {
			// Per-tx context (sender + observer + unambiguous-prefix anchors)
			// so resolveWithContext tiers 1 and 2 light up. See #1197.
			setContext(buildHopContextPubkeys(tx, pm))
			txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
			if len(txHops) > 0 {
				s.distHops = append(s.distHops, txHops...)
			}
			if txPath != nil {
				s.distPaths = append(s.distPaths, *txPath)
			}
		}
	}

	// Build broadcast maps (same shape as Node.js WS broadcast), one per observation.
	result := make([]map[string]interface{}, 0, len(broadcastOrder))
	for _, txID := range broadcastOrder {
		tx := broadcastTxs[txID]
		// Build decoded object with header.payloadTypeName for live.js
		decoded := map[string]interface{}{
			"header": map[string]interface{}{
				"payloadTypeName": resolvePayloadTypeName(tx.PayloadType),
			},
		}
		if tx.DecodedJSON != "" {
			var payload map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &payload) == nil {
				decoded["payload"] = payload
			}
		}
		// For TRACE packets, decode the full packet to include path.hopsCompleted
		// so the frontend can distinguish completed vs remaining hops (#683).
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE && tx.RawHex != "" {
			if dp, err := DecodePacket(tx.RawHex, false); err == nil {
				decoded["path"] = dp.Path
			}
		}
		for _, obs := range tx.Observations {
			// Build the nested packet object (packets.js checks m.data.packet)
			pkt := map[string]interface{}{
				"id":                tx.ID,
				"raw_hex":           strOrNil(tx.RawHex),
				"hash":              strOrNil(tx.Hash),
				"first_seen":        strOrNil(tx.FirstSeen),
				"timestamp":         strOrNil(tx.FirstSeen),
				"route_type":        intPtrOrNil(tx.RouteType),
				"payload_type":      intPtrOrNil(tx.PayloadType),
				"decoded_json":      strOrNil(tx.DecodedJSON),
				"observer_id":       strOrNil(obs.ObserverID),
				"observer_name":     strOrNil(obs.ObserverName),
				"snr":               floatPtrOrNil(obs.SNR),
				"rssi":              floatPtrOrNil(obs.RSSI),
				"path_json":         strOrNil(obs.PathJSON),
				"direction":         strOrNil(obs.Direction),
				"observation_count": tx.ObservationCount,
			}
			// Use decode-window resolved path for broadcast (never from struct)
			if broadcastRP != nil {
				if rp, ok := broadcastRP[obs.ID]; ok && rp != nil {
					pkt["resolved_path"] = rp
				}
			}
			// Broadcast map: top-level fields for live.js + nested packet for packets.js
			broadcastMap := make(map[string]interface{}, len(pkt)+2)
			for k, v := range pkt {
				broadcastMap[k] = v
			}
			broadcastMap["decoded"] = decoded
			broadcastMap["packet"] = pkt
			result = append(result, broadcastMap)
		}
	}

	// Targeted cache invalidation: only clear caches affected by the ingested
	// data instead of wiping everything on every cycle (fixes #375).
	if len(result) > 0 {
		inv := cacheInvalidation{
			hasNewTransmissions: len(broadcastTxs) > 0,
			hasNewNodes:         hasNewNodes,
		}
		for _, tx := range broadcastTxs {
			if len(tx.Observations) > 0 {
				inv.hasNewObservations = true
			}
			if tx.PayloadType != nil && *tx.PayloadType == 5 {
				inv.hasChannelData = true
			}
			if tx.PathJSON != "" {
				inv.hasNewPaths = true
			}
			if inv.hasNewObservations && inv.hasChannelData && inv.hasNewPaths {
				break // all flags set, no need to continue
			}
		}
		s.invalidateCachesFor(inv)
	}

		// Per #1287 (Option 4): the server NEVER writes to the DB and
	// NEVER mutates the in-memory neighbor graph incrementally. The
	// ingestor owns neighbor_edges; recompNeighborGraph re-reads the
	// snapshot every 60s and atomic-swaps it into s.graph. We also no
	// longer persist resolved_path here — the ingestor (which already
	// sees every observation) owns that write too.
	_ = broadcastRP // resolved path is still computed in-memory (above) for live broadcast; no SQL write.

	return result, newMaxID
}

// IngestNewObservations loads new observations for transmissions already in the
// store. This catches observations that arrive after IngestNewFromDB has already
// advanced past the transmission's ID (fixes #174).
func (s *PacketStore) IngestNewObservations(sinceObsID, limit int) []map[string]interface{} {
	if limit <= 0 {
		limit = 500
	}

	var querySQL string
	obsRHCol2 := ""
	if s.db.hasObsRawHex {
		obsRHCol2 = ", o.raw_hex"
	}
	if s.db.isV3 {
		querySQL = `SELECT o.id, o.transmission_id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRHCol2 + `
			FROM observations o
			LEFT JOIN observers obs ON obs.rowid = o.observer_idx
			WHERE o.id > ?
			ORDER BY o.id ASC
			LIMIT ?`
	} else {
		querySQL = `SELECT o.id, o.transmission_id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
				o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRHCol2 + `
			FROM observations o
			LEFT JOIN observers obs ON obs.id = o.observer_id
			WHERE o.id > ?
			ORDER BY o.id ASC
			LIMIT ?`
	}

	rows, err := s.db.conn.Query(querySQL, sinceObsID, limit)
	if err != nil {
		log.Printf("[store] ingest observations query error: %v", err)
		return nil
	}
	defer rows.Close()

	type obsRow struct {
		obsID        int
		txID         int
		observerID   string
		observerName string
		observerIATA string
		direction    string
		snr, rssi    *float64
		score        *int
		pathJSON     string
		rawHex       string
		timestamp    string
	}

	var obsRows []obsRow
	for rows.Next() {
		var oid, txID int
		var observerID, observerName, observerIATA, direction, pathJSON, ts sql.NullString
		var snr, rssi sql.NullFloat64
		var score sql.NullInt64
		var obsRawHex sql.NullString

		scanArgs3 := []interface{}{&oid, &txID, &observerID, &observerName, &observerIATA, &direction,
			&snr, &rssi, &score, &pathJSON, &ts}
		if s.db.hasObsRawHex {
			scanArgs3 = append(scanArgs3, &obsRawHex)
		}
		if err := rows.Scan(scanArgs3...); err != nil {
			continue
		}

		obsRows = append(obsRows, obsRow{
			obsID:        oid,
			txID:         txID,
			observerID:   nullStrVal(observerID),
			observerName: nullStrVal(observerName),
			observerIATA: nullStrVal(observerIATA),
			direction:    nullStrVal(direction),
			snr:          nullFloatPtr(snr),
			rssi:         nullFloatPtr(rssi),
			score:        nullIntPtr(score),
			pathJSON:     nullStrVal(pathJSON),
			rawHex:       nullStrVal(obsRawHex),
			timestamp:    nullStrVal(ts),
		})
	}

	if len(obsRows) == 0 {
		return nil
	}

	// Pre-warm node cache before acquiring s.mu — same fix as IngestNewFromDB.
	// A 30s cache-miss SELECT under the write lock freezes all readers.
	_, pm := s.getCachedNodesAndPM()

	s.mu.Lock()
	defer s.mu.Unlock()

	updatedTxs := make(map[int]*StoreTx)
	broadcastMaps := make([]map[string]interface{}, 0, len(obsRows))
	// Track newly created observations for persistence — only these should be
	// persisted, not all observations of each updated tx (fixes edge count inflation).
	var newObs []*StoreObs
	var obsRPMap map[int][]*string // obsID → resolved path (decode-window)

	// Cache is already warm from the pre-lock call above; this returns instantly.
	_, pm = s.getCachedNodesAndPM()
	graphRef := s.graph.Load()

	hopsSeen := make(map[string]bool) // reused across observations; cleared per use

	for _, r := range obsRows {
		// Already ingested (e.g. by IngestNewFromDB in same cycle)
		if _, exists := s.byObsID[r.obsID]; exists {
			continue
		}

		tx := s.byTxID[r.txID]
		if tx == nil {
			continue // transmission not yet in store
		}

		// Dedup by observer + path (O(1) map lookup)
		dk := r.observerID + "|" + r.pathJSON
		if tx.obsKeys == nil {
			tx.obsKeys = make(map[string]bool)
			tx.observerSet = make(map[string]bool)
		}
		if tx.obsKeys[dk] {
			continue
		}

		obs := &StoreObs{
			ID:             r.obsID,
			TransmissionID: r.txID,
			ObserverID:     r.observerID,
			ObserverName:   r.observerName,
			ObserverIATA:   r.observerIATA,
			Direction:      r.direction,
			SNR:            r.snr,
			RSSI:           r.rssi,
			Score:          r.score,
			PathJSON:       r.pathJSON,
			RawHex:         r.rawHex,
			Timestamp:      normalizeTimestamp(r.timestamp),
		}

		// Resolve path at ingest time for late-arriving observations (review item #2).
		// Decode-window discipline: decode, feed consumers, don't store on struct.
		var obsResolvedPath []*string
		if r.pathJSON != "" && r.pathJSON != "[]" {
			if pm != nil {
				obsResolvedPath = resolvePathForObs(r.pathJSON, r.observerID, tx, pm, graphRef)
				pks := extractResolvedPubkeys(obsResolvedPath)
				for _, pk := range pks {
					s.addToByNode(tx, pk)
				}
				s.addToResolvedPubkeyIndex(tx.ID, pks)
				// byPathHop resolved-key entries (#1164: helper invalidates relay stats cache).
				s.addResolvedPubkeysToPathHopIndex(tx, pks, hopsSeen)
			}
		}
		// Stash for broadcast/persist
		if obsResolvedPath != nil {
			if obsRPMap == nil {
				obsRPMap = make(map[int][]*string)
			}
			obsRPMap[r.obsID] = obsResolvedPath
		}

		tx.Observations = append(tx.Observations, obs)
		tx.obsKeys[dk] = true
		if obs.ObserverID != "" && !tx.observerSet[obs.ObserverID] {
			tx.observerSet[obs.ObserverID] = true
			tx.UniqueObserverCount++
		}
		tx.ObservationCount++
		newObs = append(newObs, obs)
		if obs.Timestamp > tx.LatestSeen {
			tx.LatestSeen = obs.Timestamp
		}
		s.byObsID[r.obsID] = obs
		if r.obsID > s.maxObsID {
			s.maxObsID = r.obsID
		}
		if r.observerID != "" {
			s.byObserver[r.observerID] = append(s.byObserver[r.observerID], obs)
		}
		s.totalObs++
		s.trackedBytes += estimateStoreObsBytes(obs)
		updatedTxs[r.txID] = tx

		decoded := map[string]interface{}{
			"header": map[string]interface{}{
				"payloadTypeName": resolvePayloadTypeName(tx.PayloadType),
			},
		}
		if tx.DecodedJSON != "" {
			var payload map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &payload) == nil {
				decoded["payload"] = payload
			}
		}
		// For TRACE packets, decode the full packet to include path.hopsCompleted
		// so the frontend can distinguish completed vs remaining hops (#683).
		if tx.PayloadType != nil && *tx.PayloadType == PayloadTRACE && tx.RawHex != "" {
			if dp, err := DecodePacket(tx.RawHex, false); err == nil {
				decoded["path"] = dp.Path
			}
		}

		pkt := map[string]interface{}{
			"id":                tx.ID,
			"raw_hex":           strOrNil(tx.RawHex),
			"hash":              strOrNil(tx.Hash),
			"first_seen":        strOrNil(tx.FirstSeen),
			"timestamp":         strOrNil(tx.FirstSeen),
			"route_type":        intPtrOrNil(tx.RouteType),
			"payload_type":      intPtrOrNil(tx.PayloadType),
			"decoded_json":      strOrNil(tx.DecodedJSON),
			"observer_id":       strOrNil(obs.ObserverID),
			"observer_name":     strOrNil(obs.ObserverName),
			"snr":               floatPtrOrNil(obs.SNR),
			"rssi":              floatPtrOrNil(obs.RSSI),
			"path_json":         strOrNil(obs.PathJSON),
			"direction":         strOrNil(obs.Direction),
			"observation_count": tx.ObservationCount,
		}
		// Use decode-window resolved path for broadcast
		if obsRPMap != nil {
			if rp, ok := obsRPMap[obs.ID]; ok && rp != nil {
				pkt["resolved_path"] = rp
			}
		}
		broadcastMap := make(map[string]interface{}, len(pkt)+2)
		for k, v := range pkt {
			broadcastMap[k] = v
		}
		broadcastMap["decoded"] = decoded
		broadcastMap["packet"] = pkt
		broadcastMaps = append(broadcastMaps, broadcastMap)
	}

	// Re-pick best observation for updated transmissions and update subpath index
	// if the path changed.
	oldPaths := make(map[int]string, len(updatedTxs))
	for txID, tx := range updatedTxs {
		oldPaths[txID] = tx.PathJSON
	}
	for _, tx := range updatedTxs {
		pickBestObservation(tx)
	}
	pathHopMutated := false
	for txID, tx := range updatedTxs {
		if tx.PathJSON != oldPaths[txID] {
			// Path changed — remove old subpaths, add new ones.
			oldHops := parsePathJSON(oldPaths[txID])
			if len(oldHops) >= 2 {
				// Temporarily set parsedPath to old hops for removal.
				saved, savedFlag := tx.parsedPath, tx.pathParsed
				tx.parsedPath, tx.pathParsed = oldHops, true
				if removeTxFromSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
					s.spTotalPaths--
				}
				tx.parsedPath, tx.pathParsed = saved, savedFlag
			}
			// Remove old path-hop index entries using old hops.
			// Resolved pubkey entries are managed via resolvedPubkeyIndex, not byPathHop.
			if len(oldHops) > 0 {
				saved, savedFlag := tx.parsedPath, tx.pathParsed
				tx.parsedPath, tx.pathParsed = oldHops, true
				removeTxFromPathHopIndex(s.byPathHop, tx)
				tx.parsedPath, tx.pathParsed = saved, savedFlag
			}
			// pickBestObservation already set pathParsed=false so
			// addTxToSubpathIndex will re-parse the new path.
			if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
				s.spTotalPaths++
			}
			addTxToPathHopIndex(s.byPathHop, tx)
			// #1164: coalesce — one invalidate after the loop, not per-tx.
			pathHopMutated = true
		}
	}
	if pathHopMutated {
		s.refreshSpIndexSnap()
		s.invalidateRelayStatsCache()
	}

	// Check if any paths changed (used for distance update and cache invalidation).
	hasPathChanges := false
	var changedTxs []*StoreTx
	for txID, tx := range updatedTxs {
		if tx.PathJSON != oldPaths[txID] {
			hasPathChanges = true
			changedTxs = append(changedTxs, tx)
		}
	}
	if len(changedTxs) > 0 {
		s.updateDistanceIndexForTxs(changedTxs)
	}

	if len(updatedTxs) > 0 {
		// Targeted cache invalidation: new observations always affect RF
		// analytics; topology/distance/subpath caches only if paths changed.
		// Channel and hash caches are unaffected by observation-only ingestion.
		s.invalidateCachesFor(cacheInvalidation{
			hasNewObservations: true,
			hasNewPaths:        hasPathChanges,
		})
	}

	// Per #1287 (Option 4): server never writes to the DB and never
	// mutates the in-memory neighbor graph incrementally — the
	// ingestor owns both. recompNeighborGraph re-reads the snapshot
	// every 60s and atomic-swaps into s.graph.
	_ = obsRPMap // resolved path stays in-memory for broadcast; no SQL write.
	_ = newObs
	_ = pm
	_ = graphRef

	return broadcastMaps
}

// MaxTransmissionID returns the highest transmission ID in the store.
func (s *PacketStore) MaxTransmissionID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxTxID
}

// MaxObservationID returns the highest observation ID in the store.
func (s *PacketStore) MaxObservationID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxObsID
}

// --- Internal filter/query helpers ---

// filterPackets applies PacketQuery filters to the in-memory packet list.
func (s *PacketStore) filterPackets(q PacketQuery) []*StoreTx {
	// Fast path: single-key index lookups
	if q.Hash != "" && q.Type == nil && q.Route == nil && q.Observer == "" &&
		q.Region == "" && q.Area == "" && q.Node == "" && q.Channel == "" && q.Since == "" && q.Until == "" {
		h := strings.ToLower(q.Hash)
		tx := s.byHash[h]
		if tx == nil {
			return nil
		}
		return []*StoreTx{tx}
	}
	if q.Observer != "" && q.Type == nil && q.Route == nil &&
		q.Region == "" && q.Area == "" && q.Node == "" && q.Channel == "" && q.Hash == "" && q.Since == "" && q.Until == "" {
		return s.transmissionsForObserver(q.Observer, nil)
	}

	// Pre-compute filter parameters outside the hot loop.
	var (
		filterType  int
		hasType     bool
		filterRoute int
		hasRoute    bool
		filterHash  string
		hasSince    = q.Since != ""
		hasUntil    = q.Until != ""
	)
	if q.Type != nil {
		hasType = true
		filterType = *q.Type
	}
	if q.Route != nil {
		hasRoute = true
		filterRoute = *q.Route
	}
	if q.Hash != "" {
		filterHash = strings.ToLower(q.Hash)
	}
	filterChannel := q.Channel

	// Pre-compute observer set for observer filter.
	var observerSet map[string]bool
	if q.Observer != "" {
		ids := strings.Split(q.Observer, ",")
		observerSet = make(map[string]bool, len(ids))
		for _, id := range ids {
			observerSet[strings.TrimSpace(id)] = true
		}
	}

	// Pre-compute region observer set. Prefer a set the caller already
	// resolved before taking s.mu (review item #2) so we never run the
	// GetObserverIdsForRegion SQL query under a read lock.
	var regionObservers map[string]bool
	if q.Region != "" {
		if q.regionObserversResolvedSet {
			regionObservers = q.regionObserversResolved
		} else {
			regionObservers = s.resolveRegionObservers(q.Region)
		}
		if len(regionObservers) == 0 {
			return nil
		}
	}

	// Pre-compute area node set.
	var areaNodes map[string]bool
	if q.Area != "" {
		areaNodes = s.resolveAreaNodes(q.Area)
	}

	// Pre-compute node filter parameters.
	var nodePK string
	var nodeHashSet map[string]bool
	hasNode := q.Node != ""
	if hasNode {
		nodePK = s.db.resolveNodePubkey(q.Node)
		indexed := s.byNode[nodePK]
		nodeHashSet = make(map[string]bool, len(indexed))
		for _, tx := range indexed {
			nodeHashSet[tx.Hash] = true
		}
	}

	// Determine the source slice. Use index-based source when only node
	// filter is active and an index exists.
	source := s.packets
	if hasNode && !hasType && !hasRoute && q.Observer == "" &&
		filterHash == "" && !hasSince && !hasUntil && q.Region == "" && q.Area == "" && filterChannel == "" {
		if indexed, ok := s.byNode[nodePK]; ok {
			return indexed
		}
	}
	// Single-pass filter: apply all predicates in one scan.
	results := filterTxSlice(source, func(tx *StoreTx) bool {
		// Data integrity: exclude legacy rows missing hash or timestamp (#871)
		if tx.Hash == "" || tx.FirstSeen == "" {
			return false
		}
		if hasType && (tx.PayloadType == nil || *tx.PayloadType != filterType) {
			return false
		}
		if hasRoute && (tx.RouteType == nil || *tx.RouteType != filterRoute) {
			return false
		}
		if filterHash != "" && tx.Hash != filterHash {
			return false
		}
		if hasSince && tx.FirstSeen <= q.Since {
			return false
		}
		if hasUntil && tx.FirstSeen >= q.Until {
			return false
		}
		if observerSet != nil {
			found := false
			for _, obs := range tx.Observations {
				if observerSet[obs.ObserverID] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if regionObservers != nil {
			found := false
			for _, obs := range tx.Observations {
				if regionObservers[obs.ObserverID] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		if areaNodes != nil {
			// Only ADVERT packets carry the originator pubkey (public_key/pubKey).
			// All other packet types (GRP_TXT, TXT_MSG, REQ, …) have encrypted
			// senders so pk == "" and are excluded when an area filter is active.
			d := tx.ParsedDecoded()
			pk, _ := d["public_key"].(string)
			if pk == "" {
				pk, _ = d["pubKey"].(string)
			}
			if pk == "" || !areaNodes[pk] {
				return false
			}
		}
		if hasNode {
			if !nodeHashSet[tx.Hash] {
				return false
			}
		}
		if filterChannel != "" {
			if !packetMatchesChannel(tx, filterChannel) {
				return false
			}
		}
		return true
	})

	return results
}

// packetMatchesChannel returns true if the transmission's decoded payload
// matches the requested channel filter (#812). The filter accepts either a
// plaintext channel name (e.g. "public", "#test") matching decoded.channel,
// or "enc_<HEX>" matching the channelHashHex of an undecryptable GRP_TXT.
func packetMatchesChannel(tx *StoreTx, filterChannel string) bool {
	if tx.PayloadType == nil || *tx.PayloadType != 5 {
		return false
	}
	if tx.DecodedJSON == "" {
		return false
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return false
	}
	if ch, ok := d["channel"].(string); ok && ch != "" {
		if ch == filterChannel {
			return true
		}
	}
	if strings.HasPrefix(filterChannel, "enc_") {
		if hex, ok := d["channelHashHex"].(string); ok && hex != "" {
			if "enc_"+hex == filterChannel {
				return true
			}
		}
	}
	return false
}

// transmissionsForObserver returns unique transmissions for an observer.
func (s *PacketStore) transmissionsForObserver(observerIDs string, from []*StoreTx) []*StoreTx {
	ids := strings.Split(observerIDs, ",")
	idSet := make(map[string]bool, len(ids))
	for i, id := range ids {
		ids[i] = strings.TrimSpace(id)
		idSet[ids[i]] = true
	}
	if from != nil {
		return filterTxSlice(from, func(tx *StoreTx) bool {
			for _, obs := range tx.Observations {
				if idSet[obs.ObserverID] {
					return true
				}
			}
			return false
		})
	}
	// Use byObserver index: union transmissions for all IDs
	seen := make(map[int]bool)
	var result []*StoreTx
	for _, id := range ids {
		for _, obs := range s.byObserver[id] {
			if seen[obs.TransmissionID] {
				continue
			}
			seen[obs.TransmissionID] = true
			tx := s.byTxID[obs.TransmissionID]
			if tx != nil {
				result = append(result, tx)
			}
		}
	}
	return result
}

// resolveRegionObservers returns a set of observer IDs for a given IATA region.
// Results are cached for 30 seconds to avoid repeated DB queries.
// Uses its own mutex (regionObsMu) so callers holding s.mu won't deadlock.
func (s *PacketStore) resolveRegionObservers(region string) map[string]bool {
	s.regionObsMu.Lock()
	defer s.regionObsMu.Unlock()

	if s.regionObsCache != nil && time.Since(s.regionObsCacheTime) < 30*time.Second {
		if m, ok := s.regionObsCache[region]; ok {
			return m
		}
		return s.fetchAndCacheRegionObs(region)
	}
	// Cache expired — rebuild.
	s.regionObsCache = make(map[string]map[string]bool)
	s.regionObsCacheTime = time.Now()

	// Fetch for the requested region and cache it.
	return s.fetchAndCacheRegionObs(region)
}

// fetchAndCacheRegionObs fetches observer IDs for a region from the DB and stores in cache.
// Caller must hold regionObsMu.
func (s *PacketStore) fetchAndCacheRegionObs(region string) map[string]bool {
	if m, ok := s.regionObsCache[region]; ok {
		return m
	}
	ids, err := s.db.GetObserverIdsForRegion(region)
	if err != nil || len(ids) == 0 {
		s.regionObsCache[region] = nil
		return nil
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	s.regionObsCache[region] = m
	return m
}

// resolveAreaNodes returns a set of node pubkeys whose GPS coordinates fall
// inside the named area polygon. Returns nil if the area key is not in config.
// Results are cached per-key for 30 seconds. Uses its own RWMutex so callers
// holding s.mu won't deadlock.
func (s *PacketStore) resolveAreaNodes(areaKey string) map[string]bool {
	if s.config == nil || s.config.Areas == nil {
		return nil
	}
	entry, ok := s.config.Areas[areaKey]
	if !ok {
		return nil
	}

	// Fast path: serve from cache if the per-key TTL is still valid.
	s.areaNodeMu.RLock()
	if t, ok := s.areaNodeCacheTimes[areaKey]; ok && time.Since(t) < 30*time.Second {
		m := s.areaNodeCache[areaKey]
		s.areaNodeMu.RUnlock()
		return m
	}
	s.areaNodeMu.RUnlock()

	// Slow path: query the DB outside any lock, then write back under Lock.
	pks, err := s.db.GetNodePubkeysInArea(entry)
	var m map[string]bool
	if err == nil && len(pks) > 0 {
		m = make(map[string]bool, len(pks))
		for _, pk := range pks {
			m[pk] = true
		}
	}

	s.areaNodeMu.Lock()
	// Re-check in case another goroutine already refreshed while we queried.
	if t, ok := s.areaNodeCacheTimes[areaKey]; !ok || time.Since(t) >= 30*time.Second {
		s.areaNodeCache[areaKey] = m
		s.areaNodeCacheTimes[areaKey] = time.Now()
	} else {
		m = s.areaNodeCache[areaKey]
	}
	s.areaNodeMu.Unlock()
	return m
}

// iataMatchesRegion returns true if iata matches any of the comma-separated
// region codes in regionParam. Comparison is case-insensitive and trim-tolerant.
// Empty iata never matches; empty regionParam never matches.
//
// #804: shared helper used by analytics to attribute transmissions to a node's
// HOME region (derived from observers that hear its zero-hop direct adverts)
// rather than to the observer that happened to relay a packet.
func iataMatchesRegion(iata, regionParam string) bool {
	if iata == "" || regionParam == "" {
		return false
	}
	codes := normalizeRegionCodes(regionParam)
	if len(codes) == 0 {
		return false
	}
	got := strings.TrimSpace(strings.ToUpper(iata))
	if got == "" {
		return false
	}
	for _, c := range codes {
		if c == got {
			return true
		}
	}
	return false
}

// computeNodeHomeRegions returns a pubkey → IATA map deriving each node's
// HOME region from zero-hop DIRECT adverts. A zero-hop direct advert is the
// most authoritative location signal because the path byte is set locally on
// the originating radio and the packet has not been relayed: the observer
// that hears it is necessarily within direct RF range of the originator.
//
// When a node has zero-hop direct adverts heard by observers from multiple
// regions, the most-frequently-observed region wins (geographic plurality).
//
// Caller must hold s.mu (read or write). Returns empty map (not nil) if no
// observers are loaded or no zero-hop direct adverts have been seen.
//
// #804: feeds analytics region-attribution so a multi-byte repeater whose
// flood adverts get relayed across regions is still attributed to its home.
func (s *PacketStore) computeNodeHomeRegions() map[string]string {
	// Build observer → IATA map. observers table is small (≪ packets), so a
	// single DB read here is acceptable; resolveRegionObservers does similar.
	obsIATA := make(map[string]string, 64)
	if s.db != nil {
		if observers, err := s.db.GetObservers(); err == nil {
			for _, o := range observers {
				if o.IATA != nil && *o.IATA != "" {
					obsIATA[o.ID] = strings.TrimSpace(strings.ToUpper(*o.IATA))
				}
			}
		}
	}
	if len(obsIATA) == 0 {
		return map[string]string{}
	}

	// Tally zero-hop direct ADVERT region observations per pubkey.
	type tally struct {
		counts map[string]int
	}
	per := make(map[string]*tally, 256)

	for _, tx := range s.packets {
		if tx.RawHex == "" || len(tx.RawHex) < 4 {
			continue
		}
		if tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT {
			continue
		}
		if tx.DecodedJSON == "" {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := header & 0x03
		if routeType != uint64(RouteDirect) && routeType != uint64(RouteTransportDirect) {
			continue
		}
		// Path byte index — for direct/transport-direct it's at offset 1
		// (matches the analytics decoder's pathByteIdx logic).
		if len(tx.RawHex) < 4 {
			continue
		}
		pathByte, err := strconv.ParseUint(tx.RawHex[2:4], 16, 8)
		if err != nil {
			continue
		}
		hopCount := pathByte & 0x3F
		if hopCount != 0 {
			continue
		}

		var d map[string]interface{}
		if json.Unmarshal([]byte(tx.DecodedJSON), &d) != nil {
			continue
		}
		pk, _ := d["pubKey"].(string)
		if pk == "" {
			pk, _ = d["public_key"].(string)
		}
		if pk == "" {
			continue
		}

		for _, obs := range tx.Observations {
			iata := obsIATA[obs.ObserverID]
			if iata == "" {
				continue
			}
			t := per[pk]
			if t == nil {
				t = &tally{counts: map[string]int{}}
				per[pk] = t
			}
			t.counts[iata]++
		}
	}

	out := make(map[string]string, len(per))
	for pk, t := range per {
		var bestIATA string
		bestCount := 0
		for iata, n := range t.counts {
			if n > bestCount || (n == bestCount && iata < bestIATA) {
				bestCount = n
				bestIATA = iata
			}
		}
		if bestIATA != "" {
			out[pk] = bestIATA
		}
	}
	return out
}

// enrichObs returns a map with observation fields + transmission fields.
func (s *PacketStore) enrichObs(obs *StoreObs) map[string]interface{} {
	tx := s.byTxID[obs.TransmissionID]

	m := map[string]interface{}{
		"id":            obs.ID,
		"timestamp":     strOrNil(obs.Timestamp),
		"observer_id":   strOrNil(obs.ObserverID),
		"observer_name": strOrNil(obs.ObserverName),
		"observer_iata": strOrNil(obs.ObserverIATA),
		"direction":     strOrNil(obs.Direction),
		"snr":           floatPtrOrNil(obs.SNR),
		"rssi":          floatPtrOrNil(obs.RSSI),
		"score":         intPtrOrNil(obs.Score),
		"path_json":     strOrNil(obs.PathJSON),
	}
	// On-demand SQL fetch for resolved_path
	rp := s.fetchResolvedPathForObs(obs.ID)
	if rp != nil {
		m["resolved_path"] = rp
	}

	if tx != nil {
		m["hash"] = strOrNil(tx.Hash)
		// Prefer per-observation raw_hex; fall back to transmission-level (#881)
		if obs.RawHex != "" {
			m["raw_hex"] = obs.RawHex
		} else {
			m["raw_hex"] = strOrNil(tx.RawHex)
		}
		m["payload_type"] = intPtrOrNil(tx.PayloadType)
		m["route_type"] = intPtrOrNil(tx.RouteType)
		m["decoded_json"] = strOrNil(tx.DecodedJSON)
	}

	return m
}

// --- Conversion helpers ---

// txToMap converts a StoreTx to the map shape matching scanTransmissionRow output.
func txToMap(tx *StoreTx, includeObservations ...bool) map[string]interface{} {
	m := map[string]interface{}{
		"id":                tx.ID,
		"raw_hex":           strOrNil(tx.RawHex),
		"hash":              strOrNil(tx.Hash),
		"first_seen":        strOrNil(tx.FirstSeen),
		"timestamp":         strOrNil(tx.FirstSeen),
		"route_type":        intPtrOrNil(tx.RouteType),
		"payload_type":      intPtrOrNil(tx.PayloadType),
		"decoded_json":      strOrNil(tx.DecodedJSON),
		"observation_count": tx.ObservationCount,
		"observer_id":       strOrNil(tx.ObserverID),
		"observer_name":     strOrNil(tx.ObserverName),
		"observer_iata":     strOrNil(tx.ObserverIATA),
		"snr":               floatPtrOrNil(tx.SNR),
		"rssi":              floatPtrOrNil(tx.RSSI),
		"path_json":         strOrNil(tx.PathJSON),
		"direction":         strOrNil(tx.Direction),
	}
	// Include parsed path array to match Node.js output shape
	if hops := txGetParsedPath(tx); len(hops) > 0 {
		m["_parsedPath"] = hops
	} else {
		m["_parsedPath"] = nil
	}
	// Only build observation sub-maps when caller requests them (avoids allocations that get stripped)
	if len(includeObservations) > 0 && includeObservations[0] {
		obs := make([]map[string]interface{}, 0, len(tx.Observations))
		for _, o := range tx.Observations {
			om := map[string]interface{}{
				"id":            o.ID,
				"observer_id":   strOrNil(o.ObserverID),
				"observer_name": strOrNil(o.ObserverName),
				"observer_iata": strOrNil(o.ObserverIATA),
				"snr":           floatPtrOrNil(o.SNR),
				"rssi":          floatPtrOrNil(o.RSSI),
				"path_json":     strOrNil(o.PathJSON),
				"timestamp":     strOrNil(o.Timestamp),
				"direction":     strOrNil(o.Direction),
			}
			obs = append(obs, om)
		}
		m["observations"] = obs
	}
	return m
}

// txToMapWithRP is like txToMap but also fetches resolved_path on demand from the store.
func (s *PacketStore) txToMapWithRP(tx *StoreTx, includeObservations ...bool) map[string]interface{} {
	m := txToMap(tx, includeObservations...)
	// On-demand SQL fetch for resolved_path
	rp := s.fetchResolvedPathForTxBest(tx)
	if rp != nil {
		m["resolved_path"] = rp
	}
	// Also add resolved_path to observation sub-maps if present
	if len(includeObservations) > 0 && includeObservations[0] {
		if obsList, ok := m["observations"].([]map[string]interface{}); ok {
			for i, o := range tx.Observations {
				if i < len(obsList) {
					obsRP := s.fetchResolvedPathForObs(o.ID)
					if obsRP != nil {
						obsList[i]["resolved_path"] = obsRP
					}
				}
			}
		}
	}
	return m
}

// txToMapWithRPCached is identical to txToMapWithRP but reads resolved paths
// from a pre-fetched per-tx map (built by prefetchResolvedPathsForTxs) instead
// of issuing one SQL query per packet/observation (review item #3). byObs may
// be nil for a tx that had no stored resolved_path.
func (s *PacketStore) txToMapWithRPCached(tx *StoreTx, byObs map[int][]*string, includeObservations bool) map[string]interface{} {
	m := txToMap(tx, includeObservations)
	if rp := resolvedPathForTxBestFromCache(tx, byObs); rp != nil {
		m["resolved_path"] = rp
	}
	if includeObservations {
		if obsList, ok := m["observations"].([]map[string]interface{}); ok {
			for i, o := range tx.Observations {
				if i < len(obsList) {
					if obsRP := byObs[o.ID]; obsRP != nil {
						obsList[i]["resolved_path"] = obsRP
					}
				}
			}
		}
	}
	return m
}

func strOrNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// normalizeTimestamp converts SQLite datetime format ("YYYY-MM-DD HH:MM:SS")
// to ISO 8601 ("YYYY-MM-DDTHH:MM:SSZ"). Already-ISO strings pass through.
func normalizeTimestamp(s string) string {
	if s == "" {
		return s
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return s
}

func intPtrOrNil(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrOrNil(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func nullIntPtr(ni sql.NullInt64) *int {
	if ni.Valid {
		v := int(ni.Int64)
		return &v
	}
	return nil
}

func nullFloatPtr(nf sql.NullFloat64) *float64 {
	if nf.Valid {
		return &nf.Float64
	}
	return nil
}

// resolvePayloadTypeName returns the firmware-standard name for a payload_type.
func resolvePayloadTypeName(pt *int) string {
	if pt == nil {
		return "UNKNOWN"
	}
	if name, ok := payloadTypeNames[*pt]; ok {
		return name
	}
	return fmt.Sprintf("UNK(%d)", *pt)
}

// txGetParsedPath returns cached parsed path hops, parsing on first call.
// Guarded by tx.pathMu so concurrent read queries (each under s.mu.RLock())
// cannot race-write parsedPath/pathParsed (review item #4).
func txGetParsedPath(tx *StoreTx) []string {
	tx.pathMu.Lock()
	defer tx.pathMu.Unlock()
	if tx.pathParsed {
		return tx.parsedPath
	}
	tx.parsedPath = parsePathJSON(tx.PathJSON)
	tx.pathParsed = true
	return tx.parsedPath
}

// buildPathHopIndex scans all packets and populates byPathHop.
// Must be called with s.mu held.
func (s *PacketStore) buildPathHopIndex() {
	s.byPathHop = make(map[string][]*StoreTx, 4096)
	for _, tx := range s.packets {
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	log.Printf("[store] Built path-hop index: %d unique keys", len(s.byPathHop))
}

// addTxToPathHopIndex indexes a transmission under each unique raw hop key.
// Resolved pubkey keys are handled by the decode-window via feedDecodeWindowConsumers.
func addTxToPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops))
	for _, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			idx[key] = append(idx[key], tx)
		}
	}
}

// addTxToRelayTimeIndex records the relay timestamp for each resolved pubkey.
// pubkeys is the pre-extracted list (use extractResolvedPubkeys on the decoded path).
// Maintains sorted ascending order for O(log n) window queries.
// Must be called with s.mu held (or during build before store is live).
func addTxToRelayTimeIndex(idx map[string][]int64, firstSeen string, pubkeys []string) {
	if len(pubkeys) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		pk = strings.ToLower(pk)
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		if i < len(slice) && slice[i] == millis {
			continue // idempotent
		}
		slice = append(slice, 0)
		copy(slice[i+1:], slice[i:])
		slice[i] = millis
		idx[pk] = slice
	}
}

// removeFromRelayTimeIndex removes the relay timestamp for each resolved pubkey.
// Inverse of addTxToRelayTimeIndex.
func removeFromRelayTimeIndex(idx map[string][]int64, firstSeen string, pubkeys []string) {
	if len(pubkeys) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(pubkeys))
	for _, pk := range pubkeys {
		pk = strings.ToLower(pk)
		if pk == "" || seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		if i < len(slice) && slice[i] == millis {
			newSlice := make([]int64, 0, len(slice)-1)
			newSlice = append(newSlice, slice[:i]...)
			newSlice = append(newSlice, slice[i+1:]...)
			idx[pk] = newSlice
			if len(newSlice) == 0 {
				delete(idx, pk)
			}
		}
	}
}

// relayMetrics computes relay_count_1h, relay_count_24h, and last_relayed from a
// sorted unix-millis slice. now is time.Now().UnixMilli(). O(log n).
func relayMetrics(times []int64, now int64) (count1h, count24h int, lastRelayed string) {
	if len(times) == 0 {
		return 0, 0, ""
	}
	i1h := sort.Search(len(times), func(i int) bool { return times[i] >= now-3600000 })
	i24h := sort.Search(len(times), func(i int) bool { return times[i] >= now-86400000 })
	count1h = len(times) - i1h
	count24h = len(times) - i24h
	lastRelayed = time.UnixMilli(times[len(times)-1]).UTC().Format(time.RFC3339)
	return
}

// removeTxFromPathHopIndex removes a transmission from all its raw path-hop index entries.
// Resolved pubkey entries are cleaned up via removeFromResolvedPubkeyIndex.
func removeTxFromPathHopIndex(idx map[string][]*StoreTx, tx *StoreTx) {
	hops := txGetParsedPath(tx)
	if len(hops) == 0 {
		return
	}
	seen := make(map[string]bool, len(hops))
	for _, hop := range hops {
		key := strings.ToLower(hop)
		if !seen[key] {
			seen[key] = true
			removeTxFromSlice(idx, key, tx)
		}
	}
}

// addResolvedPubkeysToPathHopIndex appends tx into byPathHop under each
// resolved pubkey key that isn't already present as a raw hop. Mutating
// byPathHop here MUST be paired with invalidateRelayStatsCache so the
// cached batch relay stats don't go stale for up to relayStatsCacheTTL.
// hopsSeen is a scratch map the caller can reuse across calls (it will
// be cleared on entry).
//
// Must be called with s.mu held.
func (s *PacketStore) addResolvedPubkeysToPathHopIndex(tx *StoreTx, pubkeys []string, hopsSeen map[string]bool) bool {
	if len(pubkeys) == 0 {
		return false
	}
	clear(hopsSeen)
	for _, hop := range txGetParsedPath(tx) {
		hopsSeen[strings.ToLower(hop)] = true
	}
	mutated := false
	for _, pk := range pubkeys {
		if !hopsSeen[pk] {
			hopsSeen[pk] = true
			s.byPathHop[pk] = append(s.byPathHop[pk], tx)
			mutated = true
		}
	}
	// Mutating byPathHop invalidates the batch relay-stats cache (#1164).
	if mutated {
		s.invalidateRelayStatsCache()
	}
	return mutated
}

// invalidateRelayStatsCache drops the cached batch relay-stats result so
// the next call to GetRepeaterNodeStatsBatchCached recomputes from scratch.
// Call this whenever byPathHop changes (add or remove). Safe to call while
// holding s.mu — relayStatsCacheMu is never acquired while holding relayStatsCacheMu,
// so there is no lock-ordering cycle.
func (s *PacketStore) invalidateRelayStatsCache() {
	s.relayStatsCacheMu.Lock()
	s.relayStatsCache = nil
	s.relayStatsCacheMu.Unlock()
}

// removeTxFromSlice removes tx from idx[key] by ID, deleting the key if empty.
func removeTxFromSlice(idx map[string][]*StoreTx, key string, tx *StoreTx) {
	list := idx[key]
	for i, t := range list {
		if t.ID == tx.ID {
			idx[key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(idx[key]) == 0 {
		delete(idx, key)
	}
}

// Self-accounting memory estimation constants.
// These estimate the in-memory cost of StoreTx and StoreObs structs including
// map/index overhead. They don't need to be exact — just proportional to actual
// usage and independent of GC state.
//
// Issue #743: Previous estimates missed major per-packet allocations:
// - spTxIndex: O(path²) entries per tx (50-150MB at scale)
// - Per-tx maps: obsKeys, observerSet (~11MB at scale)
// - byPathHop index entries (20-40MB at scale)
// Note: ResolvedPath per-obs overhead eliminated by #800 refactor.
const (
	storeTxBaseBytes  = 384 // StoreTx struct fields + map headers + sync.Once + string headers
	storeObsBaseBytes = 192 // StoreObs struct fields + string headers
	indexEntryBytes   = 48  // average cost of one index map entry (key + pointer + bucket overhead)
	numIndexesPerTx   = 5   // byHash, byTxID, byNode, byPayloadType, nodeHashes entries
	numIndexesPerObs  = 2   // byObsID, byObserver entries

	// Per-tx map overhead (obsKeys + observerSet): map header + initial buckets
	perTxMapsBytes = 200

	// Per path hop: byPathHop index entry (pointer + map bucket)
	perPathHopBytes = 50

	// Per subpath entry in spTxIndex: string key + slice append + pointer
	perSubpathEntryBytes = 40
)

// estimateStoreTxBytes returns the estimated memory cost of a StoreTx (excluding observations).
// Includes per-tx maps (obsKeys, observerSet), byPathHop entries, and spTxIndex subpath entries.
func estimateStoreTxBytes(tx *StoreTx) int64 {
	base := int64(storeTxBaseBytes)
	base += int64(len(tx.RawHex) + len(tx.Hash) + len(tx.DecodedJSON) + len(tx.PathJSON))
	base += int64(numIndexesPerTx * indexEntryBytes)

	// Per-tx maps: obsKeys + observerSet
	base += perTxMapsBytes

	// Path-dependent costs
	hops := int64(len(txGetParsedPath(tx)))
	base += hops * perPathHopBytes

	// spTxIndex: O(path²) subpath combinations
	if hops > 1 {
		subpaths := hops * (hops - 1) / 2
		base += subpaths * perSubpathEntryBytes
	}

	return base
}

// estimateStoreTxBytesTypical returns the estimated memory cost of a typical
// transmission with the given number of observations. Used for budget
// calculation during bounded cold load (no actual StoreTx needed).
func estimateStoreTxBytesTypical(numObs int) int64 {
	// Typical tx: ~64 byte hash, ~200 byte decoded JSON, ~40 byte path, 3 hops
	base := int64(storeTxBaseBytes) + 64 + 200 + 40
	base += int64(numIndexesPerTx * indexEntryBytes)
	base += perTxMapsBytes
	hops := int64(3)
	base += hops * perPathHopBytes
	base += (hops * (hops - 1) / 2) * perSubpathEntryBytes
	// Add observation costs
	obsBase := int64(storeObsBaseBytes) + 30 + 30 + 60 // observer ID + name + path
	obsBase += int64(numIndexesPerObs * indexEntryBytes)
	// No per-obs ResolvedPath overhead (#800)
	base += int64(numObs) * obsBase
	return base
}

// estimateStoreObsBytes returns the estimated memory cost of a StoreObs.
// ResolvedPath membership index overhead is tracked separately.
func estimateStoreObsBytes(obs *StoreObs) int64 {
	base := int64(storeObsBaseBytes)
	base += int64(len(obs.PathJSON) + len(obs.ObserverID))
	base += int64(numIndexesPerObs * indexEntryBytes)
	// ResolvedPath field removed (#800) — no per-obs RP overhead
	return base
}

// estimatedMemoryMB returns current Go heap allocation in MB.
// Used by eviction logic (dual trigger alongside trackedBytes) and by
// stats/debug endpoints. In tests, memoryEstimator can be set to inject
// a deterministic value (otherwise runtime.ReadMemStats is called).
func (s *PacketStore) estimatedMemoryMB() float64 {
	if s.memoryEstimator != nil {
		return s.memoryEstimator()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / 1048576.0
}

// trackedMemoryMB returns the self-accounted packet store memory in MB.
// It reads s.trackedBytes under s.mu.RLock — callers must NOT already hold
// s.mu. Callers that already hold the lock should snapshot s.trackedBytes
// into a local and use trackedBytesToMB instead, to avoid a data race with
// ingest writers that mutate s.trackedBytes under s.mu.Lock.
func (s *PacketStore) trackedMemoryMB() float64 {
	s.mu.RLock()
	tb := s.trackedBytes
	s.mu.RUnlock()
	return trackedBytesToMB(tb)
}

// trackedBytesToMB converts a pre-read trackedBytes value to MB. Pure function
// — safe to call with the lock held using a snapshotted value.
func trackedBytesToMB(trackedBytes int64) float64 {
	return float64(trackedBytes) / 1048576.0
}

// EvictStale removes packets older than the retention window and/or exceeding
// the memory cap. Must be called with s.mu held (Lock). Returns the number of
// packets evicted.
// evictionCandidateTxIDs determines which tx IDs would be evicted and returns them.
// Must be called under s.mu.Lock (or RLock). Does NOT modify any state.
func (s *PacketStore) evictionCandidateTxIDs() []int {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return nil
	}
	cutoffIdx := 0
	if s.retentionHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour))).Format(time.RFC3339)
		for cutoffIdx < len(s.packets) && s.packets[cutoffIdx].FirstSeen < cutoff {
			cutoffIdx++
		}
	}
	if s.maxMemoryMB > 0 {
		highWatermark := int64(s.maxMemoryMB) * 1048576
		lowWatermark := int64(float64(highWatermark) * 0.85)
		// Dual trigger: self-accounted trackedBytes OR actual Go heap alloc.
		// trackedBytes underestimates actual heap by 2–3× (map overhead, GC
		// metadata, analytics caches are not tracked), so the trackedBytes-only
		// check silently fails when the process is well over budget.
		//
		// HeapAlloc includes the entire process heap — not just the packet store.
		// Analytics caches, observer registry, and HTTP buffers add a baseline
		// overhead that is invisible to trackedBytes. A 15 % uplift on the heap
		// trigger gives those allocations room to exist without prematurely
		// evicting packets; at 1.15× the trigger still sits at 77 % of GOMEMLIMIT
		// (which is set to 1.5× maxMemoryMB), well within the safety margin.
		const heapTriggerFactor = 1.15
		heapMB := s.estimatedMemoryMB()
		trackedOver := s.trackedBytes > highWatermark
		heapOver := heapMB > float64(s.maxMemoryMB)*heapTriggerFactor
		if (trackedOver || heapOver) && len(s.packets) > 0 {
			memCutoff := cutoffIdx
			if trackedOver {
				// trackedBytes path: loop until projected remaining tracked
				// bytes drop below the 85% low-watermark (original logic).
				var bytesToEvict int64
				for memCutoff < len(s.packets) && (s.trackedBytes-bytesToEvict) > lowWatermark {
					tx := s.packets[memCutoff]
					bytesToEvict += estimateStoreTxBytes(tx)
					for _, obs := range tx.Observations {
						bytesToEvict += estimateStoreObsBytes(obs)
					}
					memCutoff++
				}
			}
			if heapOver {
				// Heap path: evict proportionally to bring the packet store's
				// tracked bytes down to the 85% low-watermark.
				//
				// Previous formula used (heapMB - targetMB) / heapMB, which
				// treated the entire Go heap as if it were all packet-store data.
				// In practice heapMB includes analytics caches, GC metadata,
				// WebSocket buffers, and other non-store overhead that cannot be
				// reduced by evicting packets. When that overhead is large,
				// fractionToEvict exceeds 1.0 (capped at 25%), causing maximum
				// eviction every tick even though trackedBytes is within budget.
				//
				// Fix: base the fraction on packet-store overage only.
				// If trackedBytes ≤ targetMB, the heap is high due to non-store
				// overhead — evicting packets won't help, so skip.
				storeTrackedMB := trackedBytesToMB(s.trackedBytes)
				targetMB := float64(s.maxMemoryMB) * 0.85
				storeOverage := storeTrackedMB - targetMB
				if storeOverage > 0 {
					fractionToEvict := storeOverage / storeTrackedMB
					heapCutoff := cutoffIdx + int(fractionToEvict*float64(len(s.packets)))
					if heapCutoff > memCutoff {
						memCutoff = heapCutoff
					}
				}
			}
			// Safety cap: never evict more than 25% of packets in a single pass.
			maxEvict := len(s.packets) / 4
			if maxEvict < 1 {
				maxEvict = 1
			}
			if memCutoff > maxEvict {
				memCutoff = maxEvict
			}
			if memCutoff > cutoffIdx {
				cutoffIdx = memCutoff
			}
		}
	}
	if cutoffIdx == 0 || cutoffIdx > len(s.packets) {
		return nil
	}
	ids := make([]int, cutoffIdx)
	for i := 0; i < cutoffIdx; i++ {
		ids[i] = s.packets[i].ID
	}
	return ids
}

// EvictStaleWithRP runs eviction using pre-fetched resolved pubkeys.
// rpBatch may be nil (in which case resolved pubkey cleanup for byNode/nodeHashes is skipped).
// Must be called under s.mu.Lock.
func (s *PacketStore) EvictStaleWithRP(rpBatch map[int][]string) int {
	return s.evictStaleInternal(rpBatch, 0)
}

// EvictStale runs eviction, fetching resolved pubkeys inline (SQL under lock).
// Prefer RunEviction() which batches the SQL outside the lock.
// Must be called under s.mu.Lock.
func (s *PacketStore) EvictStale() int {
	return s.evictStaleInternal(nil, 0)
}

// evictStaleInternal performs one eviction pass under s.mu.Lock.
// maxChunk caps the number of packets removed in this call (0 = unlimited,
// governed only by the 25% safety cap). Callers that need chunked passes with
// lock yields between chunks (i.e. RunEviction) pass a positive maxChunk.
func (s *PacketStore) evictStaleInternal(rpBatch map[int][]string, maxChunk int) int {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return 0
	}

	cutoffIdx := 0

	// Time-based eviction: find how many packets from the head are too old
	if s.retentionHours > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(s.retentionHours * float64(time.Hour))).Format(time.RFC3339)
		for cutoffIdx < len(s.packets) && s.packets[cutoffIdx].FirstSeen < cutoff {
			cutoffIdx++
		}
	}

	// Memory-based eviction: dual trigger on self-accounted trackedBytes OR
	// actual Go heap alloc. High watermark = maxMemoryMB (trigger), low watermark
	// = 85% (target). Safety cap: never evict more than 25% in a single pass.
	// trackedBytes underestimates actual heap by 2–3× so the heap check catches
	// cases the trackedBytes check misses entirely.
	//
	// The heap trigger uses a 15 % uplift (heapTriggerFactor = 1.15) so that
	// process-wide baseline allocations (analytics caches, observer registry,
	// HTTP buffers) do not prematurely starve the packet store of its configured
	// budget — matching the same constant used in evictionCandidateTxIDs.
	if s.maxMemoryMB > 0 {
		const heapTriggerFactor = 1.15
		highWatermark := int64(s.maxMemoryMB) * 1048576
		lowWatermark := int64(float64(highWatermark) * 0.85)
		heapMB := s.estimatedMemoryMB()
		trackedOver := s.trackedBytes > highWatermark
		heapOver := heapMB > float64(s.maxMemoryMB)*heapTriggerFactor
		if (trackedOver || heapOver) && len(s.packets) > 0 {
			memCutoff := cutoffIdx
			if trackedOver {
				// Original loop: evict until trackedBytes would drop below low-watermark.
				var bytesToEvict int64
				for memCutoff < len(s.packets) && (s.trackedBytes-bytesToEvict) > lowWatermark {
					tx := s.packets[memCutoff]
					bytesToEvict += estimateStoreTxBytes(tx)
					for _, obs := range tx.Observations {
						bytesToEvict += estimateStoreObsBytes(obs)
					}
					memCutoff++
				}
			}
			if heapOver {
				// Heap path: evict proportionally to bring tracked bytes to
				// the 85% low-watermark (same target as the trackedOver path).
				// Using (heapMB - targetMB)/heapMB would treat the entire Go
				// heap as packet-store data; analytics caches, GC metadata, and
				// WebSocket buffers inflate heapMB without being reducible by
				// packet eviction, causing massive over-eviction every tick.
				// When trackedBytes ≤ targetMB the heap overage is due to
				// non-store overhead — evicting packets won't help, so skip.
				storeTrackedMB := trackedBytesToMB(s.trackedBytes)
				targetMB := float64(s.maxMemoryMB) * 0.85
				storeOverage := storeTrackedMB - targetMB
				if storeOverage > 0 {
					fractionToEvict := storeOverage / storeTrackedMB
					heapCutoff := cutoffIdx + int(fractionToEvict*float64(len(s.packets)))
					if heapCutoff > memCutoff {
						memCutoff = heapCutoff
					}
				}
			}
			// Safety cap: never evict more than 25% in a single pass.
			maxEvict := len(s.packets) / 4
			if maxEvict < 1 {
				maxEvict = 1
			}
			if memCutoff > maxEvict {
				memCutoff = maxEvict
			}
			if memCutoff > cutoffIdx {
				cutoffIdx = memCutoff
			}
		}
	}

	if cutoffIdx == 0 {
		return 0
	}
	if cutoffIdx > len(s.packets) {
		cutoffIdx = len(s.packets)
	}
	// Chunk cap: callers (RunEviction) may request a smaller slice per lock
	// acquisition so readers get a chance to proceed between batches.
	if maxChunk > 0 && cutoffIdx > maxChunk {
		cutoffIdx = maxChunk
	}

	evicting := s.packets[:cutoffIdx]
	evictedObs := 0
	var evictedBytes int64

	// Build sets of evicted IDs for batch removal from secondary indexes
	evictedTxIDs := make(map[int]struct{}, cutoffIdx)
	evictedObsIDs := make(map[int]struct{}, cutoffIdx*2)
	// Track which observer IDs and payload types need filtering
	affectedObservers := make(map[string]struct{})
	affectedPayloadTypes := make(map[int]struct{})
	affectedNodes := make(map[string]struct{})

	// First pass: remove from primary indexes (byHash, byTxID, byObsID),
	// collect IDs for batch secondary index cleanup, and handle non-index work
	for _, tx := range evicting {
		delete(s.byHash, tx.Hash)
		delete(s.byTxID, tx.ID)
		evictedTxIDs[tx.ID] = struct{}{}
		evictedBytes += estimateStoreTxBytes(tx)

		for _, obs := range tx.Observations {
			delete(s.byObsID, obs.ID)
			evictedObsIDs[obs.ID] = struct{}{}
			evictedBytes += estimateStoreObsBytes(obs)
			if obs.ObserverID != "" {
				affectedObservers[obs.ObserverID] = struct{}{}
			}
			evictedObs++
		}

		s.untrackAdvertPubkey(tx)
		if tx.PayloadType != nil {
			affectedPayloadTypes[*tx.PayloadType] = struct{}{}
		}

		// Remove from nodeHashes and collect affected node keys.
		// Must mirror indexByNode: process decoded JSON fields AND resolved_path pubkeys.
		evictedFromNode := make(map[string]bool)
		if tx.DecodedJSON != "" {
			var decoded map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &decoded) == nil {
				for _, field := range []string{"pubKey", "destPubKey", "srcPubKey"} {
					if v, ok := decoded[field].(string); ok && v != "" {
						if hashes, ok := s.nodeHashes[v]; ok {
							delete(hashes, tx.Hash)
							if len(hashes) == 0 {
								delete(s.nodeHashes, v)
							}
						}
						affectedNodes[v] = struct{}{}
						evictedFromNode[v] = true
					}
				}
			}
		}
		// Clean up resolved_path pubkeys from byNode/nodeHashes.
		// Uses pre-fetched batch data when available (no SQL under lock).
		var rpPubkeys []string
		if rpBatch != nil {
			rpPubkeys = rpBatch[tx.ID]
		}
		for _, pk := range rpPubkeys {
			if pk == "" || evictedFromNode[pk] {
				continue
			}
			if hashes, ok := s.nodeHashes[pk]; ok {
				delete(hashes, tx.Hash)
				if len(hashes) == 0 {
					delete(s.nodeHashes, pk)
				}
			}
			affectedNodes[pk] = struct{}{}
			evictedFromNode[pk] = true
		}

		// Remove from resolved pubkey index
		s.removeFromResolvedPubkeyIndex(tx.ID)

		// Remove from subpath index
		removeTxFromSubpathIndexFull(s.spIndex, s.spTxIndex, tx)
		// Remove from path-hop index
		removeTxFromPathHopIndex(s.byPathHop, tx)
	}
	s.refreshSpIndexSnap()
	s.invalidateRelayStatsCache()

	// Batch-remove from byObserver: single pass per affected observer slice
	for obsID := range affectedObservers {
		obsList := s.byObserver[obsID]
		filtered := obsList[:0]
		for _, o := range obsList {
			if _, evicted := evictedObsIDs[o.ID]; !evicted {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) == 0 {
			delete(s.byObserver, obsID)
		} else {
			s.byObserver[obsID] = filtered
		}
	}

	// Batch-remove from byPayloadType: single pass per affected type slice
	for pt := range affectedPayloadTypes {
		ptList := s.byPayloadType[pt]
		filtered := ptList[:0]
		for _, t := range ptList {
			if _, evicted := evictedTxIDs[t.ID]; !evicted {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(s.byPayloadType, pt)
		} else {
			s.byPayloadType[pt] = filtered
		}
	}

	// Batch-remove from byNode: single pass per affected node slice
	for nodeKey := range affectedNodes {
		nodeList := s.byNode[nodeKey]
		filtered := nodeList[:0]
		for _, t := range nodeList {
			if _, evicted := evictedTxIDs[t.ID]; !evicted {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(s.byNode, nodeKey)
		} else {
			s.byNode[nodeKey] = filtered
		}
	}

	// Remove from distance indexes — O(evicted) swap-with-last via the by-tx
	// index maps, instead of O(total distHops+distPaths) full-scan per chunk.
	// rebuildDistIndexMaps is not needed: removeDistRecordsForTxs maintains the
	// maps incrementally. Initialise maps from slices only if they are nil.
	if (s.distHopsByTx == nil || s.distPathsByTx == nil) && (len(s.distHops) > 0 || len(s.distPaths) > 0) {
		s.rebuildDistIndexMaps()
	}
	evictedTxIDsBool := make(map[int]bool, len(evictedTxIDs))
	for id := range evictedTxIDs {
		evictedTxIDsBool[id] = true
	}
	s.removeDistRecordsForTxs(evictedTxIDsBool)

	// Trim packets slice
	n := copy(s.packets, s.packets[cutoffIdx:])
	s.packets = s.packets[:n]
	s.totalObs -= evictedObs

	evictCount := cutoffIdx
	atomic.AddInt64(&s.evicted, int64(evictCount))
	s.trackedBytes -= evictedBytes
	if s.trackedBytes < 0 {
		s.trackedBytes = 0
	}
	// s.mu.Lock is held here; use the lock-free converter on the snapshotted
	// s.trackedBytes — trackedMemoryMB() would re-acquire RLock and deadlock.
	log.Printf("[store] Evicted %d packets (%d obs, freed ~%.1fMB, tracked ~%.1fMB)",
		evictCount, evictedObs, float64(evictedBytes)/1048576.0, trackedBytesToMB(s.trackedBytes))

	// Eviction removes data — all caches may be affected
	s.invalidateCachesFor(cacheInvalidation{eviction: true})

	// Invalidate hash size cache
	s.hashSizeInfoMu.Lock()
	s.hashSizeInfoCache = nil
	s.hashSizeInfoMu.Unlock()

	return evictCount
}

// RunEviction acquires the write lock and runs eviction. Safe to call from
// a goroutine. Returns evicted count.
//
// Three-phase approach:
//  1. Determine eviction candidates under a short-held write lock.
//  2. Batch-fetch resolved pubkeys from SQL with no lock held.
//  3. Evict in chunks of evictionChunkSize, releasing and re-acquiring the
//     write lock between chunks. This yields to pending reads (e.g. /api/stats,
//     packet ingest) so they are not starved for 90 s during large evictions.
func (s *PacketStore) RunEviction() int {
	// Phase 1: determine candidates under lock
	s.mu.Lock()
	txIDs := s.evictionCandidateTxIDs()
	s.mu.Unlock()

	// Phase 2: batch-fetch resolved pubkeys from SQL (no lock held)
	var rpBatch map[int][]string
	if len(txIDs) > 0 {
		rpBatch = s.resolvedPubkeysForEvictionBatch(txIDs)
	}

	// Phase 3: chunked eviction — each iteration holds the write lock only
	// for evictionChunkSize packets, then yields so readers can acquire RLock.
	//
	// IMPORTANT: the loop must not evict more than len(txIDs) packets total.
	// txIDs was produced by evictionCandidateTxIDs() which applies a 25%
	// safety cap. rpBatch only covers those txIDs; packets evicted beyond
	// len(txIDs) would have missing resolved-pubkey data, leaving byNode and
	// nodeHashes indexes stale. Stopping at len(txIDs) also restores the
	// original intent of the safety cap: at most 25% of the store is removed
	// per RunEviction call (≈ per ticker fire), preventing sudden large drops.
	// Smaller chunk size reduces per-chunk write-lock hold from seconds to
	// tens of milliseconds, giving readers (/api/stats, /api/health) a window
	// between chunks.  The loop safety bound is set generously so the full
	// 25%-of-store cap can always be reached even at the smaller chunk size.
	const evictionChunkSize = 500
	maxTotal := len(txIDs)
	total := 0
	maxChunks := maxTotal/evictionChunkSize + 10 // generous safety bound
	for i := 0; i < maxChunks; i++ {
		if total >= maxTotal {
			break
		}
		remaining := maxTotal - total
		chunkCap := evictionChunkSize
		if remaining < chunkCap {
			chunkCap = remaining
		}
		s.mu.Lock()
		n := s.evictStaleInternal(rpBatch, chunkCap)
		s.mu.Unlock()
		total += n
		if n == 0 {
			break
		}
		runtime.Gosched() // yield between chunks so readers can acquire RLock
	}
	// Compact the resolved pubkey index once per RunEviction call rather than
	// once per chunk — CompactResolvedPubkeyIndex is O(index size) and calling
	// it 25–100 times per RunEviction was the dominant source of write-lock
	// contention visible in /api/stats latency.
	if total > 0 {
		s.mu.Lock()
		s.CompactResolvedPubkeyIndex()
		s.CheckResolvedPubkeyIndexSize()
		s.mu.Unlock()
	}
	return total
}

// StartEvictionTicker starts a background goroutine that runs eviction every
// minute. Returns a stop function.
func (s *PacketStore) StartEvictionTicker() func() {
	if s.retentionHours <= 0 && s.maxMemoryMB <= 0 {
		return func() {} // no-op
	}
	ticker := time.NewTicker(1 * time.Minute)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				s.RunEviction()
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// hasUpperASCII reports whether s contains any uppercase ASCII letter.
// Used by resolveWithContext tier-1 to skip the strings.ToLower allocation
// when the context pubkeys are already lowercased (the common case — see
// buildHopContextPubkeys / buildAggregateHopContextPubkeys, which lowercase
// on the way in). #1247.
func hasUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return false
}

func filterTxSlice(s []*StoreTx, fn func(*StoreTx) bool) []*StoreTx {
	var result []*StoreTx
	for _, tx := range s {
		if fn(tx) {
			result = append(result, tx)
		}
	}
	return result
}

// countNonPrintable counts characters that are non-printable (< 0x20 except \n, \t)
// or invalid UTF-8 replacement characters. Mirrors the heuristic from #197.
func countNonPrintable(s string) int {
	count := 0
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			count++
		} else if r == utf8.RuneError {
			count++
		}
	}
	return count
}

// hasGarbageChars returns true if the string contains garbage (non-printable) data.
func hasGarbageChars(s string) bool {
	return s != "" && (!utf8.ValidString(s) || countNonPrintable(s) > 2)
}

// --- Topology Analytics ---

type nodeInfo struct {
	PublicKey        string
	Name             string
	Role             string
	Lat              float64
	Lon              float64
	HasGPS           bool
	LastSeen         time.Time
	ObservationCount int // count of advertisements/observations; used for tier-3 tiebreak in resolveWithContext
}

// schemaDegradationLogged is now a PacketStore field (see type definition) so
// each store/test instance has a fresh dedupe set. Issue #1199 item 5: the
// prior package-level sync.Map silently suppressed re-emission across tests.

func (s *PacketStore) logSchemaDegradationOnce(msg string) {
	if _, loaded := s.schemaDegradationLogged.LoadOrStore(msg, true); !loaded {
		log.Printf("[store] schema-degradation: %s", msg)
	}
}

func (s *PacketStore) getAllNodes() []nodeInfo {
	// Schema probe: try richest → leanest. Logs a one-shot warning when we
	// fall back to a thinner schema so operators see that a column is
	// missing and the new tiebreak features are degraded. See #1197
	// (adversarial r1 #10).
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon, last_seen, COALESCE(advert_count, 0) FROM nodes")
	hasLastSeen := true
	hasAdvertCount := true
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
	defer rows.Close()
	var nodes []nodeInfo
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		var lastSeen sql.NullString
		var advertCount sql.NullInt64
		if hasAdvertCount {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen, &advertCount)
		} else if hasLastSeen {
			rows.Scan(&pk, &name, &role, &lat, &lon, &lastSeen)
		} else {
			rows.Scan(&pk, &name, &role, &lat, &lon)
		}
		n := nodeInfo{PublicKey: pk, Name: nullStrVal(name), Role: nullStrVal(role)}
		if lat.Valid && lon.Valid {
			n.Lat = lat.Float64
			n.Lon = lon.Float64
			n.HasGPS = !(n.Lat == 0 && n.Lon == 0)
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

type prefixMap struct {
	m map[string][]nodeInfo
}

// maxPrefixLen caps prefix map entries. MeshCore path hops use 2–6 char
// prefixes; 8 gives comfortable headroom while cutting map size from ~31×N
// entries to ~7×N (+ 1 full-key entry per node for exact-match lookups).
const maxPrefixLen = 8

// canAppearInPath returns true if the node's role allows it to appear as a
// path hop.  Only repeaters, room servers, and rooms can forward packets;
// companions and sensors originate but never relay.
func canAppearInPath(role string) bool {
	r := strings.ToLower(role)
	return strings.Contains(r, "repeater") || strings.Contains(r, "room_server") || r == "room"
}

func buildPrefixMap(nodes []nodeInfo) *prefixMap {
	pm := &prefixMap{m: make(map[string][]nodeInfo, len(nodes)*(maxPrefixLen+1))}
	for _, n := range nodes {
		if !canAppearInPath(n.Role) {
			continue
		}
		pk := strings.ToLower(n.PublicKey)
		maxLen := maxPrefixLen
		if maxLen > len(pk) {
			maxLen = len(pk)
		}
		for l := 2; l <= maxLen; l++ {
			pfx := pk[:l]
			pm.m[pfx] = append(pm.m[pfx], n)
		}
		// Always add full pubkey so exact-match lookups work.
		if len(pk) > maxPrefixLen {
			pm.m[pk] = append(pm.m[pk], n)
		}
	}
	return pm
}

// getCachedNodesAndPM returns cached node list and prefix map, rebuilding if stale.
// Must be called with s.mu held (RLock or Lock).
func (s *PacketStore) getCachedNodesAndPM() ([]nodeInfo, *prefixMap) {
	s.cacheMu.Lock()
	if s.nodeCache != nil && time.Since(s.nodeCacheTime) < 30*time.Second {
		nodes, pm := s.nodeCache, s.nodePM
		s.cacheMu.Unlock()
		return nodes, pm
	}
	s.cacheMu.Unlock()

	nodes := s.getAllNodes()
	pm := buildPrefixMap(nodes)

	s.cacheMu.Lock()
	s.nodeCache = nodes
	s.nodePM = pm
	s.nodeCacheTime = time.Now()
	s.cacheMu.Unlock()

	return nodes, pm
}

// InvalidateNodeCache forces the next getCachedNodesAndPM call to rebuild.
func (s *PacketStore) InvalidateNodeCache() {
	s.cacheMu.Lock()
	s.nodeCache = nil
	s.nodePM = nil
	s.nodeCacheTime = time.Time{}
	s.cacheMu.Unlock()
}

func (pm *prefixMap) resolve(hop string) *nodeInfo {
	h := strings.ToLower(hop)
	candidates := pm.m[h]
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &candidates[0]
	}
	// Multiple candidates: prefer one with GPS
	for i := range candidates {
		if candidates[i].HasGPS {
			return &candidates[i]
		}
	}
	return &candidates[0]
}

// resolveWithContext resolves a hop prefix using the neighbor affinity graph
// for disambiguation when multiple candidates match. It applies a 4-tier
// priority:
//
//	(1) "neighbor_affinity"           — graph score vs context nodes,
//	                                    requires affinity ≥3× runner-up and
//	                                    affinityMinObservations
//	(2) "geo_proximity"               — geographic proximity to GPS context
//	                                    centroid (only fires when at least
//	                                    one context node has GPS)
//	(3) "gps_preference"              — among GPS-having candidates, pick
//	                                    highest ObservationCount; lex-pubkey
//	                                    tiebreak for determinism
//	(4) "observation_count_fallback"  — no GPS available; pick highest
//	                                    ObservationCount; lex-pubkey tiebreak
//
// (Pre-PR #1197/#1198 the tier-3 step was first-GPS-wins and tier-4 was
// first-slice-element. Both now use observation count + lex tiebreak; the
// returned method label was renamed accordingly.)
//
// contextPubkeys are pubkeys of nodes that provide context for disambiguation
// (e.g., the originator, observer, or adjacent hops in the path).
// graph may be nil, in which case tier-1 is skipped.
func (pm *prefixMap) resolveWithContext(hop string, contextPubkeys []string, graph *NeighborGraph) (*nodeInfo, string, float64) {
	h := strings.ToLower(hop)
	candidates := pm.m[h]
	if len(candidates) == 0 {
		return nil, "no_match", 0
	}
	if len(candidates) == 1 {
		return &candidates[0], "unique_prefix", 1.0
	}

	// Priority 1: Affinity graph score
	//
	// NOTE: We use raw Score() (count × time-decay) here rather than Jaccard
	// similarity. Jaccard is used at the graph builder level (disambiguate() in
	// neighbor_graph.go) to resolve ambiguous edges by comparing neighbor-set
	// overlap. Here, edges are already resolved — we just need to pick the
	// highest-affinity candidate among them. Raw score is appropriate because
	// it reflects both observation frequency and recency, which are the right
	// signals for "which candidate is this hop most likely referring to."
	//
	// Issue #1229 (Option C): the raw score is further multiplied by
	// e.Confidence() — a source-diversity factor in (0,1] derived from the
	// number of distinct observers that contributed to the edge. Edges seen
	// by a single observer are discounted to 1/3 weight; edges seen by ≥3
	// observers saturate at full weight. This stacks with the geo-rejection
	// filter merged for #1228 to give two independent lines of defense
	// against cross-region prefix-collision pollution. Backward-compatible
	// with the persistence format: legacy edges with empty Observers sets
	// fall back to single-observer weight.
	if graph != nil && len(contextPubkeys) > 0 {
		type scored struct {
			idx   int
			score float64
			count int // observation count of the best-scoring edge
		}
		now := time.Now()
		// PERF (#1247): hoist per-context work out of the candidate loop.
		// The previous shape ran graph.Neighbors(ToLower(ctxPK)) and
		// re-lowercased candPK on every (cand, ctxPK) pair, then used
		// strings.EqualFold to compare two already-lowercased pubkeys.
		// At analytics scale (5k+ contextPubkeys, ~30k resolveHop calls)
		// this dominated computeAnalyticsTopology / computeAnalyticsRF
		// CPU time (37% / 55% of those endpoints respectively per
		// pprof). The new shape:
		//   1. Lowercases ctx pubkeys at most once per call (skipped
		//      entirely when the input is already lowercased — the
		//      common case for analytics callers that go through
		//      buildHopContextPubkeys).
		//   2. Lowercases candidate pubkeys at most once per call and
		//      uses raw == comparisons against NeighborEdge.NodeA/NodeB
		//      (which makeEdgeKey already lowercases).
		//   3. Loops outer-ctx / inner-edge / matched-cand-lookup. The
		//      previous shape was outer-cand / inner-ctx / inner-edge,
		//      which called graph.Neighbors(ctxPK) — taking the graph
		//      RLock each time — once per (cand, ctx) pair instead of
		//      once per ctx.
		lowerCtx := contextPubkeys
		needLower := false
		for _, p := range contextPubkeys {
			if hasUpperASCII(p) {
				needLower = true
				break
			}
		}
		if needLower {
			lowerCtx = make([]string, len(contextPubkeys))
			for i, p := range contextPubkeys {
				lowerCtx[i] = strings.ToLower(p)
			}
		}
		candPKs := make([]string, len(candidates))
		bestScores := make([]float64, len(candidates))
		bestCounts := make([]int, len(candidates))
		needLowerCand := false
		for i, c := range candidates {
			if hasUpperASCII(c.PublicKey) {
				needLowerCand = true
				break
			}
			candPKs[i] = c.PublicKey
		}
		if needLowerCand {
			for i, c := range candidates {
				candPKs[i] = strings.ToLower(c.PublicKey)
			}
		}
		candByPK := make(map[string]int, len(candidates))
		for i, pk := range candPKs {
			candByPK[pk] = i
		}
		for _, ctxPK := range lowerCtx {
			for _, e := range graph.Neighbors(ctxPK) {
				if e.Ambiguous {
					continue
				}
				otherPK := e.NodeA
				if otherPK == ctxPK {
					otherPK = e.NodeB
				}
				ci, ok := candByPK[otherPK]
				if !ok {
					continue
				}
				s := e.Score(now) * e.Confidence()
				if s > bestScores[ci] {
					bestScores[ci] = s
					bestCounts[ci] = e.Count
				}
			}
		}
		var scores []scored
		for i, s := range bestScores {
			if s > 0 {
				scores = append(scores, scored{i, s, bestCounts[i]})
			}
		}

		if len(scores) >= 1 {
			// Sort descending
			for i := 0; i < len(scores)-1; i++ {
				for j := i + 1; j < len(scores); j++ {
					if scores[j].score > scores[i].score {
						scores[i], scores[j] = scores[j], scores[i]
					}
				}
			}
			best := scores[0]
			// Require both score ratio ≥ 3× AND minimum observations (mirrors
			// disambiguate() in neighbor_graph.go which checks affinityMinObservations).
			if best.count >= affinityMinObservations &&
				(len(scores) == 1 || best.score >= affinityConfidenceRatio*scores[1].score) {
				return &candidates[best.idx], "neighbor_affinity", best.score
			}
			// Scores too close — fall through to lower-priority strategies
		}
	}

	// Priority 2: Geographic proximity (if context pubkeys have GPS and candidates have GPS)
	if len(contextPubkeys) > 0 {
		// Find GPS positions of context nodes from the prefix map or candidates
		// We need nodeInfo for context pubkeys — look them up
		var contextLat, contextLon float64
		var contextGPSCount int
		for _, ctxPK := range contextPubkeys {
			ctxLower := strings.ToLower(ctxPK)
			if infos, ok := pm.m[ctxLower]; ok && len(infos) == 1 && infos[0].HasGPS {
				contextLat += infos[0].Lat
				contextLon += infos[0].Lon
				contextGPSCount++
			}
		}
		if contextGPSCount > 0 {
			contextLat /= float64(contextGPSCount)
			contextLon /= float64(contextGPSCount)

			bestIdx := -1
			bestDist := math.MaxFloat64
			for i, cand := range candidates {
				if !cand.HasGPS {
					continue
				}
				d := geoDistApprox(contextLat, contextLon, cand.Lat, cand.Lon)
				if d < bestDist {
					bestDist = d
					bestIdx = i
				}
			}
			if bestIdx >= 0 {
				return &candidates[bestIdx], "geo_proximity", 0
			}
		}
	}

	// Priority 3: GPS preference. Among GPS-having candidates, prefer the one
	// with the highest observation count (recent/active evidence) rather than
	// slice/DB-insertion order. Ties on count are broken by lexicographically
	// smallest PublicKey for full determinism. See #1197.
	bestGPSIdx := -1
	for i := range candidates {
		if !candidates[i].HasGPS {
			continue
		}
		if bestGPSIdx < 0 || betterByObsCount(&candidates[i], &candidates[bestGPSIdx]) {
			bestGPSIdx = i
		}
	}
	if bestGPSIdx >= 0 {
		return &candidates[bestGPSIdx], "gps_preference", 0
	}

	// Priority 4: Fallback — pick the candidate with the highest observation
	// count (no GPS available on any candidate). Avoids slice-order
	// arbitrariness. Ties on count are broken by lexicographically smallest
	// PublicKey. Method label "observation_count_fallback" — the previous
	// "first_match" was misleading after the tier-4 algorithm changed in
	// PR #1198 (adversarial r1 #2).
	bestIdx := 0
	for i := 1; i < len(candidates); i++ {
		if betterByObsCount(&candidates[i], &candidates[bestIdx]) {
			bestIdx = i
		}
	}
	return &candidates[bestIdx], "observation_count_fallback", 0
}

// betterByObsCount reports whether candidate a should beat b under the
// tier-3/4 selection rule: higher ObservationCount wins; ties go to the
// lexicographically smaller PublicKey for determinism. Pointer receivers
// avoid value-copying nodeInfo (string + 2 floats + time.Time + int) on
// the hot resolve path. See #1197 (adversarial r1 #6, carmack r1 #4).
func betterByObsCount(a, b *nodeInfo) bool {
	if a.ObservationCount != b.ObservationCount {
		return a.ObservationCount > b.ObservationCount
	}
	return a.PublicKey < b.PublicKey
}

// geoDistApprox returns an approximate distance between two lat/lon points
// (equirectangular approximation, sufficient for relative comparison).
func geoDistApprox(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180 * math.Cos((lat1+lat2)/2*math.Pi/180)
	return math.Sqrt(dLat*dLat + dLon*dLon)
}

func parsePathJSON(pathJSON string) []string {
	if pathJSON == "" || pathJSON == "[]" {
		return nil
	}
	var hops []string
	if json.Unmarshal([]byte(pathJSON), &hops) != nil {
		return nil
	}
	return hops
}

// --- Subpaths Analytics ---

// --- Subpath Detail ---
