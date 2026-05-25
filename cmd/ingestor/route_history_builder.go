package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// RouteHistoryBuilderInterval is how often the ingestor derives route-history
// edge events from observations. The server aggregates this table directly.
const RouteHistoryBuilderInterval = 60 * time.Second

type routeHistoryEdgeRow struct {
	obsID       int64
	hopIndex    int
	bucketStart int64
	a, b        string
	hash        string
	lastSeen    string
}

func (s *Store) StartRouteHistoryBuilder(interval time.Duration) func() {
	if interval <= 0 {
		interval = RouteHistoryBuilderInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})

	initialSince := time.Now().Add(-7 * 24 * time.Hour).Unix()
	if n, err := s.buildAndPersistRouteHistoryEdges(initialSince); err != nil {
		log.Printf("[route-history-build] initial build error: %v", err)
	} else {
		log.Printf("[route-history-build] initial build: %d edge events inserted", n)
	}
	if n, err := s.pruneRouteHistoryEdges(8); err != nil {
		log.Printf("[route-history-build] initial prune error: %v", err)
	} else if n > 0 {
		log.Printf("[route-history-build] initial prune removed %d old edge events", n)
	}

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		lastBuildAt := time.Now().Add(-2 * interval)
		lastPruneAt := time.Now()
		for {
			select {
			case <-t.C:
				sinceUnix := lastBuildAt.Add(-10 * time.Second).Unix()
				lastBuildAt = time.Now()
				if n, err := s.buildAndPersistRouteHistoryEdges(sinceUnix); err != nil {
					log.Printf("[route-history-build] tick error: %v", err)
				} else if n > 0 {
					log.Printf("[route-history-build] %d edge events inserted", n)
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

	return func() {
		stopOnce.Do(func() { close(stop) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Store) buildAndPersistRouteHistoryEdges(sinceUnix int64) (int, error) {
	prefixIdx, err := buildPrefixIndex(s.db)
	if err != nil {
		return 0, fmt.Errorf("build prefix index: %w", err)
	}

	query := `SELECT o.id, t.hash, COALESCE(o.resolved_path, ''), COALESCE(o.path_json, ''), t.first_seen, o.timestamp
		FROM observations o
		JOIN transmissions t ON t.id = o.transmission_id
		WHERE o.timestamp > ?
		  AND (
			(o.resolved_path IS NOT NULL AND o.resolved_path != '' AND o.resolved_path != '[]')
			OR (o.path_json IS NOT NULL AND o.path_json != '' AND o.path_json != '[]')
		  )`
	rows, err := s.db.Query(query, sinceUnix)
	if err != nil {
		return 0, fmt.Errorf("scan observations: %w", err)
	}
	defer rows.Close()

	edges := make([]routeHistoryEdgeRow, 0, 256)
	for rows.Next() {
		var obsID, epochTs int64
		var hash, resolvedPath, pathJSON, firstSeen string
		if err := rows.Scan(&obsID, &hash, &resolvedPath, &pathJSON, &firstSeen, &epochTs); err != nil {
			continue
		}
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
			edges = append(edges, routeHistoryEdgeRow{
				obsID: obsID, hopIndex: i, bucketStart: bucketStart,
				a: a, b: b, hash: hash, lastSeen: lastSeen,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(edges) == 0 {
		return 0, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO route_history_edges
		(observation_id, hop_index, bucket_start, node_a, node_b, packet_hash, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	inserted := 0
	for _, e := range edges {
		res, err := stmt.Exec(e.obsID, e.hopIndex, e.bucketStart, e.a, e.b, e.hash, e.lastSeen)
		if err != nil {
			return 0, fmt.Errorf("insert edge: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

func (s *Store) pruneRouteHistoryEdges(maxAgeDays int) (int64, error) {
	if maxAgeDays <= 0 {
		maxAgeDays = 8
	}
	cutoff := time.Now().UTC().Add(-time.Duration(maxAgeDays) * 24 * time.Hour).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM route_history_edges WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func parseRouteHistoryPath(resolvedPath, pathJSON string, idx prefixIndex) []string {
	if path := parseStringPathArray(resolvedPath); len(path) >= 2 {
		out := make([]string, 0, len(path))
		for _, hop := range path {
			h := strings.ToLower(strings.TrimSpace(hop))
			if h != "" {
				out = append(out, h)
			}
		}
		return out
	}
	raw := parseStringPathArray(pathJSON)
	if len(raw) < 2 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, hop := range raw {
		resolved, ok := resolvePrefix(idx, hop)
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, resolved)
	}
	return out
}

func parseStringPathArray(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(s), &arr) == nil {
		return arr
	}
	var nullable []*string
	if json.Unmarshal([]byte(s), &nullable) != nil {
		return nil
	}
	out := make([]string, 0, len(nullable))
	for _, p := range nullable {
		if p == nil {
			out = append(out, "")
		} else {
			out = append(out, *p)
		}
	}
	return out
}
