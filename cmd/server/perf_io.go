package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PerfIOResponse holds per-process disk I/O metrics derived from /proc/self/io.
type PerfIOResponse struct {
	ReadBytesPerSec  float64 `json:"readBytesPerSec"`
	WriteBytesPerSec float64 `json:"writeBytesPerSec"`
	SyscallsRead     float64 `json:"syscallsRead"`
	SyscallsWrite    float64 `json:"syscallsWrite"`
}

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
	at            time.Time
	readBytes     int64
	writeBytes    int64
	syscR         int64
	syscW         int64
}

// perfIOTracker keeps the previous sample so handlePerfIO can compute deltas.
var (
	perfIOMu       sync.Mutex
	perfIOLastSample procIOSample
)

// readProcIO parses /proc/self/io. Returns zero sample on non-Linux or read failure.
func readProcIO() procIOSample {
	s := procIOSample{at: time.Now()}
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "read_bytes":
			s.readBytes = val
		case "write_bytes":
			s.writeBytes = val
		case "syscr":
			s.syscR = val
		case "syscw":
			s.syscW = val
		}
	}
	return s
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
		resp.SyscallsRead = float64(cur.syscR-prev.syscR) / dt
		resp.SyscallsWrite = float64(cur.syscW-prev.syscW) / dt
	}
	writeJSON(w, resp)
}

// handlePerfSqlite returns SQLite WAL size + cache hit-rate stats.
func (s *Server) handlePerfSqlite(w http.ResponseWriter, r *http.Request) {
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
		// have a direct SQLite cache counter via the modernc driver, so we
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
	SampledAt           string           `json:"sampledAt"`
	TxInserted          int64            `json:"tx_inserted"`
	ObsInserted         int64            `json:"obs_inserted"`
	DuplicateTx         int64            `json:"tx_dupes"`
	NodeUpserts         int64            `json:"node_upserts"`
	ObserverUpserts     int64            `json:"observer_upserts"`
	WriteErrors         int64            `json:"write_errors"`
	SignatureDrops      int64            `json:"sig_drops"`
	WALCommits          int64            `json:"walCommits"`
	GroupCommitFlushes  int64            `json:"groupCommitFlushes"`
	BackfillUpdates     map[string]int64 `json:"backfillUpdates"`
}

// IngestorStatsPath is the well-known location where the ingestor writes its
// rolling stats snapshot. Overridable by env CORESCOPE_INGESTOR_STATS for tests.
func IngestorStatsPath() string {
	if p := os.Getenv("CORESCOPE_INGESTOR_STATS"); p != "" {
		return p
	}
	return "/tmp/corescope-ingestor-stats.json"
}

// handlePerfWriteSources reads the ingestor's stats file and returns a flat
// map of source-name -> counter, plus the sample timestamp.
func (s *Server) handlePerfWriteSources(w http.ResponseWriter, r *http.Request) {
	out := map[string]interface{}{
		"sources":  map[string]int64{},
		"sampleAt": "",
	}

	data, err := os.ReadFile(IngestorStatsPath())
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
		"tx_inserted":      st.TxInserted,
		"tx_dupes":         st.DuplicateTx,
		"obs_inserted":     st.ObsInserted,
		"node_upserts":     st.NodeUpserts,
		"observer_upserts": st.ObserverUpserts,
		"write_errors":     st.WriteErrors,
		"sig_drops":        st.SignatureDrops,
		"walCommits":       st.WALCommits,
		"groupCommitFlushes": st.GroupCommitFlushes,
	}
	for name, v := range st.BackfillUpdates {
		sources["backfill_"+name] = v
	}
	out["sources"] = sources
	out["sampleAt"] = st.SampledAt
	writeJSON(w, out)
}
