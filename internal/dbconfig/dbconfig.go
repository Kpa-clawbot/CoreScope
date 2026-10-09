// Package dbconfig provides the shared DBConfig struct used by both the server
// and ingestor binaries for startup loading and legacy maintenance settings.
package dbconfig

// DBConfig controls startup loading. The three former SQLite controls remain
// decodable for old config files; PostgreSQL maintenance does not use them.
type DBConfig struct {
	VacuumOnStartup        bool `json:"vacuumOnStartup"`        // Deprecated: PostgreSQL autovacuum owns maintenance.
	IncrementalVacuumPages int  `json:"incrementalVacuumPages"` // Deprecated: retained for legacy configuration decoding.
	AnalysisLimit          int  `json:"analysisLimit"`          // Deprecated: PostgreSQL owns planner statistics.

	// Load controls chunked startup loading (#1009).
	Load *LoadConfig `json:"load,omitempty"`
}

// LoadConfig controls the chunked startup-load behavior (#1009).
type LoadConfig struct {
	// ChunkSize is the number of transmission rows fetched per chunk
	// during PacketStore.LoadChunked. 0/unset → 10000.
	ChunkSize int `json:"chunkSize"`
}

// GetIncrementalVacuumPages returns the configured pages or 1024 default.
func (c *DBConfig) GetIncrementalVacuumPages() int {
	if c != nil && c.IncrementalVacuumPages > 0 {
		return c.IncrementalVacuumPages
	}
	return 1024
}

// GetLoadChunkSize returns the configured chunk size or 10000 default (#1009).
func (c *DBConfig) GetLoadChunkSize() int {
	if c != nil && c.Load != nil && c.Load.ChunkSize > 0 {
		return c.Load.ChunkSize
	}
	return 10000
}
