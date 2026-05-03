package main

import (
	"database/sql"
	"fmt"
	"sync"
)

// rwCache holds a process-wide cached RW connection per database path.
// Instead of opening and closing a new RW connection on every call to openRW,
// we cache a single *sql.DB (which internally manages one connection due to
// SetMaxOpenConns(1)). This eliminates repeated open/close overhead for
// vacuum, prune, persist operations that run frequently (#921).
var rwCache = struct {
	mu    sync.Mutex
	conns map[string]*sql.DB
}{conns: make(map[string]*sql.DB)}

// cachedRW returns a cached read-write connection for the given dbPath.
// The connection is created on first call and reused thereafter.
// Callers MUST NOT call Close() on the returned *sql.DB.
func cachedRW(dbPath string) (*sql.DB, error) {
	rwCache.mu.Lock()
	defer rwCache.mu.Unlock()

	if db, ok := rwCache.conns[dbPath]; ok {
		return db, nil
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	rwCache.conns[dbPath] = db
	return db, nil
}

// closeRWCache closes all cached RW connections (for tests/shutdown).
func closeRWCache() {
	rwCache.mu.Lock()
	defer rwCache.mu.Unlock()
	for k, db := range rwCache.conns {
		db.Close()
		delete(rwCache.conns, k)
	}
}

// rwCacheLen returns the number of cached connections (for testing).
func rwCacheLen() int {
	rwCache.mu.Lock()
	defer rwCache.mu.Unlock()
	return len(rwCache.conns)
}
