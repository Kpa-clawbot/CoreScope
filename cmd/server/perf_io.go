package main

import (
	"net/http"
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

// handlePerfIO returns delta-rate disk I/O for the server process.
// Stub for TDD red commit — will be replaced with /proc/self/io read.
func (s *Server) handlePerfIO(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, PerfIOResponse{})
}

// handlePerfSqlite returns SQLite WAL size + cache stats.
// Stub for TDD red commit — will be replaced with real PRAGMA reads.
func (s *Server) handlePerfSqlite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, PerfSqliteResponse{})
}

// handlePerfWriteSources returns per-component write counters from the
// ingestor stats file (or zeros when unavailable). Stub for red commit.
func (s *Server) handlePerfWriteSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"sources":  map[string]int64{},
		"sampleAt": "",
	})
}
