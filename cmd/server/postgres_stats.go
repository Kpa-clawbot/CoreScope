package main

import (
	"context"
	"time"
)

const postgresStatsTTL = 30 * time.Second
const postgresBlockCacheRateSQL = `blks_hit::double precision / NULLIF(blks_hit::double precision + blks_read, 0)`

// Storage traversal and full row counts are diagnostic samples, independent of
// ingest and PacketStore invalidation. Keep one bounded immutable sample per DB.
type postgresDatabaseSample struct {
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
		err := db.conn.QueryRowContext(queryCtx, `SELECT pg_database_size(current_database()), `+postgresBlockCacheRateSQL+`,
			(SELECT COUNT(*) FROM transmissions), (SELECT COUNT(*) FROM observations),
			(SELECT COUNT(*) FROM nodes), (SELECT COUNT(*) FROM observers)
			FROM pg_catalog.pg_stat_database WHERE datname=current_database()`).Scan(
			&sample.DatabaseBytes, &sample.CacheHitRate, &sample.Rows.Transmissions,
			&sample.Rows.Observations, &sample.Rows.Nodes, &sample.Rows.Observers)
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
