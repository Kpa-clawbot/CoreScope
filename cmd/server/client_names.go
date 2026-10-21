package main

import (
	"log"
	"strings"
)

// Prefix resolution over two name sources: nodes (adverts this instance heard)
// and client_node_names (a repeater's own answer to ANON_REQ_TYPE_OWNER,
// relayed by a mobile companion because this instance had no name for it; the
// ingestor writes it, cmd/ingestor/client_names.go). A repeater asked that way
// usually has no nodes row at all, so the second table is a source of
// candidates, not only of names.

// clientNodeNamesPresent reports whether client_node_names exists. The ingestor
// creates it, and on the first run of a build that adds it the server can start
// before the ingestor has, so an absent table is re-probed until it appears
// (same reason as declaredRegionsTablePresent).
func (db *DB) clientNodeNamesPresent() bool {
	if db.clientNodeNamesTable.Load() {
		return true
	}
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'client_node_names'`).Scan(&n); err != nil || n == 0 {
		return false
	}
	db.clientNodeNamesTable.Store(true)
	return true
}

// clientNameHeard is the SQL condition (alias c for client_node_names) that a
// reported target was heard directly at least once: a discover or advert
// reception keyed by its full pubkey, or by its 8-byte prefix when no other
// known pubkey (nodes, inactive_nodes, client_node_names) starts with that
// prefix. Any companion the broker accepts can report a name for any pubkey,
// so without this one message could invent a repeater, or make a real node's
// short prefix ambiguous. Without the uniqueness check, a made-up target that
// shares a heard repeater's 8-byte prefix would borrow that repeater's
// receptions and pass. The condition needs client_receptions; see
// clientNamesUsable.
//
// The prefix ranges rely on lowercase hex keys, as in lookupPrefixes: "g"
// sorts after "f", so [p, p+"g") is exactly the keys starting with p.
func (db *DB) clientNameHeard() string {
	sharedWith := func(table, col string) string {
		return `EXISTS (SELECT 1 FROM ` + table + ` o WHERE o.` + col + ` >= substr(c.target, 1, 16)
			AND o.` + col + ` < substr(c.target, 1, 16) || 'g' AND o.` + col + ` <> c.target)`
	}
	shared := sharedWith("nodes", "public_key") + ` OR ` + sharedWith("client_node_names", "target")
	if db.inactiveNodesPresent() {
		shared += ` OR ` + sharedWith("inactive_nodes", "public_key")
	}
	return `EXISTS (SELECT 1 FROM client_receptions r
	WHERE r.src IN ('discover','advert') AND (r.heard_key = c.target
		OR (r.heard_key = substr(c.target, 1, 16) AND NOT (` + shared + `))))`
}

// inactiveNodesPresent reports whether inactive_nodes exists; an older or
// partial database may lack it. Re-probed while absent.
func (db *DB) inactiveNodesPresent() bool {
	if db.inactiveNodesTable.Load() {
		return true
	}
	if !db.tablePresent("inactive_nodes") {
		return false
	}
	db.inactiveNodesTable.Store(true)
	return true
}

// clientNamesUsable reports whether client_node_names can contribute: the
// table exists, and so does client_receptions, which clientNameHeard reads.
func (db *DB) clientNamesUsable() bool {
	return db.clientNodeNamesPresent() && db.tablePresent("client_receptions")
}

// prefixHit is what one prefix matched. matches counts distinct pubkeys across
// both sources, capped at 2 by the per-source LIMIT 2: unique when it is 1.
// pubkey and name are set only when unique. name is the nodes name when that
// is non-empty, otherwise the newest client-reported name.
type prefixHit struct {
	matches int
	pubkey  string
	name    string
}

// lookupPrefixes resolves validated hex prefixes (or full pubkeys) to their
// candidates, one query per chunk. Each key contributes a LIMIT 2 lookup per
// source, so per-key work stays bounded. A key whose chunk failed is absent
// from the result (logged), and callers treat it as unresolved.
//
// client_node_names: the bare `name` beside MAX(observed_at) is SQLite's
// documented bare-column rule, so it is the name of the newest observation
// for that target. Ordering by observed_at, not ingested_at, keeps a drive
// buffered offline from overwriting a fresher answer.
func (s *Server) lookupPrefixes(keys []string) map[string]prefixHit {
	res := make(map[string]prefixHit, len(keys))
	withClient := s.db.clientNamesUsable()
	var heard string
	if withClient {
		heard = s.db.clientNameHeard()
	}
	// SQLITE_MAX_COMPOUND_SELECT is 500 by default; two terms per key keeps a
	// chunk of 200 at 400.
	const chunk = 200
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]
		parts := make([]string, 0, len(batch)*2)
		args := make([]interface{}, 0, len(batch)*4)
		for _, k := range batch {
			// Parameterized: the prefix flows in as bound args, never
			// interpolated. The per-prefix LIMIT 2 lives in a subquery because a
			// bare LIMIT on a UNION ALL term is a SQLite syntax error.
			parts = append(parts, "SELECT * FROM (SELECT ? AS pfx, public_key AS pk, COALESCE(name,'') AS nm, 0 AS src FROM nodes WHERE public_key LIKE ? LIMIT 2)")
			args = append(args, k, k+"%")
			if withClient {
				// A range, not LIKE: LIKE on a case-sensitive column cannot use
				// idx_cnn_target and scans it per key. target is lowercase hex
				// (the ingestor lowercases it), and "g" sorts after "f", so
				// [k, k+"g") is exactly the keys starting with k.
				lk := strings.ToLower(k)
				parts = append(parts, "SELECT pfx, pk, nm, src FROM (SELECT ? AS pfx, target AS pk, name AS nm, 1 AS src, MAX(observed_at) FROM client_node_names c WHERE target >= ? AND target < ? AND "+heard+" GROUP BY target LIMIT 2)")
				args = append(args, k, lk, lk+"g")
			}
		}
		rows, err := s.db.conn.Query(strings.Join(parts, " UNION ALL "), args...)
		if err != nil {
			// Don't fail the request, but don't fail silently either: a swallowed
			// error here presents as "every name is ambiguous" with no signal.
			log.Printf("WARN lookupPrefixes: %v", err)
			continue
		}
		type agg struct {
			nodeName   map[string]string // pubkey -> nodes name
			clientName map[string]string // pubkey -> newest client name
		}
		acc := map[string]*agg{}
		for rows.Next() {
			var pfx, pk, nm string
			var src int
			if err := rows.Scan(&pfx, &pk, &nm, &src); err != nil {
				continue
			}
			a := acc[pfx]
			if a == nil {
				a = &agg{nodeName: map[string]string{}, clientName: map[string]string{}}
				acc[pfx] = a
			}
			if src == 0 {
				a.nodeName[pk] = nm
			} else {
				a.clientName[pk] = nm
			}
		}
		rows.Close()
		for _, k := range batch {
			a := acc[k]
			if a == nil {
				res[k] = prefixHit{}
				continue
			}
			pks := map[string]bool{}
			for pk := range a.nodeName {
				pks[pk] = true
			}
			for pk := range a.clientName {
				pks[pk] = true
			}
			hit := prefixHit{matches: len(pks)}
			if len(pks) == 1 {
				for pk := range pks {
					hit.pubkey = pk
				}
				hit.name = a.nodeName[hit.pubkey]
				if hit.name == "" {
					hit.name = a.clientName[hit.pubkey]
				}
			}
			res[k] = hit
		}
	}
	return res
}

// visible applies the #15 / #1181 parity rule to a unique hit: a blacklisted
// node or a hidden-prefix name is reported as not found, whichever source the
// name came from.
func (s *Server) visible(h prefixHit) bool {
	return h.matches == 1 && !s.cfg.IsBlacklisted(h.pubkey) && !s.cfg.IsNameHidden(h.name)
}
