package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleBackup streams an atomic SQLite snapshot to the client using VACUUM INTO.
// The snapshot is consistent at the point of the SQL call; concurrent writes are safe.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	tmpDir, err := os.MkdirTemp("", "corescope-backup-*")
	if err != nil {
		log.Printf("[backup] failed to create temp dir: %v", err)
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	ts := time.Now().UTC().Format("20060102-150405")
	outPath := filepath.Join(tmpDir, fmt.Sprintf("meshcore-backup-%s.db", ts))

	// Escape single quotes in path to prevent SQL injection in the literal string.
	safePath := strings.ReplaceAll(outPath, "'", "''")
	if _, err := s.db.conn.Exec(fmt.Sprintf("VACUUM INTO '%s'", safePath)); err != nil {
		log.Printf("[backup] VACUUM INTO failed: %v", err)
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(outPath)
	if err != nil {
		log.Printf("[backup] failed to open backup file: %v", err)
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Printf("[backup] stat failed: %v", err)
		http.Error(w, "backup failed", http.StatusInternalServerError)
		return
	}

	log.Printf("[backup] serving %.1f MB snapshot to %s", float64(fi.Size())/(1<<20), clientIP)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="meshcore-backup-%s.db"`, ts))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, outPath, fi.ModTime(), f)
}
