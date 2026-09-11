package main

import (
	"log"
	"strings"
	"time"
)

// The /api/nodes scope_config_state field (#2001). The Scope Audit answers
// "which repeaters declare a region they are not forwarding", and to do that
// it lists only repeaters that have answered a declared-regions request at
// all — 232 of 1289 repeaters on one live instance. /api/nodes has to say
// something about the other 1057, because the map colours every repeater it
// draws and "absent from a different endpoint" is not a colour.
//
// So the field carries the audit's four declared states unchanged, plus two
// that only make sense for a node with no declared answer:
//
//   - ScopeConfigObserved: never answered, but we have seen it forward
//     scoped traffic, so it demonstrably has a region configured. We just do
//     not know which ones, nor whether it also forwards unscoped floods.
//     Unlike the four declared states, this one is not a pure function of
//     stored data: it reads transported_scopes, which the in-memory packet
//     store accumulates over its retention window, so a repeater can move
//     between Observed and None after a restart with nothing having changed
//     in the network.
//   - ScopeConfigNone: never answered, and nothing scoped has been observed
//     through it either.
//
// ScopeConfigNone is NOT a finding. Firmware drops scoped floods for regions
// it holds no key for, so a repeater with no region config and a repeater
// nobody has sent scoped traffic past look identical from here. The value
// means "we have not been told", and the UI has to say so.
const (
	ScopeConfigObserved = "observed" // no declared answer; observed forwarding scoped traffic
	ScopeConfigNone     = "none"     // no declared answer; no scoped traffic observed either
)

// nodeScopeConfigState classifies one node for the scope_config_state field.
//
// declaredCSV is the node's most recent declared-regions answer (the raw
// regions_csv) and hasDeclared says whether such an answer exists at all —
// the two cannot be collapsed into "declaredCSV != \"\"", because answering
// with an empty list is itself a state (ScopeConfigNoFlood: nothing is
// flood-allowed, not even unscoped traffic) and is different from never
// having been asked.
//
// A declared answer always wins over transportedScopes. The answer is the
// repeater's own word about its configuration; traffic is what we happened to
// see. Where the two disagree, reporting that contradiction is the Scope
// Audit's job, and re-labelling the node here would hide it.
func nodeScopeConfigState(declaredCSV string, hasDeclared bool, transportedScopes []string) string {
	if hasDeclared {
		var named []string
		wildcard := false
		for _, rgn := range splitRegionsCSV(declaredCSV) {
			// Both spellings of the wildcard. The ingestor treats "*" and "#*"
			// as the same thing (cmd/ingestor/region_keys.go:94); counting the
			// prefixed form as a named region would turn a fully configured
			// repeater into ScopeConfigNoUnscoped, which reads as a fault.
			if rgn == "*" || rgn == "#*" {
				wildcard = true
				continue
			}
			named = append(named, rgn)
		}
		return scopeAuditConfigState(named, wildcard)
	}
	if len(transportedScopes) > 0 {
		return ScopeConfigObserved
	}
	return ScopeConfigNone
}

// declaredRegionsTTL bounds how stale the per-request declared map may be.
// A declared answer changes when an observer reports or a drive is uploaded,
// which is hours apart per node, so a few seconds of staleness is invisible —
// while /api/nodes is the busiest endpoint in the server and the map pages
// through it. The scope-audit handler caches the same lookup for 30s
// (scope_audit.go), and using the same figure keeps the two pages from
// disagreeing for longer than either is stale on its own.
const declaredRegionsTTL = 30 * time.Second

// declaredRegionsCSV maps target pubkey to its newest declared regions_csv,
// merged across every collector this database carries, keyed lowercase.
//
// Keys are lowercased because the two sides of the join disagree by design:
// nodes.public_key is lowercased by migration, while node_declared_regions is
// filled by an external collector whose casing this repo does not control. The
// audit already lowercases the declared target before joining; a map that did
// not would classify the same repeater differently on two pages.
//
// The bool is false when the answer is unknown rather than absent, which
// callers must not confuse with an empty map. Two things make it unknown: the
// query failed, or this database carries neither source, in which case "no
// repeater has answered" is a statement the schema cannot support. An empty map
// with ok=true means the sources exist and nobody has answered yet, which IS a
// finding and is what ScopeConfigNone reports.
func (s *Server) declaredRegionsCSV() (map[string]string, bool) {
	if !s.db.hasConfiguredScope && !s.db.hasDeclaredRegionsTable {
		return nil, false
	}

	s.declaredRegionsMu.Lock()
	defer s.declaredRegionsMu.Unlock()
	if s.declaredRegionsCache != nil && time.Since(s.declaredRegionsAt) < declaredRegionsTTL {
		return s.declaredRegionsCache, true
	}

	rows, err := s.db.AllCurrentDeclaredRegions()
	if err != nil {
		log.Printf("[nodes] declared-regions lookup failed, scope_config_state omitted: %v", err)
		return nil, false
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Target)] = r.RegionsCSV
	}
	// Cached maps are handed to concurrent readers and never written again.
	s.declaredRegionsCache = out
	s.declaredRegionsAt = time.Now()
	return out, true
}
