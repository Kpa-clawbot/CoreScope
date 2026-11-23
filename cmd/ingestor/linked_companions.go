package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
// lock-free. A read (refresh, or a miss after the gap) holds readMu across
// its I/O; mu is only ever held for a few field updates, never across I/O,
// so the MQTT handlers never wait on the database or users.db: a miss whose
// re-read is due while another read is in flight answers from the current
// snapshot.
type linkedCompanionSet struct {
	path   string
	now    func() time.Time
	cur    atomic.Pointer[map[string]struct{}]
	loaded atomic.Bool // a read of companion_links has succeeded at least once

	mu       sync.Mutex // guards lastRead and reads
	lastRead time.Time  // last read attempt, for the miss gap
	reads    int        // read attempts (tests)

	readMu   sync.Mutex      // held by the one read in flight; guards everything below
	db       *sql.DB         // opened lazily, read-only
	accounts *accountsReader // PostgreSQL: reads corescope_users while that schema exists
	fromFile bool            // with accounts: the last read found no accounts schema and read users.db
	failing  bool            // the last read failed; logged once per failure streak
	noTable  bool            // warned that companion_links is missing
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
// miss re-reads the links when the last read is at least
// linkedCompanionsMissGap old, so a companion linked a moment ago is accepted
// within seconds while a flood of unknown pubkeys costs one read per gap.
// Only the miss that starts a re-read waits for it (accountsReadTimeout at
// most on PostgreSQL); every other caller answers from the current snapshot.
func (s *linkedCompanionSet) Allow(pubkey string) bool {
	pk := strings.ToLower(strings.TrimSpace(pubkey))
	if s.has(pk) {
		return true
	}
	s.missRead()
	if !s.loaded.Load() {
		return true
	}
	return s.has(pk)
}

func (s *linkedCompanionSet) has(pk string) bool {
	_, ok := (*s.cur.Load())[pk]
	return ok
}

// missRead re-reads the set when the miss gap has passed and no read is in
// flight, else returns at once. TryLock never waits, so taking it under mu
// cannot deadlock against refresh (readMu, then mu).
func (s *linkedCompanionSet) missRead() {
	s.mu.Lock()
	if s.now().Sub(s.lastRead) < linkedCompanionsMissGap || !s.readMu.TryLock() {
		s.mu.Unlock()
		return
	}
	s.markReadLocked()
	s.mu.Unlock()
	defer s.readMu.Unlock()
	s.read()
}

// refresh re-reads the set: at startup and from refreshLoop. It waits for a
// read in flight rather than skipping, so it always reads.
func (s *linkedCompanionSet) refresh() {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.mu.Lock()
	s.markReadLocked()
	s.mu.Unlock()
	s.read()
}

// markReadLocked records a read attempt; s.mu is held.
func (s *linkedCompanionSet) markReadLocked() {
	s.lastRead = s.now()
	s.reads++
}

func (s *linkedCompanionSet) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// read swaps in a fresh snapshot and marks the set loaded; s.readMu is held.
// A missing companion_links table (a server older than companion linking) is
// warned once and leaves the set unloaded, so data still passes. Any other
// failure keeps the current snapshot (or pass-through, before the first
// success) and is logged once per streak.
func (s *linkedCompanionSet) read() {
	keys, err := s.query()
	if isMissingTable(err) {
		if !s.noTable {
			log.Printf("[companions] %s has no companion_links table (a server older than companion linking); passing client data until it appears", s.source())
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
		log.Printf("[companions] linked companions readable again from %s", s.source())
		s.failing = false
	}
	next := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		next[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	s.cur.Store(&next)
	s.loaded.Store(true)
}

// usePostgres reads corescope_users.companion_links through r while that
// schema exists, else users.db. The set owns r from here on and closes it in
// Close.
func (s *linkedCompanionSet) usePostgres(r *accountsReader) { s.accounts = r }

// linkedCompanionsColumns are the companion_links columns
// linkedCompanionsQuery references (plainAccountsTable checks their types).
var linkedCompanionsColumns = []string{"pubkey"}

func (s *linkedCompanionSet) source() string {
	if s.accounts != nil && !s.fromFile {
		return usersPGSchema + ".companion_links"
	}
	return s.path
}

func (s *linkedCompanionSet) query() ([]string, error) {
	if s.accounts != nil {
		keys, inPG, err := s.accounts.readAccounts("companion_links", linkedCompanionsColumns, pgUsersQuery(linkedCompanionsQuery, "companion_links"))
		if err != nil {
			return nil, err
		}
		s.fromFile = !inPG
		if inPG {
			return keys, nil
		}
	}
	if s.db == nil {
		if _, err := os.Stat(s.path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w at %s", errUsersDBMissing, s.path)
			}
			return nil, err
		}
		dsn, err := usersDBReadOnlyDSN(s.path)
		if err != nil {
			return nil, err
		}
		db, err := sql.Open("sqlite3", dsn)
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

// Close closes the read-only handles, if any, after a read in flight.
func (s *linkedCompanionSet) Close() {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	if s.accounts != nil {
		s.accounts.Close()
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

// newLinkedCompanionFilter returns the linked-only filter, already read
// once, when clientRxCoverage.requireLinkedCompanion is in effect, else nil.
// The setting without userManagement.enabled is ignored with a startup
// warning, as on the server.
func newLinkedCompanionFilter(cfg *Config, accounts *accountsReader) *linkedCompanionSet {
	if !cfg.RequireLinkedCompanionSet() {
		return nil
	}
	if !cfg.RequireLinkedCompanion() {
		log.Printf("[companions] WARNING: clientRxCoverage.requireLinkedCompanion is ignored: it needs userManagement.enabled")
		return nil
	}
	s := newLinkedCompanionSet(cfg.UsersDBPath())
	if accounts != nil {
		s.usePostgres(accounts)
	}
	shown := s.path
	if abs, err := filepath.Abs(shown); err == nil {
		shown = abs
	}
	if accounts != nil {
		shown = usersPGSchema + ".companion_links while that schema exists (accounts in PostgreSQL), else " + shown
	}
	log.Printf("[companions] only linked companions may publish client data; reading companion_links from %s", shown)
	s.refresh()
	return s
}

// refreshLoop re-reads the set every linkedCompanionsRefresh for the life of
// the process, which picks up unlinks.
func (s *linkedCompanionSet) refreshLoop() {
	t := time.NewTicker(linkedCompanionsRefresh)
	defer t.Stop()
	for range t.C {
		s.refresh()
	}
}
