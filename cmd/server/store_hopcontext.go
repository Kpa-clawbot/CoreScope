// store_hopcontext.go — hop-context pubkey resolution helpers.

package main

import "strings"

// computeDistancesForTx computes distance records for a single transmission.
// buildHopContextPubkeys collects context pubkeys for hop disambiguation:
// the originator/sender pubkey plus any unambiguous-prefix anchors in the
// path (single-candidate prefixes — strong context). Used by callers of
// pm.resolveWithContext to light up tiers 1 and 2 of the resolver. See #1197.
//
// Returned pubkeys are de-duplicated and lowercased.
func buildHopContextPubkeys(tx *StoreTx, pm *prefixMap) []string {
	if tx == nil || pm == nil {
		return nil
	}
	seen := make(map[string]struct{}, 16)
	out := make([]string, 0, 16)
	add := func(pk string) {
		if pk == "" {
			return
		}
		l := strings.ToLower(pk)
		if _, ok := seen[l]; ok {
			return
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}

	// Sender / originator pubkey from decoded payload. Use the cached
	// ParsedDecoded() (sync.Once-gated) instead of re-unmarshaling — the
	// helper is hot (3 distance sites + analytics topology, all 30k+ tx
	// loops). See #1197 (carmack/adversarial r1).
	if dec := tx.ParsedDecoded(); dec != nil {
		if pk, ok := dec["pubKey"].(string); ok {
			add(pk)
		}
	}

	// Observer pubkey, where available. ObserverID is the observers.id PRIMARY
	// KEY from the MQTT topic — it is NOT guaranteed to be a node pubkey hex
	// (some observers register with arbitrary string ids like "myobserver").
	// Guard against polluting the context with non-pubkey strings: include
	// only when it parses as hex AND is long enough to plausibly be a pubkey
	// prefix. The full prefix-map lookup would also be acceptable, but the
	// hex+length check is O(len) and avoids one map probe per tx on a hot
	// path. See #1197 (adversarial r1 #4).
	if obs := tx.ObserverID; obs != "" && len(obs) >= 4 && isHexLower(strings.ToLower(obs)) {
		add(obs)
	}

	// Unambiguous-prefix anchors: any hop in the path whose prefix has exactly
	// one candidate is a strong context signal.
	for _, hop := range txGetParsedPath(tx) {
		h := strings.ToLower(hop)
		if cands, ok := pm.m[h]; ok && len(cands) == 1 {
			add(cands[0].PublicKey)
		}
	}

	return out
}

// isHexLower reports whether s consists only of [0-9a-f] (assumes already
// lowercased by caller). Used to guard ObserverID before adding it to the
// hop-disambiguation context, since ObserverID is a free-form observers.id
// and may not be a node pubkey hex.
func isHexLower(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// buildAggregateHopContextPubkeys gathers context across many txs for hot
// loops that resolve hops outside any per-tx scope (subpath/topology
// aggregations). Caller passes the slice of txs to consider; we union the
// per-tx contexts with de-dup. Used by call sites that read from precomputed
// indices (s.spIndex, s.spTxIndex) or that resolve user-supplied hops.
//
// Result is order-independent in semantics; iteration order is deterministic
// only modulo Go's map iteration (acceptable — the resolver's tier-2 averages
// GPS positions and tier-3 picks the lex-smallest pubkey on ties, so context
// order does not affect the chosen candidate).
func buildAggregateHopContextPubkeys(txs []*StoreTx, pm *prefixMap) []string {
	if len(txs) == 0 || pm == nil {
		return nil
	}
	seen := make(map[string]struct{}, 32)
	var out []string
	for _, tx := range txs {
		for _, pk := range buildHopContextPubkeys(tx, pm) {
			if _, ok := seen[pk]; ok {
				continue
			}
			seen[pk] = struct{}{}
			out = append(out, pk)
		}
	}
	return out
}

// hopResolverPerTx returns (resolveHop, setContext). The cache is allocated
// once and cleared between txs; setContext rebinds the per-tx context. Used
// by all per-tx distance/topology loops to avoid 4× duplicate closure
// definitions and per-tx map allocation. See #1197 (adversarial r1 #7,
// carmack r1 #3).
//
// CONCURRENCY: NOT safe for concurrent use. The returned closures share
// mutable captured state — `contextPubkeys` is reassigned by setContext and
// read by resolveHop, and `hopCache` is mutated by both (resolveHop writes
// on miss, setContext clears wholesale). Callers MUST invoke both functions
// from a single goroutine for the lifetime of the (resolveHop, setContext)
// pair. If a future caller fans out per-tx work across goroutines, allocate
// a fresh resolver pair per goroutine. See #1199 item 4.
func (s *PacketStore) hopResolverPerTx(pm *prefixMap) (resolveHop func(string) *nodeInfo, setContext func([]string)) {
	hopCache := make(map[string]*nodeInfo, 16)
	var contextPubkeys []string
	// Hoist the atomic graph.Load() out of the per-hop closure body so we do
	// one Load per resolver instance, not one per resolveHop call (PR #1208
	// carmack #1). Pair (resolveHop, setContext) is documented single-
	// goroutine, single-request scope — pinning the graph here matches that
	// lifetime.
	graph := s.graph.Load()
	resolveHop = func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		return r
	}
	setContext = func(ctx []string) {
		contextPubkeys = ctx
		clear(hopCache)
	}
	return resolveHop, setContext
}
