package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/channel"
)

// Approved hashtag channels (docs/specs/2026-10-07-channel-proposals-design.md).
// The server owns users.db; the ingestor only reads it, read-only, with raw
// SQL, once per approvedChannelsRefresh, and never writes it (AGENTS.md,
// read/write separation).

const approvedChannelsRefresh = time.Minute

// approvedChannelsQuery is pinned in internal/users/proposals_test.go
// (ingestorApprovedQuery), which checks it against ApprovedSubjects.
const approvedChannelsQuery = `SELECT subject FROM proposals WHERE kind = 'hashtag_channel' AND status = 'approved' ORDER BY decided_at, id LIMIT ?`

var errUsersDBMissing = errors.New("users.db not found")

// channelKeySet hands each message the current channel key map. Snapshots
// are never mutated after they are stored, so decoders read them without a
// lock. refresh is called from one goroutine only (startup, then the ticker).
type channelKeySet struct {
	configured map[string]string
	path       string
	max        int
	cur        atomic.Pointer[map[string]string]

	db       *sql.DB // opened lazily, read-only
	failing  bool    // the last read failed; logged once per failure streak
	approved int     // approved keys in the current snapshot
}

// newChannelKeySet starts with the configured keys (the very map
// loadChannelKeys built, so the feature off changes nothing).
func newChannelKeySet(configured map[string]string, usersDBPath string, max int) *channelKeySet {
	s := &channelKeySet{configured: configured, path: usersDBPath, max: max}
	s.cur.Store(&configured)
	return s
}

// Snapshot returns the key map to decode one message with.
func (s *channelKeySet) Snapshot() map[string]string { return *s.cur.Load() }

// refresh re-reads the approved names and swaps in configured + approved.
// A failed read keeps the current snapshot: only a successful read changes
// the set, so a locked or missing users.db never drops a key.
func (s *channelKeySet) refresh() {
	names, err := s.readApproved()
	if err != nil {
		if !s.failing {
			log.Printf("[channels] approved channels unavailable (%v); keeping the %d keys in force", err, len(s.Snapshot()))
			s.failing = true
		}
		return
	}
	if s.failing {
		log.Printf("[channels] approved channels readable again from %s", s.path)
		s.failing = false
	}
	next, added := mergeApprovedKeys(s.configured, names)
	if added != s.approved {
		log.Printf("[channels] %d approved hashtag channel(s) in force", added)
	}
	s.approved = added
	s.cur.Store(&next)
}

func (s *channelKeySet) readApproved() ([]string, error) {
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
	rows, err := s.db.Query(approvedChannelsQuery, s.max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// usersDBReadOnlyDSN opens users.db read-only: this process must never
// take a write lock on the server's file.
func usersDBReadOnlyDSN(path string) string {
	return "file:" + path + "?mode=ro&_busy_timeout=5000"
}

// mergeApprovedKeys returns a new map: the configured keys plus a derived key
// per approved name that passes channel.ValidateHashtagName unchanged and is
// not configured already (a configured name always wins). The second result
// counts the approved keys added.
func mergeApprovedKeys(configured map[string]string, names []string) (map[string]string, int) {
	next := make(map[string]string, len(configured)+len(names))
	for k, v := range configured {
		next[k] = v
	}
	added := 0
	for _, raw := range names {
		name, err := channel.ValidateHashtagName(raw)
		if err != nil || name != raw {
			continue
		}
		if _, ok := next[name]; ok {
			continue
		}
		next[name] = deriveHashtagChannelKey(name)
		added++
	}
	return next, added
}

// Close closes the read-only handle, if any.
func (s *channelKeySet) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// logApprovedChannelsSource logs the users.db file the approved channels are
// read from, resolved to an absolute path so a relative one shows which file
// it means; the path as given when it cannot be resolved.
func logApprovedChannelsSource(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	log.Printf("[proposals] reading approved channels from %s", path)
}
