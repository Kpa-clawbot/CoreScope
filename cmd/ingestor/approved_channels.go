package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/channel"
	"github.com/meshcore-analyzer/dbconfig"
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

const approvedChannelsSQLiteQuery = `SELECT subject FROM proposals WHERE kind = 'hashtag_channel' AND status = 'approved' ORDER BY decided_at, id LIMIT ?1`

var errUsersDBMissing = errors.New("approved channel account storage is not configured")

// channelKeySet hands each message the current channel key map. Snapshots
// are never mutated after they are stored, so decoders read them without a
// lock. refresh is called from one goroutine only (startup, then the ticker).
type channelKeySet struct {
	configured map[string]string
	path       string
	backend    dbconfig.Backend
	max        int
	cur        atomic.Pointer[map[string]string]

	db       *sql.DB // opened lazily, read-only
	failing  bool    // the last read failed; logged once per failure streak
	approved int     // approved keys in the current snapshot
}

// newChannelKeySet starts with the configured keys (the very map
// loadChannelKeys built, so the feature off changes nothing).
// newChannelKeySet retains compatibility for an unambiguous native target.
func newChannelKeySet(configured map[string]string, target string, max int) *channelKeySet {
	storage := dbconfig.Storage{Backend: dbconfig.SQLite, UsersDBPath: target}
	if strings.HasPrefix(strings.ToLower(target), "postgres:") || strings.HasPrefix(strings.ToLower(target), "postgresql:") {
		storage.Backend = dbconfig.Postgres
		storage.ApprovedChannelsDatabaseURL = target
	}
	return newChannelKeySetStorage(configured, storage, max)
}

// newChannelKeySetStorage consumes the same resolved storage choice as startup.
// Inactive addresses never cause fallback to a different account store.
func newChannelKeySetStorage(configured map[string]string, storage dbconfig.Storage, max int) *channelKeySet {
	target := storage.UsersDBPath
	if storage.Backend == dbconfig.Postgres {
		target = storage.ApprovedChannelsDatabaseURL
	}
	s := &channelKeySet{configured: configured, path: target, backend: storage.Backend, max: max}
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.backend != dbconfig.SQLite && s.backend != dbconfig.Postgres {
		return nil, errors.New("approved channel storage backend must be selected")
	}
	if s.db == nil {
		if s.path == "" {
			return nil, errUsersDBMissing
		}
		var db *sql.DB
		var err error
		if s.backend == dbconfig.SQLite {
			uri, e := dbconfig.SQLiteURI(s.path, url.Values{"mode": {"ro"}, "_busy_timeout": {"5000"}})
			if e != nil {
				return nil, e
			}
			db, err = sql.Open("sqlite3", uri)
		} else {
			db, err = pgutil.Open(s.path, true)
			if err == nil {
				err = pgutil.AssertReadOnly(db)
			}
			if err == nil {
				var credentialAccess bool
				err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p','m','f') AND c.relname <> 'corescope_schema' AND has_table_privilege(c.oid,'SELECT'))`).Scan(&credentialAccess)
				if err == nil && credentialAccess {
					err = errors.New("approved-channel reader has account-table access")
				}
			}
		}
		if err != nil {
			if db != nil {
				db.Close()
			}
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		s.db = db
	}
	if s.backend == dbconfig.SQLite {
		if err := dbconfig.AssertSQLiteImportComplete(s.db); err != nil {
			return nil, err
		}
	}
	// Check schema and rows within one native read-only snapshot. PostgreSQL's
	// projection role can read only the readiness marker and approved view.
	opts := &sql.TxOptions{ReadOnly: true}
	query := approvedChannelsSQLiteQuery
	if s.backend == dbconfig.Postgres {
		opts.Isolation = sql.LevelRepeatableRead
		query = approvedChannelsQuery
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if s.backend == dbconfig.Postgres {
		var ready bool
		if err := tx.QueryRowContext(ctx, `SELECT version,ready FROM corescope_schema WHERE kind='accounts'`).Scan(&version, &ready); err != nil {
			return nil, errors.New("approved-channel account schema is not initialized")
		}
		if version != 1 || !ready {
			return nil, errors.New("approved-channel account schema is incomplete or unsupported")
		}
	} else {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(version),0) FROM schema_version`).Scan(&count, &version); err != nil || count != 1 || version < 4 || version > 6 {
			return nil, errors.New("approved-channel SQLite account schema is incomplete or unsupported")
		}
	}
	rows, err := tx.QueryContext(ctx, query, s.max)
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
	log.Print("[proposals] reading approved channels from the selected account store")
}
