package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meshcore-analyzer/perfio"
)

// PerfIOResponse holds per-process disk I/O metrics derived from /proc/self/io.
//
// `Ingestor` is the same shape as the top-level fields, sourced from the
// ingestor's own /proc/self/io snapshot (published via the ingestor stats file).
// Issue #1120 calls for "Both ingestor and server" — this is the ingestor half.
//
// `CancelledWriteBytesPerSec` surfaces `cancelled_write_bytes` from
// /proc/self/io — bytes the kernel discarded before they hit disk (e.g. file
// truncated/unlinked while dirty). Useful signal when chasing
// write-amplification anomalies (cf. the BackfillPathJSON loop in #1119).
type PerfIOResponse struct {
	ReadBytesPerSec           float64       `json:"readBytesPerSec"`
	WriteBytesPerSec          float64       `json:"writeBytesPerSec"`
	CancelledWriteBytesPerSec float64       `json:"cancelledWriteBytesPerSec"`
	SyscallsRead              float64       `json:"syscallsRead"`
	SyscallsWrite             float64       `json:"syscallsWrite"`
	Ingestor                  *PerfIOSample `json:"ingestor,omitempty"`
}

// PerfIOSample is the canonical per-process I/O rate sample, shared with the
// ingestor via internal/perfio. Sharing the type prevents silent JSON contract
// drift between the publisher (ingestor) and the consumer (server) (#1167).
type PerfIOSample = perfio.Sample

// PerfSqliteResponse holds SQLite-specific perf metrics.
type PerfSqliteResponse struct {
	WalSizeMB    float64 `json:"walSizeMB"`
	WalSize      int64   `json:"walSize"`
	PageCount    int64   `json:"pageCount"`
	PageSize     int64   `json:"pageSize"`
	CacheSize    int64   `json:"cacheSize"`
	CacheHitRate float64 `json:"cacheHitRate"`
}

// procIOSample is a snapshot of /proc/self/io counters.
type procIOSample struct {
	at             time.Time
	readBytes      int64
	writeBytes     int64
	cancelledWrite int64
	syscR          int64
	syscW          int64
}

// perfIOTracker keeps the previous sample so handlePerfIO can compute deltas.
var (
	perfIOMu         sync.Mutex
	perfIOLastSample procIOSample
)

// readIngestorStatsParseCalls counts full json.Unmarshal calls performed by
// readIngestorIOSample (cache miss path). Exported (lowercase + same-package
// access) for tests asserting the cache eliminates redundant decodes.
// Carmack must-fix #2.
var readIngestorStatsParseCalls atomic.Int64

// resetIngestorIOCache wipes the cached snapshot. Test-only helper.
func resetIngestorIOCache() {
	ingestorIOCache.Lock()
	ingestorIOCache.version = ingestorStatsVersion{}
	ingestorIOCache.sample = nil
	ingestorIOCache.Unlock()
}

// ingestorIOCache is the byte-stable snapshot cache for readIngestorIOSample
// (Carmack must-fix #2). Keyed by the document's version (file mtime
// nanoseconds and size, or on Postgres the row's updated_at and size); on hit
// we return the previously decoded sample without decoding it again.
var ingestorIOCache struct {
	sync.Mutex
	version ingestorStatsVersion
	sample  *PerfIOSample
}

// readProcIO parses /proc/self/io. Returns a zero-time sample (at.IsZero())
// on non-Linux, read failure, or when no recognised keys were parsed
// (Carmack must-fix #6 — never publish a phantom-zero counter set, the
// next tick would treat the real counters as a giant delta).
func readProcIO() procIOSample {
	s := procIOSample{at: time.Now()}
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return procIOSample{}
	}
	defer f.Close()
	if !parseProcIOInto(bufio.NewScanner(f), &s) {
		return procIOSample{}
	}
	return s
}

// parseProcIOInto reads /proc/self/io-shaped key:value lines from sc and
// populates the byte/syscall fields on s. Returns true iff at least one
// recognised key was successfully parsed (Carmack must-fix #6).
//
// Implementation delegates to perfio.ParseProcIO — single source of truth
// shared with the ingestor (Carmack must-fix #7; previously two divergent
// copies, which is how the empty-key gate was missing on this side).
func parseProcIOInto(sc *bufio.Scanner, s *procIOSample) bool {
	var c perfio.Counters
	ok := perfio.ParseProcIO(sc, &c)
	s.readBytes = c.ReadBytes
	s.writeBytes = c.WriteBytes
	s.cancelledWrite = c.CancelledWriteBytes
	s.syscR = c.SyscR
	s.syscW = c.SyscW
	return ok
}

