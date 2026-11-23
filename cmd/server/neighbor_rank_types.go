package main

// Response shapes for GET /api/nodes/neighbor-rank (#2100): every ranked node
// listed by neighbour count, the same rank the Reach page's Rank card shows.
//
// Rank rules (shared with the Reach card through one rank view):
//   - Neighbour count: all-time distinct neighbor_edges rows per pubkey, the
//     degree snapshot in node_reach.go.
//   - Population: pubkeys with at least one edge that pass nodeVisibility
//     (blacklist, hidden name prefixes, geo_filter), the rule /api/nodes uses.
//   - Ties share a placement (competition ranking: 1, 1, 3).
//   - Filters (q, role) narrow the list but never renumber it.
//   - Only valid pubkeys (64 hex) that have a nodes row are ranked, and only
//     edges whose both ends are valid pubkeys count toward a degree.
//
// focus=<64-hex pubkey>: when that node is in the filtered list, the server
// ignores offset and returns the limit-aligned page that contains it; Offset
// in the response is always the offset actually used.

// NeighborRankEntry is one ranked node.
type NeighborRankEntry struct {
	Rank      int    `json:"rank"`      // placement in the whole visible population
	Pubkey    string `json:"pubkey"`    // lowercase hex
	Name      string `json:"name"`      // "" when the node has no nodes row
	Role      string `json:"role"`      // "" when unknown
	Neighbors int    `json:"neighbors"` // all-time distinct neighbours
}

// NeighborRankResponse is one page of the leaderboard.
type NeighborRankResponse struct {
	Entries    []NeighborRankEntry `json:"entries"`     // never null
	Total      int                 `json:"total"`       // entries matching q/role, before paging
	Offset     int                 `json:"offset"`      // offset of Entries[0] in the filtered list
	Ranked     int                 `json:"ranked"`      // size of the visible ranked population
	SnapshotAt string              `json:"snapshot_at"` // RFC3339 time of the degree snapshot
}
