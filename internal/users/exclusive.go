package users

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// exclusiveLockNamespace prefixes job names before hashing them into an
// advisory lock key, keeping them apart from other users of the key space.
const exclusiveLockNamespace = "corescope-users:"

// exclusiveReleaseTimeout bounds the unlock in release, which has no
// caller context.
const exclusiveReleaseTimeout = 10 * time.Second

// exclusiveLockKey is the pg advisory lock key for job name: FNV-64a of the
// namespaced name, as a signed bigint.
func exclusiveLockKey(name string) int64 {
	h := fnv.New64a()
	h.Write([]byte(exclusiveLockNamespace + name))
	return int64(h.Sum64())
}

// TryExclusive takes a cluster-wide lock named name for a background job.
// On Postgres: a session-level pg_try_advisory_lock on a dedicated pinned
// connection (key: FNV-64a of "corescope-users:"+name), held until release()
// is called; ok=false when another replica holds it. On SQLite: always ok
// (single process) with a no-op release. release is idempotent and also
// releases the pinned connection.
//
// When ok is false or err is non-nil, release is a no-op (never nil). The
// held lock occupies one of the store's pooled connections; if that
// connection is lost, Postgres frees the lock and another replica may take
// it while this one still runs the job, so jobs must stay safe to overlap
// (the same as on SQLite across a restart).
func (s *Store) TryExclusive(ctx context.Context, name string) (release func(), ok bool, err error) {
	noop := func() {}
	if name == "" {
		return noop, false, errors.New("users: exclusive lock name is empty")
	}
	if !s.pg {
		return noop, true, nil
	}
	key := exclusiveLockKey(name)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return noop, false, fmt.Errorf("users: exclusive lock %s: connection: %w", name, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(?)`, key).Scan(&ok); err != nil {
		// Whether the lock was taken is unknown: drop the session, which
		// releases anything it holds, instead of pooling it.
		discardConn(conn)
		return noop, false, fmt.Errorf("users: exclusive lock %s: %w", name, err)
	}
	if !ok {
		conn.Close()
		return noop, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), exclusiveReleaseTimeout)
			defer cancel()
			var unlocked bool
			if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(?)`, key).Scan(&unlocked); err != nil || !unlocked {
				// A pooled session still holding the lock would block the
				// job cluster-wide; closing the session frees it.
				discardConn(conn)
				return
			}
			conn.Close()
		})
	}, true, nil
}

// discardConn closes c's underlying connection instead of returning it to
// the pool (database/sql drops a connection whose Raw callback reports
// driver.ErrBadConn).
func discardConn(c *sql.Conn) {
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
	_ = c.Close()
}
