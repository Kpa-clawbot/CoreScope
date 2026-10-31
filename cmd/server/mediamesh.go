package main

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MediaMesh: radio2, a second LoRa profile that Keymind Cascade repeaters,
// Wadamesh TAK standalones and MeshCore Open TAK use for photos and voice.
// The ingestor records which observations were carried on it
// (observation_mediamesh) and which nodes run it (node_mediamesh); schema and
// the observer contract in internal/dbschema/mediamesh.go, writes in
// cmd/ingestor/mediamesh.go. The server keeps both in memory, read-only
// (#1283), the same way as radio_history.go:
//
//   - the poller calls refreshMediaMesh on every tick, before ingesting, so a
//     new observation's flag is known before it is broadcast;
//   - a full reload every mediaMeshFullReload picks up what the cheap checks
//     cannot see: rows deleted by retention.
//
// Output is additive: `"mediamesh": true` on an observation or packet only
// when set, and `"mediamesh": {...}` on a node only when it runs MediaMesh,
// so nothing changes for anything else.
//
// Cost (rule 0): one set lookup per observation serialised, one map lookup
// per node. The set holds only MediaMesh observations, the map only nodes
// with evidence.

const (
	mediaMeshFullReload = 10 * time.Minute
	// A node's MediaMesh mark counts while its newest evidence is this recent.
	mediaMeshEvidenceWindow = 7 * 24 * time.Hour
)

// MediaMeshNode is what /api/nodes and node detail report for a node that
// runs MediaMesh.
type MediaMeshNode struct {
	Mode     string `json:"mode"`            // rx | rxtx
	Radio    string `json:"radio,omitempty"` // "MHz,kHz,SF,CR" when reported
	Source   string `json:"source"`          // status | neighbor | heard
	LastSeen string `json:"last_seen"`
}

var (
	mediaMeshMu       sync.RWMutex
	mediaMeshObs      = map[int]struct{}{}
	mediaMeshMaxObsID int64
	mediaMeshNodes    atomic.Pointer[map[string]MediaMeshNode]
	mediaMeshNodesTS  string     // newest last_seen loaded (incremental watermark)
	mediaMeshRefresh  sync.Mutex // one refresh at a time
)

// observationMediaMesh reports whether one observation was carried on radio2.
func observationMediaMesh(obsID int) bool {
	mediaMeshMu.RLock()
	_, ok := mediaMeshObs[obsID]
	mediaMeshMu.RUnlock()
	return ok
}

// txMediaMesh reports whether any observation of tx was carried on radio2.
func txMediaMesh(tx *StoreTx) bool {
	if tx == nil {
		return false
	}
	mediaMeshMu.RLock()
	defer mediaMeshMu.RUnlock()
	if len(mediaMeshObs) == 0 {
		return false
	}
	return txMediaMeshLocked(tx)
}

// txMediaMeshLocked is txMediaMesh for a caller that holds mediaMeshMu.
func txMediaMeshLocked(tx *StoreTx) bool {
	for _, o := range tx.Observations {
		if _, ok := mediaMeshObs[o.ID]; ok {
			return true
		}
	}
	return false
}

// markMediaMesh adds `"mediamesh": true` to an API object when on.
func markMediaMesh(m map[string]interface{}, on bool) {
	if on {
		m["mediamesh"] = true
	}
}

// nodeMediaMeshAt returns the node's MediaMesh mark: nil when it has none, it
// last said radio2 is off, or the evidence is older than the window.
func nodeMediaMeshAt(pubkey string, now time.Time) *MediaMeshNode {
	p := mediaMeshNodes.Load()
	if p == nil || pubkey == "" {
		return nil
	}
	n, ok := (*p)[strings.ToLower(pubkey)]
	if !ok || n.Mode == "off" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, n.LastSeen); err == nil && now.Sub(t) > mediaMeshEvidenceWindow {
		return nil
	}
	return &n
}