// handlePerfIO returns delta-rate disk I/O for the server process (per-second).
// On the first call (no prior sample), rates are zero; subsequent calls
// report the delta divided by elapsed seconds.
func (s *Server) handlePerfIO(w http.ResponseWriter, r *http.Request) {
	cur := readProcIO()
	resp := PerfIOResponse{}

	perfIOMu.Lock()
	prev := perfIOLastSample
	perfIOLastSample = cur
	perfIOMu.Unlock()

	if !prev.at.IsZero() {
		dt := cur.at.Sub(prev.at).Seconds()
		if dt < 0.001 {
			dt = 0.001
		}
		resp.ReadBytesPerSec = float64(cur.readBytes-prev.readBytes) / dt
		resp.WriteBytesPerSec = float64(cur.writeBytes-prev.writeBytes) / dt
		resp.CancelledWriteBytesPerSec = float64(cur.cancelledWrite-prev.cancelledWrite) / dt
		resp.SyscallsRead = float64(cur.syscR-prev.syscR) / dt
		resp.SyscallsWrite = float64(cur.syscW-prev.syscW) / dt
	}
	// Ingestor block: GREEN commit replaces stub readIngestorIOSample with
	// real parsing of the ingestor stats file's procIO section (#1120
	// follow-up — "Both ingestor and server").
	if ing := readIngestorIOSampleFrom(s.db); ing != nil {
		resp.Ingestor = ing
	}
	writeJSON(w, resp)
}

// IngestorStatsStaleThreshold is the maximum age (sampledAt → now) of an
// ingestor stats snapshot before it is treated as dead and dropped from the
// /api/perf/io response. Default writer interval is ~1s; 5× that catches a
// wedged writer goroutine without flapping on a brief tick miss.
//
// #1167 must-fix #1: serving stale procIO as live disguises a dead ingestor.
const IngestorStatsStaleThreshold = 5 * time.Second

// ingestorIOPeek is the minimal subset of IngestorStats that
// readIngestorIOSample actually needs. Decoding into this instead of the
// full IngestorStats avoids allocating BackfillUpdates (a map) and the
// ~10 unused counter fields on every /api/perf/io request (Carmack
// must-fix #1).
type ingestorIOPeek struct {
	SampledAt string        `json:"sampledAt"`
	ProcIO    *PerfIOSample `json:"procIO,omitempty"`
}

// readIngestorIOSample reads the per-process I/O block from the ingestor stats
// file. Returns nil if the file is missing, malformed, carries no proc-IO
// block (older ingestor builds), OR the snapshot is older than
// IngestorStatsStaleThreshold (#1167 must-fix #1 — operators must not see
// stale numbers under .ingestor when the ingestor is down). Never errors —
// diagnostics only.
//
// Cached by (file mtime nanoseconds, size): the underlying file is byte-stable
// between 1Hz writer ticks, so polling the endpoint at 1Hz from N tabs MUST
// NOT cause N file-opens + N json.Unmarshal per second on identical bytes
// (Carmack must-fix #2). The cache invalidates as soon as either mtime or
// size differs from the cached entry.
//
// It reads the stats file; readIngestorIOSampleFrom reads db's source.
func readIngestorIOSample() *PerfIOSample { return readIngestorIOSampleFrom(nil) }

// readIngestorIOSampleFrom is readIngestorIOSample for the stats source of
// db (see statIngestorStats): the file, or on Postgres the ingestor_stats
// row, cached by its updated_at and size the same way.
func readIngestorIOSampleFrom(db *DB) *PerfIOSample {
	ver, data, statErr := db.statIngestorStats()
	if statErr != nil {
		return nil
	}

	ingestorIOCache.Lock()
	if ingestorIOCache.version == ver && ingestorIOCache.sample != nil {
		s := ingestorIOCache.sample
		ingestorIOCache.Unlock()
		// Re-validate freshness on cache hit too: a stale-but-byte-stable
		// file (writer wedged) MUST still drop after the threshold.
		if s.SampledAt != "" {
			if ts, err := time.Parse(time.RFC3339, s.SampledAt); err == nil {
				if time.Since(ts) > IngestorStatsStaleThreshold {
					return nil
				}
			}
		}
		return s
	}
	ingestorIOCache.Unlock()

	if data == nil {
		var err error
		if data, err = db.readIngestorStats(); err != nil {
			return nil
		}
	}
	readIngestorStatsParseCalls.Add(1)
	var st ingestorIOPeek
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	if st.ProcIO == nil {
		return nil
	}
	stamp := st.SampledAt
	if stamp == "" {
		stamp = st.ProcIO.SampledAt
	}
	if stamp == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return nil
	}
	if time.Since(ts) > IngestorStatsStaleThreshold {
		return nil
	}

	ingestorIOCache.Lock()
	ingestorIOCache.version = ver
	ingestorIOCache.sample = st.ProcIO
	ingestorIOCache.Unlock()

	return st.ProcIO
}

