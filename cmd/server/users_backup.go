package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
// Nothing else in the directory is ever touched.
var usersBackupName = regexp.MustCompile(`^users-\d{8}-\d{6}\.db$`)

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

// rotateUsersBackups deletes the oldest snapshots beyond keep and returns
// how many remain.
func rotateUsersBackups(dir string, keep int) (int, error) {
	names, err := listUsersBackups(dir)
	if err != nil {
		return 0, err
	}
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return len(names), err
		}
		names = names[1:]
	}
	return len(names), nil
}

// maybeBackup takes a snapshot when one is due and then rotates. The
// janitor calls it every hour, the first time at startup. A failure is
// logged and leaves the existing snapshots; the next run tries again.
func (a *authService) maybeBackup(now time.Time) {
	b := a.set.backup
	if !b.enabled {
		return
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
	kept, err := rotateUsersBackups(b.dir, b.keep)
	if err != nil {
		log.Printf("[users] backup rotation failed: %v", err)
	}
	log.Printf("[users] backup written: %s (%d bytes, kept %d)", absForLog(path), size, kept)
}
