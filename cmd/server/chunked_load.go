package main

// Chunked startup load + early HTTP readiness for issue #1009.
//
// Design:
//   * LoadChunked paginates transmissions in id-ordered chunks of
//     `chunkSize` (default 10000 via Config.DBLoadChunkSize). After the
//     first chunk is merged into the store, FirstChunkReady is closed.
//     main.go binds the HTTP listener on that signal and serves
//     partial data while remaining chunks stream in the background.
//   * loadStatusMiddleware stamps X-CoreScope-Load-Status on every
//     response: "loading; progress=<rows>" until LoadComplete()
//     reports true, then "ready". Dashboards and probes can read the
//     header without parsing JSON.
//   * OnChunkLoaded registers a per-chunk callback for progress
//     logging / tests.
//
// Concurrency: each chunk acquires s.mu.Lock() ONLY while merging the
// chunk's rows into store-shared maps. SQLite reads run lock-free so
// HTTP handlers (which take s.mu.RLock) stay responsive.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

// dbLoadConfig is the server-package alias for dbconfig.LoadConfig (#1009).
type dbLoadConfig = dbconfig.LoadConfig

// DBLoadChunkSize returns the configured chunk size for chunked
// startup load (config: db.load.chunkSize), or 10000 default (#1009).
func (c *Config) DBLoadChunkSize() int {
	return c.DB.GetLoadChunkSize()
}

// chunkedLoadState holds the runtime gates for LoadChunked. It lives
// on PacketStore via embedded fields — see store.go additions in the
// same commit.

// FirstChunkReady returns a channel closed once the first chunk has
// been merged into the store, signalling the HTTP listener can bind.
func (s *PacketStore) FirstChunkReady() <-chan struct{} {
	s.chunkedLoadInit()
	return s.firstChunkReady
}

// LoadComplete reports whether LoadChunked has finished all chunks.
func (s *PacketStore) LoadComplete() bool {
	return s.loadComplete.Load()
}

// LoadProgress reports the number of transmission rows processed by
// the in-flight (or completed) LoadChunked call.
func (s *PacketStore) LoadProgress() int64 {
	return s.loadProgressRows.Load()
}

// OnChunkLoaded registers a callback fired once per chunk after that
// chunk has been merged into the store. The callback receives the
// number of transmission rows in that chunk and the running total.
// Multiple registrations chain.
func (s *PacketStore) OnChunkLoaded(fn func(rowsThisChunk, totalRows int)) {
	s.chunkedLoadInit()
	s.chunkCBMu.Lock()
	defer s.chunkCBMu.Unlock()
	s.chunkCallbacks = append(s.chunkCallbacks, fn)
}

// chunkedLoadInit lazily initialises the readiness channel + callback
// list under a mutex so concurrent first callers don't race.
func (s *PacketStore) chunkedLoadInit() {
	s.chunkInitOnce.Do(func() {
		s.firstChunkReady = make(chan struct{})
	})
}

func (s *PacketStore) signalFirstChunk() {
	if s.firstChunkSignaled.CompareAndSwap(false, true) {
		close(s.firstChunkReady)
	}
}

func (s *PacketStore) fireChunkCallbacks(rowsThisChunk, totalRows int) {
	s.chunkCBMu.Lock()
	cbs := append([]func(int, int){}, s.chunkCallbacks...)
	s.chunkCBMu.Unlock()
	for _, cb := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[store] OnChunkLoaded callback panic: %v", r)
				}
			}()
			cb(rowsThisChunk, totalRows)
		}()
	}
}

