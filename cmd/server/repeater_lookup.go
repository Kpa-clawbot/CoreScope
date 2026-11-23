package main

import (
	"database/sql"
	"errors"
	"strings"
)

// RepeaterKnown reports whether pubkey (lowercase) is a known node, and
// whether its role is repeater. It looks in nodes first, then in
// inactive_nodes when that table exists. Read-only.
func (db *DB) RepeaterKnown(pubkey string) (found bool, isRepeater bool, err error) {
	if db == nil || db.conn == nil {
		return false, false, errors.New("repeater lookup: no database")
	}
	tables := []string{"nodes"}
	if db.inactiveNodesPresent() {
		tables = append(tables, "inactive_nodes")
	}
	for _, tbl := range tables {
		var isRep bool
		err := db.conn.QueryRow(
			`SELECT lower(COALESCE(role, '')) = 'repeater' FROM `+tbl+` WHERE public_key = ? LIMIT 1`,
			pubkey).Scan(&isRep)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return false, false, err
		}
		return true, isRep, nil
	}
	return false, false, nil
}

// InactiveNodeBasics reads name, role and last_seen of the given pubkeys
// from inactive_nodes (nodes retention moved there), keyed by lowercase
// pubkey. Empty when the table does not exist. Read-only.
func (db *DB) InactiveNodeBasics(pubkeys []string) (map[string]notifyNode, error) {
	out := make(map[string]notifyNode, len(pubkeys))
	if !db.inactiveNodesPresent() {
		return out, nil
	}
	for i := 0; i < len(pubkeys); i += notifyNodeChunk {
		chunk := pubkeys[i:min(i+notifyNodeChunk, len(pubkeys))]
		args := make([]any, len(chunk))
		for j, pk := range chunk {
			args[j] = pk
		}
		rows, err := db.conn.Query(`SELECT public_key, COALESCE(name, ''), COALESCE(role, ''), COALESCE(last_seen, '')
			FROM inactive_nodes WHERE public_key IN (`+sqlPlaceholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n notifyNode
			if err := rows.Scan(&n.Pubkey, &n.Name, &n.Role, &n.LastSeen); err != nil {
				rows.Close()
				return nil, err
			}
			n.Pubkey = strings.ToLower(n.Pubkey)
			out[n.Pubkey] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
