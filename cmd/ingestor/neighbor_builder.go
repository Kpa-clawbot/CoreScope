package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// NeighborEdgesBuilderInterval is how often the ingestor rescans
// observations and refreshes neighbor_edges. Server reads with the
// same 60s cadence (see cmd/server/neighbor_recomputer.go); a 60s
// pulse here is sufficient to keep the snapshot fresh.
const NeighborEdgesBuilderInterval = 60 * time.Second

// payloadADVERT mirrors the constant in cmd/server/decoder.go.
// Duplicated rather than imported so the ingestor binary stays
// independent of the server package.
const payloadADVERT = 0x04

// edgeRow is one row to upsert into neighbor_edges. (a, b) is already
// canonical-ordered (a <= b).
type edgeRow struct {
	a, b, ts string
}

// StartNeighborEdgesBuilder launches the periodic builder. On each
// tick it rescans recent observations + transmissions and upserts
// derived neighbor_edges rows. Builder is the only writer to
// neighbor_edges (#1287).
//
// The function returns a stop closure immediately. Initial build runs in the
// builder goroutine so MQTT startup is not blocked by a multi-minute warmup.
//
// Perf: each tick only scans observations newer than the previous build
// time (minus a 2-interval overlap for safety). The initial build uses a
// full prune-window lookback so the table is properly warm after restart.
// This avoids the previous behaviour of scanning all observations on every
// 60 s tick — which with 938 k edge rows caused a multi-minute SQLite write
// lock that blocked packet insertion (observed in production logs).
func (s *Store) StartNeighborEdgesBuilder(interval time.Duration) func() {
	if interval <= 0 {
		interval = NeighborEdgesBuilderInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})

	var stopOnce sync.Once
	go func() {
		defer close(done)
		// Warm-up: full 5-day lookback so existing edges are preserved across
		// restarts without needing a full all-time scan. This must not block
		// StartNeighborEdgesBuilder returning; MQTT startup depends on that.
		initialSince := time.Now().Add(-5 * 24 * time.Hour).Unix()
		if n, err := s.buildAndPersistNeighborEdges(initialSince); err != nil {
			log.Printf("[neighbor-build] initial build error: %v", err)
		} else {
			log.Printf("[neighbor-build] initial build: %d edges upserted", n)
		}

		t := time.NewTicker(interval)
		defer t.Stop()
		// lastBuildAt seeds the incremental window. Subtract 2×interval so
		// the very first tick overlaps the initial build and misses nothing.
		lastBuildAt := time.Now().Add(-2 * interval)
		for {
			select {
			case <-t.C:
				// Only scan observations newer than the previous build (minus a
				// 10 s overlap to cover any clock skew or in-flight writes).
				sinceUnix := lastBuildAt.Add(-10 * time.Second).Unix()
				lastBuildAt = time.Now()
				if n, err := s.buildAndPersistNeighborEdges(sinceUnix); err != nil {
					log.Printf("[neighbor-build] tick error: %v", err)
				} else if n > 0 {
					log.Printf("[neighbor-build] %d edges upserted", n)
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

// buildAndPersistNeighborEdges scans transmissions + observations,
// extracts edge candidates (originator↔first-hop on ADVERTs;
// observer↔last-hop on all packet types) and upserts them into
// neighbor_edges. Returns count of attempted upserts.
//
// sinceUnix limits the scan to observations whose timestamp (Unix epoch)
// is strictly greater than the given value. Pass 0 to scan all time
// (used only for the synchronous warm-up on startup).
//
// Resolution of hop-prefix → full pubkey is done via a one-shot
// SELECT of (lowered) pubkey prefixes from nodes. Prefixes with
// multiple candidates are skipped (matches the conservative
// resolution rule in cmd/server/extractEdgesFromObs).
func (s *Store) buildAndPersistNeighborEdges(sinceUnix int64) (int, error) {
	res, err := s.buildAndPersistDerivedEdgesWindow(sinceUnix, 0, derivedEdgesBuildOptions{Neighbor: true})
	if err != nil {
		return 0, err
	}
	return res.NeighborEdges, nil
}

// canonEdge orders the pair so node_a <= node_b (matches the existing
// schema convention used by the loader and the bridge recomputer).
func canonEdge(a, b, ts string) edgeRow {
	if a > b {
		a, b = b, a
	}
	return edgeRow{a, b, ts}
}

// parsePathArray returns the hop strings from a path_json blob.
// Defensive against missing/invalid JSON.
func parsePathArray(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var arr []string
	if json.Unmarshal([]byte(s), &arr) != nil {
		return nil
	}
	return arr
}

// prefixIndex maps a hop prefix (lowercase) → all full pubkeys whose
// public_key starts with that prefix. Prefixes with > 1 candidate are
// considered ambiguous and skipped during resolution.
type prefixIndex map[string][]string

// buildPrefixIndex reads nodes.public_key and builds the prefix → pubkey
// map. We index every 1-byte (2 hex char) prefix length the firmware
// uses (1, 2, 3, 4, 6, 8). Memory cost is O(nodes × len(prefixLens)).
func buildPrefixIndex(db *sql.DB) (prefixIndex, error) {
	rows, err := db.Query(`SELECT public_key FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := make(prefixIndex, 1024)
	var prefixLens = []int{1 * 2, 2 * 2, 3 * 2, 4 * 2, 6 * 2, 8 * 2}
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			continue
		}
		pkLower := strings.ToLower(pk)
		for _, n := range prefixLens {
			if len(pkLower) < n {
				continue
			}
			prefix := pkLower[:n]
			idx[prefix] = append(idx[prefix], pkLower)
		}
	}
	return idx, nil
}

// resolvePrefix returns the single resolved pubkey if exactly one
// candidate matches, otherwise (zero || multiple), it returns ok=false
// (matches the conservative server-side resolver in
// cmd/server/extractEdgesFromObs).
func resolvePrefix(idx prefixIndex, hop string) (string, bool) {
	h := strings.ToLower(hop)
	candidates := idx[h]
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}