// handlePerfSqlite returns SQLite WAL size + cache hit-rate stats.
// Not available on Postgres, which has no SQLite pages or WAL file to report.
func (s *Server) handlePerfSqlite(w http.ResponseWriter, r *http.Request) {
	if s.db.isPostgres() {
		writeError(w, http.StatusNotFound, "not available: the database is PostgreSQL")
		return
	}
	resp := PerfSqliteResponse{}
	if s.db != nil && s.db.conn != nil {
		var pageCount, pageSize int64
		_ = s.db.conn.QueryRow("PRAGMA page_count").Scan(&pageCount)
		_ = s.db.conn.QueryRow("PRAGMA page_size").Scan(&pageSize)
		var cacheSize int64
		_ = s.db.conn.QueryRow("PRAGMA cache_size").Scan(&cacheSize)
		resp.PageCount = pageCount
		resp.PageSize = pageSize
		resp.CacheSize = cacheSize

		// Cache hit rate: derived from PacketStore cache (rw_cache). We don't
		// have a direct SQLite cache counter through the driver, so we
		// surface the closest available proxy — the in-process row cache.
		if s.store != nil {
			cs := s.store.GetCacheStatsTyped()
			total := cs.Hits + cs.Misses
			if total > 0 {
				resp.CacheHitRate = float64(cs.Hits) / float64(total)
			}
		}

		if s.db.path != "" && s.db.path != ":memory:" {
			if info, err := os.Stat(s.db.path + "-wal"); err == nil {
				resp.WalSize = info.Size()
				resp.WalSizeMB = float64(info.Size()) / 1048576
			}
		}
	}
	writeJSON(w, resp)
}

// IngestorStats is the on-disk JSON shape the ingestor writes periodically
// for the server to expose via /api/perf/write-sources.
type IngestorStats struct {
	SampledAt          string           `json:"sampledAt"`
	TxInserted         int64            `json:"tx_inserted"`
	ObsInserted        int64            `json:"obs_inserted"`
	DuplicateTx        int64            `json:"tx_dupes"`
	NodeUpserts        int64            `json:"node_upserts"`
	ObserverUpserts    int64            `json:"observer_upserts"`
	WriteErrors        int64            `json:"write_errors"`
	SignatureDrops     int64            `json:"sig_drops"`
	WALCommits         int64            `json:"walCommits"`
	GroupCommitFlushes int64            `json:"groupCommitFlushes"`
	BackfillUpdates    map[string]int64 `json:"backfillUpdates"`
	// ProcIO is the ingestor's own /proc/self/io rates (since its previous
	// sample). Optional — older ingestor builds don't publish this. See #1120.
	ProcIO *PerfIOSample `json:"procIO,omitempty"`
	// WriterPerf is the per-component SQLite writer-lock latency
	// snapshot (#1340). Optional — older ingestor builds don't
	// publish this. Surfaced under .writer_perf by
	// handlePerfWriteSources.
	WriterPerf map[string]WriterStatsSnapshot `json:"writer_perf,omitempty"`
	// SourceLiveness (PR #1609 M1) is the per-MQTT-source two-clock
	// snapshot: lastReceiptUnix (broker liveness, stamped at receipt)
	// vs lastMessageUnix (write-path liveness, stamped post-write).
	// Surfaced by /api/healthz under .ingest_liveness so operators can
	// distinguish "broker alive, write path stuck" from "everything
	// stalled". Optional — older ingestor builds don't publish this.
	SourceLiveness map[string]SourceLivenessSnapshot `json:"source_liveness,omitempty"`
}

