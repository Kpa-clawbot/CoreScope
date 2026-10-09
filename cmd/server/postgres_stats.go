package main

import (
	"context"
	"errors"
	"github.com/meshcore-analyzer/dbconfig"
	"os"
	"time"
)

const postgresStatsTTL = 30 * time.Second
const postgresBlockCacheRateSQL = `blks_hit::double precision / NULLIF(blks_hit::double precision + blks_read, 0)`

// Storage traversal and full row counts are diagnostic samples, independent of
// ingest and PacketStore invalidation. Keep one bounded immutable sample per DB.
type sqliteDatabaseSample struct {
	WalSize                                       int64
	PageCount, PageSize, CacheSize, FreelistCount int64
	JournalMode                                   string
	PlannerStats                                  bool
}

type postgresDatabaseSample struct {
	SQLite        *sqliteDatabaseSample
	DatabaseBytes int64
	CacheHitRate  *float64
	Rows          DatabaseRowCounts
	SampledAt     time.Time
}

func (db *DB) cachedPostgresStats() (postgresDatabaseSample, bool) {
	db.pgStatsMu.Lock()
	sample, valid := db.pgStatsCache, time.Now().Before(db.pgStatsExpires)
	db.pgStatsMu.Unlock()
	return sample, valid
}

func (db *DB) postgresStats(ctx context.Context) (postgresDatabaseSample, error) {
	cached, valid := db.cachedPostgresStats()
	if valid {
		return cached, nil
	}
	if db.pgStatsMissHook != nil {
		db.pgStatsMissHook()
	}
	flight := db.pgStatsSF.DoChan("database", func() (any, error) {
		if sample, valid := db.cachedPostgresStats(); valid {
			return sample, nil
		}
		if db.pgStatsQueryHook != nil {
			db.pgStatsQueryHook()
		}
		// One canceled dashboard request must not cancel the shared sample.
		// The work itself is bounded, and no cache mutex is held during SQL.
		queryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var sample postgresDatabaseSample

		var err error
		if db.Backend() == dbconfig.SQLite {
			native := &sqliteDatabaseSample{}
			err = db.conn.QueryRowContext(queryCtx, `SELECT
    (SELECT page_count FROM pragma_page_count),(SELECT page_size FROM pragma_page_size),
    (SELECT cache_size FROM pragma_cache_size),(SELECT freelist_count FROM pragma_freelist_count),
    (SELECT journal_mode FROM pragma_journal_mode),EXISTS(SELECT 1 FROM sqlite_master WHERE name='sqlite_stat1'),
    (SELECT COUNT(*) FROM transmissions),(SELECT COUNT(*) FROM observations),(SELECT COUNT(*) FROM nodes),(SELECT COUNT(*) FROM observers)`).Scan(
				&native.PageCount, &native.PageSize, &native.CacheSize, &native.FreelistCount, &native.JournalMode, &native.PlannerStats,
				&sample.Rows.Transmissions, &sample.Rows.Observations, &sample.Rows.Nodes, &sample.Rows.Observers)
			if err == nil {
				var info os.FileInfo
				info, err = os.Stat(db.path)
				if err == nil {
					sample.DatabaseBytes = info.Size()
				}
			}
			if err == nil {
				if info, statErr := os.Stat(db.path + "-wal"); statErr == nil {
					native.WalSize = info.Size()
				} else if !errors.Is(statErr, os.ErrNotExist) {
					err = statErr
				}
			}
			sample.SQLite = native
		} else {
			err = db.conn.QueryRowContext(queryCtx, `SELECT pg_database_size(current_database()), `+postgresBlockCacheRateSQL+`,
			(SELECT COUNT(*) FROM transmissions), (SELECT COUNT(*) FROM observations),
			(SELECT COUNT(*) FROM nodes), (SELECT COUNT(*) FROM observers)
			FROM pg_catalog.pg_stat_database WHERE datname=current_database()`).Scan(
				&sample.DatabaseBytes, &sample.CacheHitRate, &sample.Rows.Transmissions,
				&sample.Rows.Observations, &sample.Rows.Nodes, &sample.Rows.Observers)
		}
		if err != nil {
			return nil, err
		}
		sample.SampledAt = time.Now()
		db.pgStatsMu.Lock()
		db.pgStatsCache, db.pgStatsExpires = sample, sample.SampledAt.Add(postgresStatsTTL)
		db.pgStatsMu.Unlock()
		return sample, nil
	})
	select {
	case result := <-flight:
		if result.Err == nil {
			return result.Val.(postgresDatabaseSample), nil
		}
		cached.CacheHitRate = nil
		return cached, result.Err
	case <-ctx.Done():
		cached.CacheHitRate = nil
		return cached, ctx.Err()
	}
}

func postgresSampleStamp(sample postgresDatabaseSample) string {
	if sample.SampledAt.IsZero() {
		return ""
	}
	return sample.SampledAt.UTC().Format(time.RFC3339Nano)
}
