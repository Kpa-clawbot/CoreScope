package main

import "log"

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
			if rgn == "*" {
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

// declaredRegionsCSV maps target pubkey to its newest declared regions_csv,
// merged across every collector this database carries. The bool is false when
// the lookup itself failed, which callers must not confuse with an empty map:
// no rows means nobody has answered, a failed query means we do not know, and
// labelling every repeater "never asked" on a failed query would invent a
// finding out of a database error.
func (s *Server) declaredRegionsCSV() (map[string]string, bool) {
	rows, err := s.db.AllCurrentDeclaredRegions()
	if err != nil {
		log.Printf("[nodes] declared-regions lookup failed, scope_config_state omitted: %v", err)
		return nil, false
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Target] = r.RegionsCSV
	}
	return out, true
}