// SourceLivenessSnapshot mirrors the ingestor's per-MQTT-source liveness
// pair (PR #1609 M1). Both fields are unix seconds; 0 means "never".
type SourceLivenessSnapshot struct {
	LastReceiptUnix int64 `json:"lastReceiptUnix"`
	LastMessageUnix int64 `json:"lastMessageUnix"`
}

// WriterStatsSnapshot mirrors the ingestor's per-component writer-lock
// latency snapshot (#1340). Times are milliseconds. Server-side decode
// uses this type to keep the JSON contract stable across processes.
type WriterStatsSnapshot struct {
	Count           int64   `json:"count"`
	ContentionTotal int64   `json:"contention_total"`
	WaitMsP50       float64 `json:"wait_ms_p50"`
	WaitMsP95       float64 `json:"wait_ms_p95"`
	WaitMsP99       float64 `json:"wait_ms_p99"`
	WaitMsMax       float64 `json:"wait_ms_max"`
	HoldMsP50       float64 `json:"hold_ms_p50"`
	HoldMsP95       float64 `json:"hold_ms_p95"`
	HoldMsP99       float64 `json:"hold_ms_p99"`
	HoldMsMax       float64 `json:"hold_ms_max"`
}

// IngestorStatsPath is the well-known location where the ingestor writes its
// rolling stats snapshot. Overridable by env CORESCOPE_INGESTOR_STATS for tests.
// On Postgres the snapshot is the ingestor_stats row instead (see
// readIngestorStats), so the two processes need no shared filesystem.
func IngestorStatsPath() string {
	if p := os.Getenv("CORESCOPE_INGESTOR_STATS"); p != "" {
		return p
	}
	return "/tmp/corescope-ingestor-stats.json"
}

// ingestorStatsVersion identifies one version of the ingestor's stats
// document, for the caches that skip decoding an unchanged one: the file's
// mtime and size, or on Postgres the row's updated_at and payload size.
type ingestorStatsVersion struct {
	mtimeUnixNano int64
	size          int64
	updatedAt     string
}

// pgIngestorStatsTimeout bounds one read of the ingestor_stats row: /healthz
// reads it, and must answer even when the database is slow.
const pgIngestorStatsTimeout = 2 * time.Second

// pgIngestorStatsLogEvery rate-limits the log line for a failed read of the
// ingestor_stats row; the readers are diagnostics polled every second.
const pgIngestorStatsLogEvery = time.Minute

var pgIngestorStatsLastLog atomic.Int64 // unix nanoseconds of the last such line

// readIngestorStatsRow reads the ingestor_stats row through the main
// (read-only) pool. No row yet is an error satisfying
// errors.Is(err, os.ErrNotExist), like a missing stats file; other failures
// are logged at most once per pgIngestorStatsLogEvery.
func (db *DB) readIngestorStatsRow() (updatedAt string, payload []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), pgIngestorStatsTimeout)
	defer cancel()
	var p string
	err = db.conn.QueryRowContext(ctx, `SELECT updated_at, payload FROM ingestor_stats WHERE id = 1`).Scan(&updatedAt, &p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("ingestor_stats has no row yet: %w", os.ErrNotExist)
	}
	if err != nil {
		now := time.Now().UnixNano()
		if last := pgIngestorStatsLastLog.Load(); now-last >= int64(pgIngestorStatsLogEvery) && pgIngestorStatsLastLog.CompareAndSwap(last, now) {
			log.Printf("[perf] read ingestor_stats: %v", err)
		}
		return "", nil, err
	}
	return updatedAt, []byte(p), nil
}

// statIngestorStats returns the version of the ingestor's stats document: on
// SQLite (or a nil db) the stats file's, on Postgres the ingestor_stats
// row's. On Postgres the one query that answers it also returns the payload
// (data non-nil), so a caller whose cache missed need not read again.
func (db *DB) statIngestorStats() (ver ingestorStatsVersion, data []byte, err error) {
	if db.isPostgres() {
		updatedAt, payload, err := db.readIngestorStatsRow()
		if err != nil {
			return ver, nil, err
		}
		return ingestorStatsVersion{size: int64(len(payload)), updatedAt: updatedAt}, payload, nil
	}
	info, err := os.Stat(IngestorStatsPath())
	if err != nil {
		return ver, nil, err
	}
	return ingestorStatsVersion{mtimeUnixNano: info.ModTime().UnixNano(), size: info.Size()}, nil, nil
}

