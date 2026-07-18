package packetpath

// TrustConfig controls how much confidence a path-hash prefix observation
// must carry before it is used as topology/mapping evidence (issue #1784).
//
// MeshCore path hops are hashed pubkey prefixes of 1, 2, or 3 bytes
// (firmware/src/Packet.cpp:13-18, hash_size = (pathByte>>6)+1). Shorter
// prefixes collide more often — a 1-byte hash has only 256 possible values,
// so on denser meshes a "currently unique candidate" resolution can still
// be a false positive. TrustConfig lets operators require a longer prefix
// before a mapping consumer (neighbor graph, path resolver, neighbor
// builder, path inspector) treats a resolved hop as trustworthy evidence.
//
// This does not affect raw storage: packets and paths are always stored
// exactly as received. It only gates *derived* trust — which observations
// are eligible to become neighbor-graph edges, resolved-path hops, or
// capability inferences. See the #1784 call-site audit for the full list
// of consumers this is meant to gate (follow-up PRs; not changed here).
type TrustConfig struct {
	// MinHashBytesForMapping is the minimum path-hash prefix length, in
	// bytes, required for a hop resolution to count as mapping evidence.
	// Valid wire values are 1, 2, or 3 (see hashSize above). 0/unset falls
	// back to DefaultMinHashBytesForMapping. Values above MaxHashBytes are
	// clamped by MinHashBytesOrDefault (see there) rather than silently
	// accepted, since no real prefix is ever longer than 3 bytes and an
	// unclamped typo (e.g. 99) would otherwise exclude every observation
	// without any error.
	MinHashBytesForMapping int `json:"minHashBytesForMapping,omitempty"`
}

// DefaultMinHashBytesForMapping is the conservative default threshold
// (issue #1784, operator-confirmed): 1-byte prefixes are excluded from
// mapping/topology evidence by default. Operators who prefer the prior
// trust-all behavior can opt back in with minHashBytesForMapping: 1.
const DefaultMinHashBytesForMapping = 2

// MaxHashBytes is the firmware's maximum path-hash prefix length
// (firmware/src/Packet.cpp:13-18, hash_size = (pathByte>>6)+1, valid
// wire values 1/2/3; hash_size==4 is reserved/unused). Used to clamp
// MinHashBytesForMapping so a misconfigured threshold above the
// achievable maximum doesn't silently exclude every observation.
const MaxHashBytes = 3

// MinHashBytesOrDefault returns the configured threshold, falling back to
// DefaultMinHashBytesForMapping if cfg is nil or unset (<= 0), and
// clamping to MaxHashBytes if configured higher. Without the clamp, a
// value like minHashBytesForMapping: 99 would silently accept no
// observation at all (no real prefix exceeds 3 bytes) instead of behaving
// like the intended "require the strongest evidence" (3).
func (c *TrustConfig) MinHashBytesOrDefault() int {
	if c == nil || c.MinHashBytesForMapping <= 0 {
		return DefaultMinHashBytesForMapping
	}
	if c.MinHashBytesForMapping > MaxHashBytes {
		return MaxHashBytes
	}
	return c.MinHashBytesForMapping
}

// MeetsPathTrust reports whether a path-hash observation of the given
// prefix length (in bytes) should be trusted as mapping/topology evidence
// under cfg's threshold.
//
// prefixBytes == 0 denotes an unknown-length/legacy observation — e.g.
// pre-#1638 persisted neighbor edges with no per-mode breakdown
// (NeighborEdge.CountsByMode[0] in cmd/server/neighbor_graph.go). Per
// #1784's bucket-0 policy: once the threshold requires more than the
// wire-format minimum of 1 byte (minBytes >= 2), an unknown-length
// observation cannot be proven to meet it and is excluded. At the
// trust-all threshold (minBytes <= 1) bucket 0 passes, matching pre-#1784
// behavior exactly — so this change is a no-op until an operator (or the
// new default) raises the threshold above 1.
//
// This function makes no consumer-side changes by itself (#1784 step 2 —
// config + helper only). It's the shared decision point each consumer
// (neighbor_graph.go, path_resolver.go, neighbor_builder.go,
// path_inspect.go) will call into in follow-up PRs, per the audit's DRY
// recommendation instead of each site reimplementing "prefix bytes =
// len(hex)/2" trust logic independently.
func MeetsPathTrust(prefixBytes int, cfg *TrustConfig) bool {
	minBytes := cfg.MinHashBytesOrDefault()
	if minBytes <= 1 {
		return true
	}
	if prefixBytes <= 0 {
		return false
	}
	return prefixBytes >= minBytes
}
