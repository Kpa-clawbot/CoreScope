package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/channel"
	"github.com/meshcore-analyzer/pgutil"
)

// Approved hashtag channels (docs/specs/2026-10-07-channel-proposals-design.md).
// The server owns account data; the ingestor only reads its approved view
// SQL, once per approvedChannelsRefresh, and never writes it (AGENTS.md,
// read/write separation).

const approvedChannelsRefresh = time.Minute

// approvedChannelsQuery is pinned in internal/users/proposals_test.go
// (ingestorApprovedQuery), which checks it against ApprovedSubjects.
const approvedChannelsQuery = `SELECT subject FROM approved_channels WHERE kind = 'hashtag_channel' ORDER BY decided_at, id LIMIT $1`

var errUsersDBMissing = errors.New("approved channel database URL is not configured")

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
// the set, so an unavailable or incomplete account store never drops a key.
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
		log.Print("[channels] approved channels readable again")
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
		if s.path == "" {
			return nil, errUsersDBMissing
		}
		db, err := pgutil.Open(s.path, true)
		if err != nil {
			return nil, err
		}
		if err = pgutil.AssertReadOnly(db); err != nil {
			db.Close()
			return nil, err
		}
		var credentialAccess bool
		if err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p','m','f') AND c.relname <> 'corescope_schema' AND has_table_privilege(c.oid,'SELECT'))`).Scan(&credentialAccess); err != nil {
			db.Close()
			return nil, err
		}
		if credentialAccess {
			db.Close()
			return nil, errors.New("approved-channel reader has account-table access")
		}
		db.SetMaxOpenConns(1)
		s.db = db
	}
	// Read the gate and projection in one snapshot. The readiness marker is the
	// only base table this role can read; an import must not expose partial keys.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	var ready bool
	if err := tx.QueryRowContext(ctx, `SELECT version,ready FROM corescope_schema WHERE kind='accounts'`).Scan(&version, &ready); err != nil {
		return nil, errors.New("approved-channel account schema is not initialized")
	}
	if version != 1 || !ready {
		return nil, errors.New("approved-channel account schema is incomplete or unsupported")
	}
	rows, err := tx.QueryContext(ctx, approvedChannelsQuery, s.max)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
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

// Connection strings contain credentials and must never appear in logs.
func logApprovedChannelsSource(databaseURL string) {
	log.Print("[proposals] reading approved channels with restricted PostgreSQL reader")
}
