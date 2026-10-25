// Package dbconfig provides the shared DBConfig struct used by both the server
// and ingestor binaries for storage selection, startup and maintenance settings.
package dbconfig

// DBConfig controls storage and startup loading. SQLite maintenance options
// remain active for SQLite and are ignored by PostgreSQL.
type DBConfig struct {
	Backend                Backend `json:"backend,omitempty"`
	VacuumOnStartup        bool    `json:"vacuumOnStartup"`
	IncrementalVacuumPages int     `json:"incrementalVacuumPages"`
	AnalysisLimit          int     `json:"analysisLimit"`

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