// LoadChunked streams transmissions + observations from SQLite into
// the in-memory store in id-ordered chunks of `chunkSize` rows. Pass
// 0 to use the default (10000).
//
// After the first chunk is merged, FirstChunkReady is closed and the
// HTTP listener may bind. Remaining chunks stream while handlers run
// against partially-populated data; loadStatusMiddleware advertises
// loading status until LoadComplete() returns true.
//
// Re-entrancy: LoadChunked is NOT safe to call concurrently with
// itself on the same PacketStore — it resets loadComplete /
// loadProgressRows and mutates store-shared maps under s.mu. In
// production it is invoked exactly once from main.go boot. Tests that
// open a fresh store per test are also safe. If a future caller needs
// repeat or concurrent loads, add a top-level mutex first.
func (s *PacketStore) LoadChunked(chunkSize int) error {
	if chunkSize <= 0 {
		chunkSize = 10000
	}
	// Startup-ordering invariant (PR #1643 R1 munger #2). Mirror the
	// guard in Load() so the production async path also fast-fails when
	// neighbor_edges has rows but the graph is missing. See Load() for
	// the full rationale.
	if neighborEdgesTableExists(s.db.conn) && s.graph.Load() == nil {
		panic("packet store LoadChunked(): neighbor_edges table has rows but s.graph is nil — graph must be loaded before packet load (see main.go #1643 invariant)")
	}
	s.chunkedLoadInit()
	// Reset state for repeat calls in tests.
	s.loadComplete.Store(false)
	s.loadProgressRows.Store(0)

	// On any return — error OR success — unblock listeners that gate on
	// the readiness signal so an empty/failed DB does not deadlock the
	// caller. Note: loadComplete is set on the success path only (see
	// the end of this function) so probes do NOT see ready=true after a
	// failed load.
	defer s.signalFirstChunk()

	t0 := time.Now()

	// Build the retention/memory filter the legacy Load() uses so
	// behavior is preserved when callers migrate from Load → LoadChunked.
	// Built against the `t2` alias used inside the chunk subquery so we
	// don't need brittle post-hoc string rewrites.
	var loadConditions []string
	hotCutoffHours := s.retentionHours
	if s.hotStartupHours > 0 {
		hotCutoffHours = s.hotStartupHours
	}
	var hotCutoffStr string
	if hotCutoffHours > 0 {
		hotCutoffStr = time.Now().UTC().Add(-time.Duration(hotCutoffHours * float64(time.Hour))).Format(time.RFC3339)
		loadConditions = append(loadConditions, fmt.Sprintf("t2.first_seen >= '%s'", hotCutoffStr))
	}

	// COUNT honours the same retention/hot-startup filter the chunk
	// loop applies, so the logged "DB total" matches the rows the
	// loop will actually walk. Use a `t2` alias to share the WHERE
	// builder above. If the count fails (e.g. empty DB, locked WAL),
	// fall through with -1 — it's only used for the post-load log line.
	totalInDB := -1
	countSQL := "SELECT COUNT(*) FROM transmissions t2"
	if len(loadConditions) > 0 {
		countSQL += " WHERE " + strings.Join(loadConditions, " AND ")
	}
	if err := s.db.conn.QueryRow(countSQL).Scan(&totalInDB); err != nil {
		totalInDB = -1
	}

	// Memory cap honoured by clamping the maximum cursor walk.
	var maxPackets int64
	if s.maxMemoryMB > 0 {
		avgBytes := int64(1000)
		if sample := estimateStoreTxBytesTypical(10); sample > avgBytes {
			avgBytes = sample
		}
		maxPackets = (int64(s.maxMemoryMB) * 1048576) / avgBytes
		if maxPackets < 1000 {
			maxPackets = 1000
		}
	}

	chunkIdx := 0
	totalLoaded := 0
	// Start the id cursor BELOW the minimum possible row id so the
	// first chunk's `t2.id > cursorID` predicate includes id=0. The
	// e2e fixture seed for issue #1486 inserts the grouped-packet row
	// with id=0 (so it sorts LAST in the default packets view via
	// `ORDER BY id DESC` / oldest first_seen). Seeding the cursor at
	// 0 silently excluded that row, leaving the page with no
	// tr[data-hash] and timing out the playwright wait. Legacy Load()
	// had no id cursor and loaded id=0 unconditionally — we restore
	// that semantic by starting one below SQLite's minimum rowid (-1).
	var cursorID int64 = -1

	// Relay-hop fallback inputs, fetched ONCE before the chunk-query loop.
	// getCachedNodesAndPM issues its own DB query, so calling it while a
	// chunk cursor is open would deadlock on a single-connection SQLite
	// pool. resolved_path is never persisted post-#1287, so scanAndMergeChunk
	// re-resolves relay hops from path_json using these snapshots.
	// PR #1643 R1 munger #1: cold load uses unique_prefix-only gate, so
	// the neighbor graph is no longer consulted here (affinity-tier
	// resolution against ≤168h-old observations would silently mis-attribute).
	s.mu.RLock()
	_, relayPM := s.getCachedNodesAndPM()
	s.mu.RUnlock()
	var coldLoadAmbiguousHopsSkipped int

	for {
		conds := append([]string{}, loadConditions...)
		conds = append(conds, fmt.Sprintf("t2.id > %d", cursorID))
		whereClause := "WHERE " + strings.Join(conds, " AND ")

		rpCol := ""
		if s.db.hasResolvedPath {
			rpCol = ", o.resolved_path"
		}
		obsRawHexCol := ""
		if s.db.hasObsRawHex {
			obsRawHexCol = ", o.raw_hex"
		}

		var chunkSQL string
		if s.db.isV3 {
			chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
					t.payload_type, t.payload_version, t.decoded_json,
					o.id, obs.id, obs.name, COALESCE(obs.iata, ''), o.direction,
					o.snr, o.rssi, o.score, o.path_json, strftime('%Y-%m-%dT%H:%M:%fZ', o.timestamp, 'unixepoch')` + obsRawHexCol + rpCol + `
				FROM (SELECT * FROM transmissions t2 ` + whereClause + ` ORDER BY t2.id ASC LIMIT ` + fmt.Sprintf("%d", chunkSize) + `) AS t
				LEFT JOIN observations o ON o.transmission_id = t.id
				LEFT JOIN observers obs ON obs.rowid = o.observer_idx
				ORDER BY t.id ASC, o.timestamp DESC`
		} else {
			chunkSQL = `SELECT t.id, t.raw_hex, t.hash, t.first_seen, t.route_type,
					t.payload_type, t.payload_version, t.decoded_json,
					o.id, o.observer_id, o.observer_name, COALESCE(obs.iata, ''), o.direction,
					o.snr, o.rssi, o.score, o.path_json, o.timestamp` + obsRawHexCol + rpCol + `
				FROM (SELECT * FROM transmissions t2 ` + whereClause + ` ORDER BY t2.id ASC LIMIT ` + fmt.Sprintf("%d", chunkSize) + `) AS t
				LEFT JOIN observations o ON o.transmission_id = t.id
				LEFT JOIN observers obs ON obs.id = o.observer_id
				ORDER BY t.id ASC, o.timestamp DESC`
		}

		rows, err := s.db.conn.Query(chunkSQL)
		if err != nil {
			return fmt.Errorf("chunk %d: query: %w", chunkIdx, err)
		}

		chunkTxCount, lastID, err := s.scanAndMergeChunk(rows, relayPM, &coldLoadAmbiguousHopsSkipped)
		rows.Close()
		if err != nil {
			return fmt.Errorf("chunk %d: scan: %w", chunkIdx, err)
		}

		if chunkTxCount == 0 {
			break
		}

		cursorID = lastID
		totalLoaded += chunkTxCount
		chunkIdx++
		s.loadProgressRows.Store(int64(totalLoaded))
		s.signalFirstChunk()
		s.fireChunkCallbacks(chunkTxCount, totalLoaded)

		if maxPackets > 0 && int64(totalLoaded) >= maxPackets {
			break
		}
		if chunkTxCount < chunkSize {
			break
		}
	}

	// Post-load: pick best observation, build indexes — same shape as
	// legacy Load().
	s.mu.Lock()
	for _, tx := range s.packets {
		pickBestObservation(tx)
		s.indexByNode(tx)
	}
	// Restore the "s.packets sorted oldest-first by FirstSeen" invariant
	// that legacy Load() got for free from "ORDER BY t.first_seen ASC".
	// LoadChunked walks chunks in id-ASC order so the slice ends up
	// id-ordered, which only equals first_seen-ordered when ids and
	// timestamps are correlated. After tools/freshen-fixture.sh (or any
	// real-world out-of-order ingest) they're not, leaving
	// s.packets[0].FirstSeen pointing at the newest row — which then
	// poisons oldestLoaded below and routes legitimate in-memory queries
	// to the SQL fallback. GetTimestamps (store.go) and QueryPackets
	// both rely on this invariant. See PR #1596 / mobile e2e regression.
	sort.SliceStable(s.packets, func(i, j int) bool {
		return s.packets[i].FirstSeen < s.packets[j].FirstSeen
	})
	s.buildSubpathIndex()
	s.buildPathHopIndex()
	s.buildDistanceIndex()
	if s.hotStartupHours > 0 {
		s.oldestLoaded = hotCutoffStr
	} else if len(s.packets) > 0 {
		s.oldestLoaded = s.packets[0].FirstSeen
	}
	s.loaded = true
	s.mu.Unlock()

	// #1009 / PR #1596: flip the subpath + pathHop ready flags now that
	// the chunk loader has built both indexes synchronously above.
	// Without this, WaitIndexesReady (used by
	// StartRepeaterEnrichmentRecomputer at boot) blocks for up to
	// repeaterEnrichmentPrewarmWait (60s), delaying HTTP listener bind
	// past CI's 30s /api/healthz deadline.
	s.markIndexesReadySync()

	elapsed := time.Since(t0)
	log.Printf("[store] LoadChunked: %d transmissions (%d observations) across %d chunk(s) in %v (chunkSize=%d, DB total=%d)",
		totalLoaded, s.totalObs, chunkIdx, elapsed, chunkSize, totalInDB)
	if coldLoadAmbiguousHopsSkipped > 0 {
		log.Printf("[store] LoadChunked: skipped %d ambiguous-prefix relay hops (unique_prefix gate, PR #1643 R1)",
			coldLoadAmbiguousHopsSkipped)
	}
	s.loadMultibyteCapFromDB()
	// Mark complete on the success path only — see the function-level
	// defer above for why this is NOT in a deferred call. Probes that
	// read LoadComplete()==true after a failed load would otherwise
	// see ready=true for a half-loaded store.
	s.loadComplete.Store(true)
	return nil
}

// scanAndMergeChunk consumes one chunk's rows under s.mu.Lock and
// returns the number of distinct transmissions seen + the max
// transmission id (cursor for the next chunk).
func (s *PacketStore) scanAndMergeChunk(rows *sql.Rows, relayPM *prefixMap, coldLoadAmbiguousHopsSkipped *int) (int, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	hopsSeen := make(map[string]bool)
	seenTxIDs := make(map[int]bool)
	var maxID int64

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
			log.Printf("[store] LoadChunked scan error: %v", err)
			continue
		}

		if int64(txID) > maxID {
			maxID = int64(txID)
		}
		seenTxIDs[txID] = true

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

			rpStr := nullStrVal(resolvedPathStr)
			if rpStr != "" {
				rp := unmarshalResolvedPath(rpStr)
				pks := extractResolvedPubkeys(rp)
				s.indexResolvedPathHops(tx, pks, hopsSeen)
			} else if relayPM != nil && obsPJ != "" && obsPJ != "[]" {
				// resolved_path is NULL on live (since #1287 relay data is
				// persisted as neighbor_edges, not per-observation). Re-resolve
				// relay-hop attribution from path_json so relay nodes keep their
				// analytics history across a restart instead of rebuilding only
				// from post-restart live traffic. relayPM is passed in from
				// LoadChunked (fetched before any chunk cursor opened).
				// byNode ONLY — see the Load() counterpart for why the
				// resolved_path/path-hop indexes must NOT be populated here.
				// PR #1643 R1 munger #1: unique_prefix-only gate.
				rp := resolvePathForObsColdLoad(obsPJ, obsIDStr, tx, relayPM, coldLoadAmbiguousHopsSkipped)
				for _, pk := range extractResolvedPubkeys(rp) {
					s.addToByNode(tx, pk)
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
	if err := rows.Err(); err != nil {
		return len(seenTxIDs), maxID, err
	}
	return len(seenTxIDs), maxID, nil
}

// loadStatusMiddleware sets X-CoreScope-Load-Status on every response.
// While LoadChunked is in flight the header reports
// "loading; progress=<rows>"; after completion it reports "ready".
// The header is set BEFORE calling the next handler so probes can
// observe it on any response (including streaming bodies).
func loadStatusMiddleware(s *PacketStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s != nil && s.LoadComplete() {
			w.Header().Set("X-CoreScope-Load-Status", "ready")
		} else if s != nil {
			w.Header().Set("X-CoreScope-Load-Status",
				fmt.Sprintf("loading; progress=%d", s.LoadProgress()))
		} else {
			w.Header().Set("X-CoreScope-Load-Status", "loading")
		}
		next.ServeHTTP(w, r)
	})
}

// --- runtime state stitched into PacketStore via store_chunked.go ---

// Forward declarations of the new PacketStore fields used above. The
// actual struct fields live in store.go; placing them here as a
// reminder keeps the chunked-load surface easy to audit.
var _ = sync.Once{}
var _ atomic.Bool
