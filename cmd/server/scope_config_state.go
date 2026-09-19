package main

import (
	"log"
	"strings"
	"sync/atomic"
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

// isScopeWildcard recognises both spellings of the flood wildcard. The
// ingestor treats "*" and "#*" as the same thing (cmd/ingestor/region_keys.go),
// and the two readers of a declared list have to agree: counting the prefixed
// form as a named region turns a fully configured repeater into
// ScopeConfigNoUnscoped, which reads as a fault, and does it on one page only.
//
// Shared with the scope audit deliberately. The map and that page classify the
// same declared list, so the rule lives in one place and
// TestScopeConfigStateAgreesWithTheAudit holds them to it.
func isScopeWildcard(region string) bool {
	return region == "*" || region == "#*"
}

// splitDeclaredRegions reads one declared regions_csv into its named regions
// and whether the flood wildcard was present. It is the single reading of a
// declared list: the Scope Audit's declaredRegions, /api/nodes'
// scope_config_state and its declared_regions field (#1862) all come from it,
// so a repeater cannot be listed under one set of names on the audit page and
// another on the map. Named regions are normScope'd, because regions_csv
// arrives with and without the leading '#' depending on the collector, and
// the wildcard is never a named region (see isScopeWildcard).
func splitDeclaredRegions(csv string) (named []string, wildcard bool) {
	all := splitRegionsCSV(csv)
	named = make([]string, 0, len(all))
	for _, rgn := range all {
		if isScopeWildcard(rgn) {
			wildcard = true
			continue
		}
		named = append(named, normScope(rgn))
	}
	return named, wildcard
}

// enrichNodeDeclaredScope sets the two declared-regions fields on a
// repeater/room node: scope_config_state (#2001) and declared_regions
// (#1862). Shared by the list and detail handlers so the node page and the
// map read one repeater the same way.
//
// declared_regions is set only when the node has a declared answer. An empty
// list means it answered and named no region; an absent field means it was
// never asked. The map's region filter treats neither as evidence that the
// repeater lacks a region.
//
// declared_regions_truncated is set, to true, only when that answer was
// flagged as cut off: the list is then partial, which the Scope Audit also
// shows. Omitted otherwise, since a source that does not record truncation
// cannot vouch for a complete list either.
func enrichNodeDeclaredScope(node map[string]interface{}, answer declaredAnswer, hasDeclared bool, transportedScopes []string) {
	node["scope_config_state"] = nodeScopeConfigState(answer.RegionsCSV, hasDeclared, transportedScopes)
	if hasDeclared {
		named, _ := splitDeclaredRegions(answer.RegionsCSV)
		node["declared_regions"] = named
		if answer.Truncated {
			node["declared_regions_truncated"] = true
		}
	}
}

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
		named, wildcard := splitDeclaredRegions(declaredCSV)
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

// declaredAnswer is one node's newest declared-regions answer as the node
// endpoints read it: the raw regions_csv, and whether the collector flagged
// the list as cut off. Truncated is only ever true for a source that records
// it (see AllCurrentDeclaredRegions); false means "not flagged", not "known
// complete".
type declaredAnswer struct {
	RegionsCSV string
	Truncated  bool
}

// declaredRegionsCSV maps target pubkey to its newest declared answer,
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
func (s *Server) declaredRegionsCSV() (map[string]declaredAnswer, bool) {
	if !s.db.hasConfiguredScope && !s.db.declaredRegionsTablePresent() {
		return nil, false
	}

	s.declaredRegionsMu.Lock()
	cached, at := s.declaredRegionsCache, s.declaredRegionsAt
	s.declaredRegionsMu.Unlock()
	if cached != nil && time.Since(at) < declaredRegionsTTL {
		return cached, true
	}

	// The query runs outside the mutex, and singleflight collapses the herd at
	// the TTL boundary into one execution. Holding the lock across it would
	// serialise every concurrent /api/nodes request behind a full scan of nodes
	// plus a window function over node_declared_regions — on the busiest
	// endpoint in the server, every 30s. AGENTS.md: copy under the lock,
	// process outside it.
	v, err, _ := s.declaredRegionsSF.Do("declared-regions", func() (interface{}, error) {
		// Double-check inside the flight: a previous winner may have stored a
		// fresh map between this caller's read above and its arrival here, and
		// singleflight frees the key as soon as that winner returns. Without
		// this, a TTL boundary under load can run the query a second time for
		// nothing. Same idiom as the stats cache in routes.go.
		s.declaredRegionsMu.Lock()
		fresh, freshAt := s.declaredRegionsCache, s.declaredRegionsAt
		s.declaredRegionsMu.Unlock()
		if fresh != nil && time.Since(freshAt) < declaredRegionsTTL {
			return fresh, nil
		}

		rows, qerr := s.db.AllCurrentDeclaredRegions()
		if qerr != nil {
			return nil, qerr
		}
		atomic.AddInt64(&s.declaredRegionsQueries, 1)
		out := make(map[string]declaredAnswer, len(rows))
		for _, r := range rows {
			out[strings.ToLower(r.Target)] = declaredAnswer{RegionsCSV: r.RegionsCSV, Truncated: r.Truncated}
		}
		s.declaredRegionsMu.Lock()
		// Cached maps are handed to concurrent readers and never written again.
		s.declaredRegionsCache = out
		s.declaredRegionsAt = time.Now()
		s.declaredRegionsMu.Unlock()
		return out, nil
	})
	if err != nil {
		log.Printf("[nodes] declared-regions lookup failed, scope_config_state omitted: %v", err)
		return nil, false
	}
	return v.(map[string]declaredAnswer), true
}