// EnrichNodeWithMediaMesh adds `"mediamesh": {...}` to a node that runs it.
func EnrichNodeWithMediaMesh(node map[string]interface{}, pubkey string) {
	if n := nodeMediaMeshAt(pubkey, time.Now()); n != nil {
		node["mediamesh"] = n
	} else {
		delete(node, "mediamesh")
	}
}

func loadMediaMeshObservations(conn *sql.DB, sinceID int64) (map[int]struct{}, int64, error) {
	out := map[int]struct{}{}
	maxID := sinceID
	if !tableExists(conn, "observation_mediamesh") {
		return out, maxID, nil
	}
	rows, err := conn.Query(`SELECT observation_id FROM observation_mediamesh
		WHERE observation_id > ? ORDER BY observation_id`, sinceID)
	if err != nil {
		return nil, sinceID, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, sinceID, err
		}
		out[int(id)] = struct{}{}
		maxID = id
	}
	return out, maxID, rows.Err()
}

func loadMediaMeshNodes(conn *sql.DB, since string) (map[string]MediaMeshNode, string, error) {
	out := map[string]MediaMeshNode{}
	newest := since
	if !tableExists(conn, "node_mediamesh") {
		return out, newest, nil
	}
	rows, err := conn.Query(`SELECT public_key, mode, COALESCE(radio, ''), source, last_seen
		FROM node_mediamesh WHERE last_seen > ?`, since)
	if err != nil {
		return nil, since, err
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var n MediaMeshNode
		if err := rows.Scan(&pk, &n.Mode, &n.Radio, &n.Source, &n.LastSeen); err != nil {
			return nil, since, err
		}
		n.Radio = canonicalRadio(n.Radio)
		out[strings.ToLower(pk)] = n
		if n.LastSeen > newest {
			newest = n.LastSeen
		}
	}
	return out, newest, rows.Err()
}

// refreshMediaMesh brings the in-memory MediaMesh state up to date. full=false
// is the per-tick check: observation ids above the last one seen, and node
// rows stamped after the newest one loaded.
func refreshMediaMesh(conn *sql.DB, full bool) {
	if conn == nil {
		return
	}
	mediaMeshRefresh.Lock()
	defer mediaMeshRefresh.Unlock()

	mediaMeshMu.RLock()
	sinceObs, sinceTS := mediaMeshMaxObsID, mediaMeshNodesTS
	mediaMeshMu.RUnlock()
	if full {
		sinceObs, sinceTS = 0, ""
	}

	ids, maxID, err := loadMediaMeshObservations(conn, sinceObs)
	if err != nil {
		log.Printf("[mediamesh] observations: %v", err)
		return
	}
	nodes, newest, err := loadMediaMeshNodes(conn, sinceTS)
	if err != nil {
		log.Printf("[mediamesh] nodes: %v", err)
		return
	}

	mediaMeshMu.Lock()
	if full {
		mediaMeshObs = ids
	} else {
		for id := range ids {
			mediaMeshObs[id] = struct{}{}
		}
	}
	mediaMeshMaxObsID = maxID
	mediaMeshNodesTS = newest
	mediaMeshMu.Unlock()

	if full || len(nodes) > 0 {
		merged := nodes
		if !full {
			if cur := mediaMeshNodes.Load(); cur != nil {
				merged = make(map[string]MediaMeshNode, len(*cur)+len(nodes))
				for k, v := range *cur {
					merged[k] = v
				}
				for k, v := range nodes {
					merged[k] = v
				}
			}
		}
		mediaMeshNodes.Store(&merged)
	}
}

// runMediaMesh loads the state now and fully again every mediaMeshFullReload
// until ctx ends. The poller does the per-tick part.
func runMediaMesh(ctx context.Context, conn *sql.DB) {
	refreshMediaMesh(conn, true)
	go func() {
		t := time.NewTicker(mediaMeshFullReload)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshMediaMesh(conn, true)
			}
		}
	}()
}
