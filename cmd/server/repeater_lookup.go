package main

import (
	"database/sql"
	"errors"
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
