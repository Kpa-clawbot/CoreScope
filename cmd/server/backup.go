package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleBackup streams a consistent SQLite snapshot of the analyzer DB.
//
// Requires API-key authentication (mounted via requireAPIKey in routes.go).
//
// Strategy: SQLite's `VACUUM INTO 'path'` produces an atomic, defragmented
// copy of the current database into a new file. It runs at READ ISOLATION
// against the source DB (works on our read-only connection) and never
// blocks concurrent writers — the ingestor keeps writing to the WAL while
// the snapshot is taken from a consistent read transaction.
//
// Response:
//
//	200 OK
//	Content-Type: application/octet-stream
//	Content-Disposition: attachment; filename="corescope-backup-<unix>.db"
//	<body: complete SQLite database file>
//
// The temp file is removed after the response is fully written, regardless
// of whether the client successfully consumed the stream.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil || s.db.conn == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	ts := time.Now().UTC().Unix()
	clientIP := r.Header.Get("X-Forwarded-For")
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	log.Printf("[backup] generating backup for client %s", clientIP)

	// Stage the snapshot in the OS temp dir so we never touch the live DB
	// directory (avoids confusing operators / accidental WAL clobber).
	tmpDir, err := os.MkdirTemp("", "corescope-backup-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tempdir failed: "+err.Error())
		return
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			log.Printf("[backup] cleanup error: %v", rmErr)
		}
	}()

	snapshotPath := filepath.Join(tmpDir, fmt.Sprintf("corescope-backup-%d.db", ts))

	// SQLite parses the path literal — escape any single quotes defensively.
	// (mkdtemp output won't contain quotes, but be paranoid for future-proofing.)
	escaped := strings.ReplaceAll(snapshotPath, "'", "''")
	if _, err := s.db.conn.ExecContext(r.Context(), fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot failed: "+err.Error())
		return
	}

	f, err := os.Open(snapshotPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "open snapshot failed: "+err.Error())
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err == nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"corescope-backup-%d.db\"", ts))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, f); err != nil {
		// Headers already flushed; just log. Client will see truncated stream.
		log.Printf("[backup] stream error: %v", err)
	}
}
