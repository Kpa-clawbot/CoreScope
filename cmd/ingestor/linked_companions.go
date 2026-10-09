package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Linked-only ingest (docs/specs/2026-10-08-companion-linking-design.md,
// "Linked-only ingest"). With clientRxCoverage.requireLinkedCompanion and
// userManagement.enabled, every meshcore/client/<pubkey>/... message whose
// pubkey no user linked is dropped before any client handler runs. The set
// comes from users.db, read-only, with raw SQL (AGENTS.md: the ingestor never
// imports internal/users and never writes users.db). It is a filter for
// honest clients, not a security boundary: all RX clients share one broker
// account.

const (
	// linkedCompanionsRefresh is the periodic re-read; it picks up unlinks.
	linkedCompanionsRefresh = time.Minute
	// linkedCompanionsMissGap caps the re-reads a miss may trigger.
	linkedCompanionsMissGap = 5 * time.Second
)

// linkedCompanionsQuery reads internal/users schema v6 (companion_links);
// TestLinkedCompanionsTableIsInUsersSchema checks the table still exists there.
const linkedCompanionsQuery = `SELECT pubkey FROM companion_links`

// linkedCompanionSet hands each client message the current set of linked
// pubkeys. Snapshots are never mutated after they are stored, so a hit is
// lock-free. Reads (refresh, or a miss after the gap) are serialized by mu.
type linkedCompanionSet struct {
	path string
	now  func() time.Time
	cur  atomic.Pointer[map[string]struct{}]

	mu       sync.Mutex  // guards everything below
	db       *sql.DB     // opened lazily, read-only
	lastRead time.Time   // last read attempt, for the miss gap
	reads    int         // read attempts (tests)
	failing  bool        // the last read failed; logged once per failure streak
	loaded   atomic.Bool // a read of companion_links has succeeded at least once
	noTable  bool        // warned that companion_links is missing
}

// newLinkedCompanionSet starts unloaded: until the first successful read of
// companion_links, every pubkey passes. A users.db that is missing, unreadable
// or older than companion linking must not cost the data of linked companions.
func newLinkedCompanionSet(usersDBPath string) *linkedCompanionSet {
	s := &linkedCompanionSet{path: usersDBPath, now: time.Now}
	empty := map[string]struct{}{}
	s.cur.Store(&empty)
	return s
}

// Allow reports whether pubkey (the topic segment, any case) is linked. A
// miss re-reads users.db when the last read is at least
// linkedCompanionsMissGap old, so a companion linked a moment ago is accepted
// within seconds while a flood of unknown pubkeys costs one read per gap.
func (s *linkedCompanionSet) Allow(pubkey string) bool {
	pk := strings.ToLower(strings.TrimSpace(pubkey))
	if _, ok := (*s.cur.Load())[pk]; ok {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := (*s.cur.Load())[pk]; ok { // another miss re-read meanwhile
		return true
	}
	if s.now().Sub(s.lastRead) < linkedCompanionsMissGap {
		return !s.loaded.Load()
	}
	s.readLocked()
	if !s.loaded.Load() {
		return true
	}
	_, ok := (*s.cur.Load())[pk]
	return ok
}

// refresh re-reads the set: at startup and from refreshLoop.
func (s *linkedCompanionSet) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readLocked()
}

func (s *linkedCompanionSet) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// readLocked swaps in a fresh snapshot and marks the set loaded. A missing
// companion_links table (a server older than companion linking) is warned once
// and leaves the set unloaded, so data still passes. Any other failure keeps
// the current snapshot (or pass-through, before the first success) and is
// logged once per streak.
func (s *linkedCompanionSet) readLocked() {
	s.lastRead = s.now()
	s.reads++
	keys, err := s.query()
	if err != nil && strings.Contains(err.Error(), "no such table") {
		if !s.noTable {
			log.Printf("[companions] %s has no companion_links table (a server older than companion linking); passing client data until it appears", s.path)
			s.noTable = true
		}
		return
	}
	if err != nil {
		if !s.failing {
			log.Printf("[companions] linked companions unavailable (%v); keeping the %d in force", err, len(*s.cur.Load()))
			s.failing = true
		}
		return
	}
	if s.failing {
		log.Printf("[companions] linked companions readable again from %s", s.path)
		s.failing = false
	}
	next := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		next[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	s.cur.Store(&next)
	s.loaded.Store(true)
}

func (s *linkedCompanionSet) query() ([]string, error) {
	if s.db == nil {
		if _, err := os.Stat(s.path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w at %s", errUsersDBMissing, s.path)
			}
			return nil, err
		}
		db, err := sql.Open("sqlite3", usersDBReadOnlyDSN(s.path))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		s.db = db
	}
	rows, err := s.db.Query(linkedCompanionsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

// Close closes the read-only handle, if any.
func (s *linkedCompanionSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

// allowClientPubkey applies the linked-only filter to a client topic's
// pubkey segment. A drop is counted, never logged: a flood would otherwise
// fill the log.
func (s *Store) allowClientPubkey(pubkey string) bool {
	if s.linkedCompanions == nil || s.linkedCompanions.Allow(pubkey) {
		return true
	}
	s.Stats.ClientUnlinkedDropped.Add(1)
	return false
}
