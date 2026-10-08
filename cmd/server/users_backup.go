package main

import (
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// users.db snapshots (docs/specs/2026-10-08-account-export-and-users-backup-design.md).
const (
	usersBackupInterval = 24 * time.Hour
	usersBackupLayout   = "20060102-150405" // UTC, in the file name
)

// usersBackupName matches the snapshot files this code writes and rotates.
// Apart from orphaned temporary files (usersBackupTemp), nothing else in
// the directory is ever touched.
var usersBackupName = regexp.MustCompile(`^users-\d{8}-\d{6}\.db$`)

// usersBackupTemp matches the temporary name a snapshot is written under.
// One is only left behind when the process died mid-write.
var usersBackupTemp = regexp.MustCompile(`^users-\d{8}-\d{6}\.db\.tmp$`)

// usersBackupTempMaxAge is how old an orphaned temporary snapshot must be
// before the sweep removes it; no snapshot write takes this long.
const usersBackupTempMaxAge = 24 * time.Hour

func usersBackupFile(t time.Time) string { return "users-" + t.UTC().Format(usersBackupLayout) + ".db" }

// listUsersBackups returns the snapshot file names in dir, oldest first
// (the UTC timestamp sorts by name). A missing dir has none.
func listUsersBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && usersBackupName.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// usersBackupDue reports whether a snapshot is due at now: the newest
// snapshot whose name time is not in the future is at least
// usersBackupInterval old, or there is none. Future names (a clock stepped
// back) are skipped so they cannot stop the backups.
func usersBackupDue(names []string, now time.Time) bool {
	for i := len(names) - 1; i >= 0; i-- {
		t, err := time.Parse(usersBackupLayout, strings.TrimSuffix(strings.TrimPrefix(names[i], "users-"), ".db"))
		if err != nil || t.After(now) {
			continue
		}
		return now.Sub(t) >= usersBackupInterval
	}
	return true
}

// writeUsersBackup snapshots st into dir (created 0700) under a temporary
// name and renames it when complete. It returns the path and size.
func writeUsersBackup(st *users.Store, dir string, now time.Time) (string, int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, usersBackupFile(now))
	tmp := path + ".tmp"
	if err := st.Snapshot(tmp); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return path, fi.Size(), nil
}

// sweepUsersBackupTemps removes orphaned temporary snapshots (regular files
// named like usersBackupTemp, modified more than usersBackupTempMaxAge
// before now) and returns how many it removed. A missing dir has none.
func sweepUsersBackupTemps(dir string, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !usersBackupTemp.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			return removed, err
		}
		if now.Sub(fi.ModTime()) <= usersBackupTempMaxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// rotateUsersBackups deletes the oldest snapshots beyond keep and returns
// how many remain. It never deletes justWritten, which a future-dated name
// (a clock that ran ahead) would otherwise push out as the oldest.
func rotateUsersBackups(dir string, keep int, justWritten string) (int, error) {
	names, err := listUsersBackups(dir)
	if err != nil {
		return 0, err
	}
	kept := len(names)
	for _, n := range names {
		if kept <= keep {
			break
		}
		if n == justWritten {
			continue
		}
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return kept, err
		}
		kept--
	}
	return kept, nil
}

// maybeBackup sweeps orphaned temporary files, takes a snapshot when one
// is due and then rotates. The
// janitor calls it every hour, the first time at startup. A failure is
// logged and leaves the existing snapshots; the next run tries again.
func (a *authService) maybeBackup(now time.Time) {
	b := a.set.backup
	if !b.enabled {
		return
	}
	if n, err := sweepUsersBackupTemps(b.dir, now); err != nil {
		log.Printf("[users] backup temp sweep failed: %v", err)
	} else if n > 0 {
		log.Printf("[users] backup removed %d orphaned temporary file(s)", n)
	}
	names, err := listUsersBackups(b.dir)
	if err != nil {
		log.Printf("[users] backup failed: %v", err)
		return
	}
	if !usersBackupDue(names, now) {
		return
	}
	path, size, err := writeUsersBackup(a.st, b.dir, now)
	if err != nil {
		log.Printf("[users] backup failed: %v", err)
		return
	}
	kept, err := rotateUsersBackups(b.dir, b.keep, filepath.Base(path))
	if err != nil {
		log.Printf("[users] backup rotation failed: %v", err)
	}
	log.Printf("[users] backup written: %s (%d bytes, kept %d)", absForLog(path), size, kept)
}

// handleAdminUsersBackup streams a fresh users.db snapshot from a temp
// directory, removed afterwards. The file holds password hashes and
// addresses: admin only, audited as user.backup.
func (s *Server) handleAdminUsersBackup(w http.ResponseWriter, _ *http.Request, admin *users.User, _ *users.Session) {
	a := s.auth
	tmpDir, err := os.MkdirTemp("", "corescope-users-")
	if err != nil {
		log.Printf("[users] backup download: temp dir: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			log.Printf("[users] backup download cleanup: %v", err)
		}
	}()
	name := "corescope-users-" + time.Now().UTC().Format(usersBackupLayout) + ".db"
	path := filepath.Join(tmpDir, name)
	if err := a.st.Snapshot(path); err != nil {
		log.Printf("[users] backup download: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("[users] backup download: open snapshot: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer f.Close() // runs before the RemoveAll above (LIFO), which Windows needs
	if fi, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	a.audit(idPtr(admin.ID), "user.backup", nil, nil)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("[users] backup download stream: %v", err)
	}
}
