package main

import (
	"net/http"
)

// handleBackup streams a consistent SQLite snapshot of the analyzer DB.
//
// Requires API-key authentication (mounted via requireAPIKey in routes.go).
// Uses SQLite's `VACUUM INTO` to produce an atomic, consistent copy of the
// source database without blocking writers. The temp file is removed after
// the response is fully written.
//
// Response:
//   200 OK
//   Content-Type: application/octet-stream
//   Content-Disposition: attachment; filename="corescope-backup-<unix>.db"
//   <body: full SQLite database file>
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	// stub — implementation lands in green commit
	w.WriteHeader(http.StatusOK)
}