// readIngestorStats returns the ingestor's stats document (the JSON shape
// IngestorStats decodes): the stats file on SQLite (or a nil db), the
// ingestor_stats payload on Postgres. Nothing written yet is an error
// satisfying errors.Is(err, os.ErrNotExist) on both.
func (db *DB) readIngestorStats() ([]byte, error) {
	if db.isPostgres() {
		_, payload, err := db.readIngestorStatsRow()
		return payload, err
	}
	return os.ReadFile(IngestorStatsPath())
}

// readIngestorSourceLiveness returns the per-source receipt/write-path
// liveness map from the ingestor stats file, or nil on any error / older
// ingestor that doesn't publish the field. PR #1609 M1 — surfaced by
// /api/healthz under .ingest_liveness so operators can spot "broker
// alive, write path stuck".
//
// /healthz is a hot path (LB / k8s / uptime monitors), so the result
// is memoized with a short TTL (sourceLivenessCacheTTL) and refreshed
// whenever the underlying file mtime changes (PR #1623 round-1
// finding 4). The lock is held briefly; the costly Unmarshal happens
// at most once per refresh window.
//
// It reads the stats file; readIngestorSourceLivenessFrom reads db's source.
func readIngestorSourceLiveness() map[string]SourceLivenessSnapshot {
	return readIngestorSourceLivenessFrom(nil)
}

// pgSourceLivenessKey is sourceLivenessCache.path for the ingestor_stats row.
const pgSourceLivenessKey = "postgres:ingestor_stats"

// readIngestorSourceLivenessFrom is readIngestorSourceLiveness for the stats
// source of db. On Postgres there is no cheap mtime probe (asking the row
// is the read), so a cached value is served for the whole TTL, and a refresh
// whose row is unchanged (same updated_at and size) skips the decode.
func readIngestorSourceLivenessFrom(db *DB) map[string]SourceLivenessSnapshot {
	if db.isPostgres() {
		return readIngestorSourceLivenessPG(db)
	}
	path := IngestorStatsPath()
	now := time.Now()

	sourceLivenessCache.mu.RLock()
	if sourceLivenessCache.path == path &&
		now.Sub(sourceLivenessCache.cachedAt) < sourceLivenessCacheTTL {
		// Cheap mtime probe: if the file moved since we cached, fall
		// through to the refresh path. Stat is cheap relative to
		// ReadFile+Unmarshal.
		info, err := os.Stat(path)
		fresh := err == nil && info.ModTime().Equal(sourceLivenessCache.mtime)
		if fresh || (err != nil && sourceLivenessCache.mtime.IsZero()) {
			out := sourceLivenessCache.value
			sourceLivenessCache.mu.RUnlock()
			return out
		}
	}
	sourceLivenessCache.mu.RUnlock()

	sourceLivenessCache.mu.Lock()
	defer sourceLivenessCache.mu.Unlock()
	// Re-check under the write lock — another goroutine may have just
	// refreshed.
	if sourceLivenessCache.path == path &&
		time.Since(sourceLivenessCache.cachedAt) < sourceLivenessCacheTTL {
		info, err := os.Stat(path)
		fresh := err == nil && info.ModTime().Equal(sourceLivenessCache.mtime)
		if fresh || (err != nil && sourceLivenessCache.mtime.IsZero()) {
			return sourceLivenessCache.value
		}
	}

	data, err := sourceLivenessReadFile(path)
	if err != nil {
		// Cache the negative result too, so a missing file doesn't
		// hammer the disk under /healthz pressure.
		sourceLivenessCache.path = path
		sourceLivenessCache.value = nil
		sourceLivenessCache.cachedAt = now
		sourceLivenessCache.mtime = time.Time{}
		return nil
	}
	var st IngestorStats
	if err := json.Unmarshal(data, &st); err != nil {
		sourceLivenessCache.path = path
		sourceLivenessCache.value = nil
		sourceLivenessCache.cachedAt = now
		sourceLivenessCache.mtime = time.Time{}
		return nil
	}
	sourceLivenessCache.path = path
	sourceLivenessCache.value = st.SourceLiveness
	sourceLivenessCache.cachedAt = now
	if info, err := os.Stat(path); err == nil {
		sourceLivenessCache.mtime = info.ModTime()
	} else {
		sourceLivenessCache.mtime = time.Time{}
	}
	return st.SourceLiveness
}

