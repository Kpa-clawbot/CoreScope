package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meshcore-analyzer/pgutil"
)

// handleBackup stages a consistent PostgreSQL custom-format pg_dump snapshot.
// pgutil passes credentials through the child environment, bounds its lifetime,
// removes partial output, and returns redacted errors.
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

	snapshotPath := filepath.Join(tmpDir, fmt.Sprintf("corescope-backup-%d.dump", ts))

	if err := pgutil.Dump(r.Context(), s.db.path, snapshotPath); err != nil {
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
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"corescope-backup-%d.dump\"", ts))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, f); err != nil {
		// Headers already flushed; just log. Client will see truncated stream.
		log.Printf("[backup] stream error: %v", err)
	}
}
