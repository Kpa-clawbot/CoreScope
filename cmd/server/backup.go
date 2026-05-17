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
//
// Hardening: backups are serialized (one at a time) and spaced by
// backupMinInterval, and a database larger than backupMaxBytes is refused —
// VACUUM INTO is disk/CPU-heavy and the response is otherwise unbounded.
const (
	// backupMaxBytes caps the database size this endpoint will back up.
	backupMaxBytes = 2 << 30 // 2 GiB
	// backupMinInterval is the minimum gap between two backups.
	backupMinInterval = 10 * time.Second
)

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil || s.db.conn == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	// Rate limit + single-flight. VACUUM INTO is expensive; rapid or
	// concurrent calls would thrash the database.
	s.backupMu.Lock()
	if s.backupRunning {
		s.backupMu.Unlock()
		writeError(w, http.StatusTooManyRequests, "a backup is already in progress")
		return
	}
	if !s.backupLastDone.IsZero() {
		if wait := backupMinInterval - time.Since(s.backupLastDone); wait > 0 {
			s.backupMu.Unlock()
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "backup requested too soon; try again shortly")
			return
		}
	}
	s.backupRunning = true
	s.backupMu.Unlock()
	defer func() {
		s.backupMu.Lock()
		s.backupRunning = false
		s.backupLastDone = time.Now()
		s.backupMu.Unlock()
	}()

	// Size guard — refuse to back up an oversized database before paying the
	// cost of VACUUM INTO.
	if s.db.path != "" {
		if st, statErr := os.Stat(s.db.path); statErr == nil && st.Size() > backupMaxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "database too large to back up via this endpoint")
			return
		}
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
		writeInternalError(w, "handleBackup MkdirTemp", err)
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
		writeInternalError(w, "handleBackup VACUUM INTO", err)
		return
	}

	f, err := os.Open(snapshotPath)
	if err != nil {
		writeInternalError(w, "handleBackup open snapshot", err)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		writeInternalError(w, "handleBackup stat snapshot", err)
		return
	}
	if stat.Size() > backupMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "backup snapshot too large to stream")
		return
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"corescope-backup-%d.db\"", ts))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, f); err != nil {
		// Headers already flushed; just log. Client will see truncated stream.
		log.Printf("[backup] stream error: %v", err)
	}
}
