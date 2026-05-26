package main

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// DerivedEdgesBuilderInterval is the cadence for ingestor-owned derived edge
// tables that are built from observations/path data.
const DerivedEdgesBuilderInterval = 60 * time.Second

type RouteHistoryBackfillSettings struct {
	Enabled  bool
	Lookback time.Duration
	Window   time.Duration
	Pause    time.Duration
}

func DefaultRouteHistoryBackfillSettings() RouteHistoryBackfillSettings {
	return RouteHistoryBackfillSettings{
		Enabled:  true,
		Lookback: routeHistoryBackfillLookback,
		Window:   routeHistoryBackfillWindow,
		Pause:    routeHistoryBackfillPause,
	}
}

type derivedEdgesBuildOptions struct {
	Neighbor     bool
	RouteHistory bool
}

type derivedEdgesBuildResult struct {
	NeighborEdges     int
	RouteHistoryEdges int
}

// StartDerivedEdgesBuilder runs the production edge materializer. It scans the
// live observation window once and feeds both neighbor_edges and route-history
// aggregates. Historical route-history backfill remains route-only because
// neighbor_edges has no per-observation idempotency key; replaying historical
// rows into it would inflate the cumulative pair counts.
func (s *Store) StartDerivedEdgesBuilder(interval time.Duration, routeCfg RouteHistoryBackfillSettings) func() {
	if interval <= 0 {
		interval = DerivedEdgesBuilderInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	backfillDone := make(chan struct{})

	var stopOnce sync.Once
	go func() {
		defer close(done)
		initialSince := time.Now().Add(-2 * interval).Unix()
		if res, err := s.buildAndPersistDerivedEdgesWindow(initialSince, 0, derivedEdgesBuildOptions{Neighbor: true, RouteHistory: true}); err != nil {
			log.Printf("[derived-edge-build] initial build error: %v", err)
		} else {
			log.Printf("[derived-edge-build] initial build: %d neighbor edges, %d route-history edge events",
				res.NeighborEdges, res.RouteHistoryEdges)
		}
		if n, err := s.pruneRouteHistoryEdges(8); err != nil {
			log.Printf("[route-history-build] initial prune error: %v", err)
		} else if n > 0 {
			log.Printf("[route-history-build] initial prune removed %d old edge events", n)
		}

		t := time.NewTicker(interval)
		defer t.Stop()
		lastBuildAt := time.Now().Add(-2 * interval)
		lastPruneAt := time.Now()
		for {
			select {
			case <-t.C:
				sinceUnix := lastBuildAt.Add(-10 * time.Second).Unix()
				lastBuildAt = time.Now()
				if res, err := s.buildAndPersistDerivedEdgesWindow(sinceUnix, 0, derivedEdgesBuildOptions{Neighbor: true, RouteHistory: true}); err != nil {
					log.Printf("[derived-edge-build] tick error: %v", err)
				} else if res.NeighborEdges > 0 || res.RouteHistoryEdges > 0 {
					log.Printf("[derived-edge-build] %d neighbor edges, %d route-history edge events",
						res.NeighborEdges, res.RouteHistoryEdges)
				}
				if time.Since(lastPruneAt) >= 24*time.Hour {
					lastPruneAt = time.Now()
					if n, err := s.pruneRouteHistoryEdges(8); err != nil {
						log.Printf("[route-history-build] prune error: %v", err)
					} else if n > 0 {
						log.Printf("[route-history-build] pruned %d old edge events", n)
					}
				}
			case <-stop:
				return
			}
		}
	}()

	go func() {
		defer close(backfillDone)
		if !routeCfg.Enabled {
			log.Printf("[route-history-build] backfill disabled")
			return
		}
		s.backfillRouteHistoryEdgesWithSettings(stop, time.Now(), routeCfg)
	}()

	return func() {
		stopOnce.Do(func() { close(stop) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		select {
		case <-backfillDone:
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Store) buildAndPersistDerivedEdgesWindow(sinceUnix, untilUnix int64, opts derivedEdgesBuildOptions) (derivedEdgesBuildResult, error) {
	var result derivedEdgesBuildResult
	if !opts.Neighbor && !opts.RouteHistory {
		return result, nil
	}

	prefixIdx, err := buildPrefixIndex(s.db)
	if err != nil {
		return result, fmt.Errorf("build prefix index: %w", err)
	}

	query := `SELECT
			o.id,
			t.hash,
			t.payload_type,
			COALESCE(t.decoded_json, ''),
			COALESCE(t.from_pubkey, ''),
			COALESCE(o.path_json, ''),
			COALESCE(o.resolved_path, ''),
			t.first_seen,
			COALESCE(obs.id, '') AS observer_id,
			o.timestamp
		FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		LEFT JOIN observers obs ON obs.rowid = o.observer_idx
		WHERE o.timestamp > ?`
	args := []interface{}{sinceUnix}
	if untilUnix > 0 {
		query += ` AND o.timestamp <= ?`
		args = append(args, untilUnix)
	}
	if opts.RouteHistory && !opts.Neighbor {
		query += ` AND (
			(o.resolved_path IS NOT NULL AND o.resolved_path != '' AND o.resolved_path != '[]')
			OR (o.path_json IS NOT NULL AND o.path_json != '' AND o.path_json != '[]')
		  )`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result, fmt.Errorf("scan observations: %w", err)
	}
	defer rows.Close()

	neighborEdges := make([]edgeRow, 0, 256)
	routeEdges := make([]routeHistoryEdgeRow, 0, 256)
	for rows.Next() {
		var payloadType sql.NullInt64
		var obsID, epochTs int64
		var hash, decodedJSON, fromPubkey, pathJSON, resolvedPath, firstSeen, observerID string
		if err := rows.Scan(&obsID, &hash, &payloadType, &decodedJSON, &fromPubkey, &pathJSON, &resolvedPath, &firstSeen, &observerID, &epochTs); err != nil {
			continue
		}

		if opts.Neighbor {
			fromNode := lowerASCII(fromPubkey)
			if fromNode == "" {
				fromNode = lowerASCII(extractPubkeyFromAdvertJSON(decodedJSON))
			}
			isAdvert := payloadType.Valid && payloadType.Int64 == int64(payloadADVERT)
			ts := time.Unix(epochTs, 0).UTC().Format(time.RFC3339)
			observerPK := lowerASCII(observerID)
			path := parsePathArray(pathJSON)

			if len(path) == 0 {
				if isAdvert && fromNode != "" && fromNode != observerPK && observerPK != "" {
					neighborEdges = append(neighborEdges, canonEdge(fromNode, observerPK, ts))
				}
			} else {
				if isAdvert && fromNode != "" {
					if resolved, ok := resolvePrefix(prefixIdx, path[0]); ok && resolved != fromNode {
						neighborEdges = append(neighborEdges, canonEdge(fromNode, resolved, ts))
					}
				}
				if observerPK != "" {
					last := path[len(path)-1]
					if resolved, ok := resolvePrefix(prefixIdx, last); ok && resolved != observerPK {
						neighborEdges = append(neighborEdges, canonEdge(observerPK, resolved, ts))
					}
				}
			}
		}

		if opts.RouteHistory {
			path := parseRouteHistoryPath(resolvedPath, pathJSON, prefixIdx)
			if len(path) < 2 {
				continue
			}
			lastSeen := firstSeen
			if epochTs > 0 {
				lastSeen = time.Unix(epochTs, 0).UTC().Format(time.RFC3339)
			}
			bucketStart := time.Unix(epochTs, 0).UTC().Truncate(time.Hour).Unix()
			for i := 0; i < len(path)-1; i++ {
				a, b := path[i], path[i+1]
				if a == "" || b == "" || a == b {
					continue
				}
				if a > b {
					a, b = b, a
				}
				routeEdges = append(routeEdges, routeHistoryEdgeRow{
					obsID: obsID, hopIndex: i, bucketStart: bucketStart,
					a: a, b: b, hash: hash, lastSeen: lastSeen,
				})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(neighborEdges) == 0 && len(routeEdges) == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	if len(neighborEdges) > 0 {
		n, err := persistNeighborEdgesTx(tx, neighborEdges)
		if err != nil {
			return result, err
		}
		result.NeighborEdges = n
	}
	if len(routeEdges) > 0 {
		n, err := persistRouteHistoryEdgesTx(tx, routeEdges)
		if err != nil {
			return result, err
		}
		result.RouteHistoryEdges = n
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit: %w", err)
	}
	return result, nil
}

func persistNeighborEdgesTx(tx *sql.Tx, edges []edgeRow) (int, error) {
	stmt, err := tx.Prepare(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(node_a, node_b) DO UPDATE SET
		  count = count + 1,
		  last_seen = MAX(last_seen, excluded.last_seen)`)
	if err != nil {
		return 0, fmt.Errorf("prepare neighbor edge: %w", err)
	}
	defer stmt.Close()
	var firstErr error
	for _, e := range edges {
		if _, err := stmt.Exec(e.a, e.b, e.ts); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return 0, fmt.Errorf("upsert neighbor edge: %w", firstErr)
	}
	return len(edges), nil
}

func persistRouteHistoryEdgesTx(tx *sql.Tx, edges []routeHistoryEdgeRow) (int, error) {
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO route_history_edges
		(observation_id, hop_index, bucket_start, node_a, node_b, packet_hash, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare route-history edge: %w", err)
	}
	defer stmt.Close()
	aggStmt, err := tx.Prepare(`INSERT INTO route_history_edge_hourly
		(bucket_start, node_a, node_b, count, last_seen, sample1)
		VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT(bucket_start, node_a, node_b) DO UPDATE SET
			count = count + 1,
			last_seen = MAX(last_seen, excluded.last_seen),
			sample2 = CASE WHEN sample1 IS NOT excluded.sample1 AND sample2 IS NULL THEN excluded.sample1 ELSE sample2 END,
			sample3 = CASE WHEN sample1 IS NOT excluded.sample1 AND sample2 IS NOT excluded.sample1 AND sample3 IS NULL THEN excluded.sample1 ELSE sample3 END,
			sample4 = CASE WHEN sample1 IS NOT excluded.sample1 AND sample2 IS NOT excluded.sample1 AND sample3 IS NOT excluded.sample1 AND sample4 IS NULL THEN excluded.sample1 ELSE sample4 END,
			sample5 = CASE WHEN sample1 IS NOT excluded.sample1 AND sample2 IS NOT excluded.sample1 AND sample3 IS NOT excluded.sample1 AND sample4 IS NOT excluded.sample1 AND sample5 IS NULL THEN excluded.sample1 ELSE sample5 END`)
	if err != nil {
		return 0, fmt.Errorf("prepare route-history aggregate: %w", err)
	}
	defer aggStmt.Close()
	inserted := 0
	for _, e := range edges {
		res, err := stmt.Exec(e.obsID, e.hopIndex, e.bucketStart, e.a, e.b, e.hash, e.lastSeen)
		if err != nil {
			return 0, fmt.Errorf("insert route-history edge: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if _, err := aggStmt.Exec(e.bucketStart, e.a, e.b, e.lastSeen, e.hash); err != nil {
				return 0, fmt.Errorf("upsert route-history hourly edge: %w", err)
			}
			inserted += int(n)
		}
	}
	return inserted, nil
}

func (s *Store) backfillRouteHistoryEdgesWithSettings(stop <-chan struct{}, now time.Time, cfg RouteHistoryBackfillSettings) {
	s.backfillRouteHistoryEdges(stop, now, cfg.Lookback, cfg.Window, cfg.Pause)
}

func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
