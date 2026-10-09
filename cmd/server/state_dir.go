package main

import "path/filepath"

// runtimeStateDir is set once before the server's background workers start.
var runtimeStateDir = "data"

// Queue helpers use an anchor filename; it is unrelated to the database URL.
func (db *DB) statePath() string {
	dir := db.stateDir
	if dir == "" {
		dir = runtimeStateDir
	}
	return filepath.Join(dir, "meshcore")
}