func readIngestorSourceLivenessPG(db *DB) map[string]SourceLivenessSnapshot {
	c := &sourceLivenessCache
	c.mu.RLock()
	if c.path == pgSourceLivenessKey && time.Since(c.cachedAt) < sourceLivenessCacheTTL {
		out := c.value
		c.mu.RUnlock()
		return out
	}
	c.mu.RUnlock()

	// The row is read under the write lock, as the file is above: callers
	// arriving during a refresh wait for its result instead of each
	// querying (the read is bounded by pgIngestorStatsTimeout).
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == pgSourceLivenessKey && time.Since(c.cachedAt) < sourceLivenessCacheTTL {
		return c.value
	}
	updatedAt, data, err := db.readIngestorStatsRow()
	now := time.Now()
	// updated_at is the ingestor's sampledAt (whole seconds), so the size
	// joins it in the key, as it joins the mtime for the file.
	version := fmt.Sprintf("%s/%d", updatedAt, len(data))
	if err == nil && c.path == pgSourceLivenessKey && c.updatedAt != "" && version == c.updatedAt {
		c.cachedAt = now
		return c.value
	}
	var st IngestorStats
	if err == nil {
		err = json.Unmarshal(data, &st)
	}
	c.path, c.cachedAt, c.mtime = pgSourceLivenessKey, now, time.Time{}
	if err != nil {
		c.value, c.updatedAt = nil, ""
		return nil
	}
	c.value, c.updatedAt = st.SourceLiveness, version
	return st.SourceLiveness
}

// sourceLivenessReadFile is the file-reader used by
// readIngestorSourceLiveness. Swappable for tests so call counts can
// be asserted (PR #1623 round-1 finding 4 TTL cache test).
var sourceLivenessReadFile = os.ReadFile

// sourceLivenessCacheTTL caps how long a parsed liveness map is reused
// across /healthz probes. 1s is short enough that operators see stale
// data only briefly during incidents, but long enough to coalesce
// hundreds of probes/sec from LBs.
var sourceLivenessCacheTTL = time.Second

// sourceLivenessCache memoizes the parsed liveness map keyed by file
// path + mtime (on Postgres pgSourceLivenessKey + the row's updated_at and
// size, in updatedAt). See
// readIngestorSourceLiveness.
var sourceLivenessCache struct {
	mu        sync.RWMutex
	path      string
	value     map[string]SourceLivenessSnapshot
	cachedAt  time.Time
	mtime     time.Time
	updatedAt string
}

// resetSourceLivenessCache clears the memo. Test-only helper; callable
// from production code is harmless (next call just re-reads).
func resetSourceLivenessCache() {
	sourceLivenessCache.mu.Lock()
	defer sourceLivenessCache.mu.Unlock()
	sourceLivenessCache.path = ""
	sourceLivenessCache.value = nil
	sourceLivenessCache.cachedAt = time.Time{}
	sourceLivenessCache.mtime = time.Time{}
	sourceLivenessCache.updatedAt = ""
}

// handlePerfWriteSources reads the ingestor's stats (the file, or on Postgres
// the ingestor_stats row) and returns a flat map of source-name -> counter,
// plus the sample timestamp.
func (s *Server) handlePerfWriteSources(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{
		"sources":  map[string]int64{},
		"sampleAt": "",
	}

	data, err := s.db.readIngestorStats()
	if err != nil {
		writeJSON(w, out)
		return
	}
	var st IngestorStats
	if err := json.Unmarshal(data, &st); err != nil {
		writeJSON(w, out)
		return
	}
	sources := map[string]int64{
		"tx_inserted":        st.TxInserted,
		"tx_dupes":           st.DuplicateTx,
		"obs_inserted":       st.ObsInserted,
		"node_upserts":       st.NodeUpserts,
		"observer_upserts":   st.ObserverUpserts,
		"write_errors":       st.WriteErrors,
		"sig_drops":          st.SignatureDrops,
		"walCommits":         st.WALCommits,
		"groupCommitFlushes": st.GroupCommitFlushes,
	}
	for name, v := range st.BackfillUpdates {
		sources["backfill_"+name] = v
	}
	out["sources"] = sources
	out["sampleAt"] = st.SampledAt
	// Surface per-component SQLite writer-lock latency histograms
	// (#1340) under .writer_perf so operators can see when a
	// component (e.g. neighbor_builder) is starving the writer.
	// Empty map when the ingestor is too old to publish this field.
	if len(st.WriterPerf) > 0 {
		out["writer_perf"] = st.WriterPerf
	} else {
		out["writer_perf"] = map[string]WriterStatsSnapshot{}
	}
	writeJSON(w, out)
}
