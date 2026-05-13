// Package dbconfig provides the shared DBConfig struct used by both the server
// and ingestor binaries for SQLite vacuum and maintenance settings (#919, #921).
package dbconfig

// DBConfig controls SQLite vacuum and maintenance behavior (#919).
type DBConfig struct {
	VacuumOnStartup        bool `json:"vacuumOnStartup"`        // one-time full VACUUM on startup if auto_vacuum is not INCREMENTAL
	IncrementalVacuumPages int  `json:"incrementalVacuumPages"` // pages returned to OS per reaper cycle (default 1024)
}

// GetIncrementalVacuumPages returns the configured pages or 1024 default.
func (c *DBConfig) GetIncrementalVacuumPages() int {
	if c != nil && c.IncrementalVacuumPages > 0 {
		return c.IncrementalVacuumPages
	}
	return 1024
}
