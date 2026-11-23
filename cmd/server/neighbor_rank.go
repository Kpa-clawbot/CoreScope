package main

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limits for GET /api/nodes/neighbor-rank.
const (
	neighborRankMaxQuery   = 64
	neighborRankMaxOffset  = 1000000
	neighborRankMaxLimit   = 200
	neighborRankDefaultLim = 50
)

var neighborRankRoles = map[string]bool{"repeater": true, "room": true, "companion": true, "sensor": true}

// neighborRankRow is one entry of the view plus its lowercased name, so a q
// filter does no per-request allocation.
type neighborRankRow struct {
	NeighborRankEntry
	nameLower string
}

// neighborRankView is the visible ranked population for one degree snapshot
// and one set of visibility rules. Immutable once built; shared by the Reach
// card and the leaderboard.
type neighborRankView struct {
	snap *degreeSnapshot
	gen  string // nodeVisibilityGen the view was built under
	// nodes identifies the node-cache slice the view was built from (a cache
	// refresh allocates a new slice), so a rename takes effect after one.
	nodes    *nodeInfo
	nodesLen int
	rows     []neighborRankRow
	pos      map[string]int // pubkey → index in rows
}

// rankOf is the pubkey's placement, 0 when it is not in the view.
func (v *neighborRankView) rankOf(pubkey string) int {
	if i, ok := v.pos[pubkey]; ok {
		return v.rows[i].Rank
	}
	return 0
}

// buildNeighborRankView ranks every pubkey with degree >= 1 that has a row in
// nodes and passes visibleNode (the /api/nodes rule): degree descending,
// pubkey ascending, ties sharing the first placement (1, 1, 3). A pubkey with
// no nodes row is never ranked: its name cannot be checked against the hidden
// prefixes, so it fails closed.
func buildNeighborRankView(snap *degreeSnapshot, gen string, nodes []nodeInfo, visibleNode func(*nodeInfo) bool) *neighborRankView {
	info := make(map[string]*nodeInfo, len(nodes))
	for i := range nodes {
		info[strings.ToLower(nodes[i].PublicKey)] = &nodes[i]
	}
	rows := make([]neighborRankRow, 0, len(snap.deg))
	for pk, d := range snap.deg {
		n := info[pk]
		if d < 1 || n == nil || !visibleNode(n) {
			continue
		}
		rows = append(rows, neighborRankRow{
			NeighborRankEntry: NeighborRankEntry{Pubkey: pk, Name: n.Name, Role: n.Role, Neighbors: d},
			nameLower:         strings.ToLower(n.Name),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Neighbors != rows[j].Neighbors {
			return rows[i].Neighbors > rows[j].Neighbors
		}
		return rows[i].Pubkey < rows[j].Pubkey
	})
	pos := make(map[string]int, len(rows))
	for i := range rows {
		if i > 0 && rows[i].Neighbors == rows[i-1].Neighbors {
			rows[i].Rank = rows[i-1].Rank
		} else {
			rows[i].Rank = i + 1
		}
		pos[rows[i].Pubkey] = i
	}
	return &neighborRankView{snap: snap, gen: gen, nodes: firstNode(nodes), nodesLen: len(nodes), rows: rows, pos: pos}
}

// getNeighborRankView returns the rank view for the current degree snapshot
// and visibility rules, or nil when there is no snapshot. The build runs
// without holding degreeMu (a concurrent herd may build twice; the last
// writer wins).
func (s *Server) getNeighborRankView(ctx context.Context) *neighborRankView {
	snap := s.getDegreeSnapshot(ctx)
	if snap == nil {
		return nil
	}
	gen := s.nodeVisibilityGen()
	var nodes []nodeInfo
	if s.store != nil {
		nodes, _ = s.store.getCachedNodesAndPM()
	}
	s.reach.degreeMu.Lock()
	cur := s.reach.rankView
	s.reach.degreeMu.Unlock()
	if cur != nil && cur.snap == snap && cur.gen == gen && cur.nodes == firstNode(nodes) && cur.nodesLen == len(nodes) {
		return cur
	}
	view := buildNeighborRankView(snap, gen, nodes, s.visibleNodeInfo())
	if s.reach.beforeRankStore != nil {
		s.reach.beforeRankStore()
	}
	s.reach.degreeMu.Lock()
	// Keep a view only while its snapshot is still current, so a slow build
	// for an old snapshot cannot overwrite a newer one.
	if s.reach.degreeSnap == snap {
		s.reach.rankView = view
	}
	s.reach.degreeMu.Unlock()
	return view
}

func (s *Server) handleNeighborRank(w http.ResponseWriter, r *http.Request) {
	qp := r.URL.Query()
	q := strings.ToLower(strings.TrimSpace(qp.Get("q")))
	if len(q) > neighborRankMaxQuery {
		writeError(w, http.StatusBadRequest, "q too long: max 64 characters")
		return
	}
	role := qp.Get("role")
	if role != "" && !neighborRankRoles[role] {
		writeError(w, http.StatusBadRequest, "invalid role: expected repeater, room, companion or sensor")
		return
	}
	offset, limit := 0, neighborRankDefaultLim
	if v := qp.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > neighborRankMaxOffset {
			writeError(w, http.StatusBadRequest, "invalid offset: expected an integer from 0 to 1000000")
			return
		}
		offset = n
	}
	if v := qp.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > neighborRankMaxLimit {
			writeError(w, http.StatusBadRequest, "invalid limit: expected an integer from 1 to 200")
			return
		}
		limit = n
	}

	focus := strings.ToLower(strings.TrimSpace(qp.Get("focus")))
	if qp.Has("focus") && !isHexPubkey(focus) {
		writeError(w, http.StatusBadRequest, "invalid focus: expected 64 hex chars")
		return
	}

	view := s.getNeighborRankView(r.Context())
	if view == nil {
		writeError(w, http.StatusServiceUnavailable, "neighbour ranking unavailable")
		return
	}

	// One pass: collect matches' view indexes so focus can locate its
	// position before the page is cut.
	matches := make([]int32, 0, len(view.rows))
	focusAt := -1
	for i := range view.rows {
		row := &view.rows[i]
		if role != "" && row.Role != role {
			continue
		}
		if q != "" && !strings.Contains(row.nameLower, q) && !strings.HasPrefix(row.Pubkey, q) {
			continue
		}
		if focus != "" && row.Pubkey == focus {
			focusAt = len(matches)
		}
		matches = append(matches, int32(i))
	}
	if focusAt >= 0 {
		offset = focusAt / limit * limit
	}
	entries := make([]NeighborRankEntry, 0, limit)
	for k := offset; k < len(matches) && len(entries) < limit; k++ {
		entries = append(entries, view.rows[matches[k]].NeighborRankEntry)
	}
	total := len(matches)
	writeJSON(w, NeighborRankResponse{
		Entries: entries, Total: total, Offset: offset, Ranked: len(view.rows),
		SnapshotAt: view.snap.at.UTC().Format(time.RFC3339),
	})
}

// firstNode identifies one node-cache build: the cache is replaced as a whole
// slice on refresh, so its first element's address (with its length) changes
// whenever the cache does. nil for an empty cache.
func firstNode(nodes []nodeInfo) *nodeInfo {
	if len(nodes) == 0 {
		return nil
	}
	return &nodes[0]
}
