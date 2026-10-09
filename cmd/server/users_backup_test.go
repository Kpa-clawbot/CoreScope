package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

var backupNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// backupService is a test auth service with backups on in a fresh, not yet
// created directory.
func backupService(t *testing.T, keep int) (*authService, string) {
	t.Helper()
	a, _ := newTestBackupAuthService(t)
	dir := filepath.Join(t.TempDir(), "backups")
	a.set.backup = backupSettings{enabled: true, dir: dir, keep: keep}
	return a, dir
}

func snapshotNames(t *testing.T, dir string) []string {
	t.Helper()
	names, err := listUsersBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func writeBackupFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsersBackupWrittenWhenNoneExists(t *testing.T) {
	a, dir := backupService(t, 7)
	if _, err := a.st.CreatePending("a@example.org", "Aaa", "$argon2id$placeholder"); err != nil {
		t.Fatal(err)
	}
	a.maybeBackup(backupNow)
	names := snapshotNames(t, dir)
	if len(names) != 1 || names[0] != testUsersBackupFile(backupNow) || names[0] != testNativeSQL("users-20261008-120000.db", "users-20261008-120000.dump") {
		t.Fatalf("snapshots = %v", names)
	}
	db := restorePostgresTestBackup(t, filepath.Join(dir, names[0]))
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'a@example.org'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("user rows in the snapshot = %d, %v", n, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestUsersBackupFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	a, dir := backupService(t, 7)
	a.maybeBackup(backupNow)
	fi, err := os.Stat(filepath.Join(dir, testUsersBackupFile(backupNow)))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v; want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v; want 0700", di.Mode().Perm())
	}
}

func TestUsersBackupSkippedWhenFresh(t *testing.T) {
	a, dir := backupService(t, 7)
	fresh := testUsersBackupFile(backupNow.Add(-time.Hour))
	writeBackupFile(t, dir, fresh)
	a.maybeBackup(backupNow)
	a.maybeBackup(backupNow.Add(22 * time.Hour))
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, []string{fresh}) {
		t.Fatalf("snapshots = %v; want only %s", names, fresh)
	}
}

func TestUsersBackupWrittenWhenStale(t *testing.T) {
	a, dir := backupService(t, 7)
	stale := testUsersBackupFile(backupNow.Add(-25 * time.Hour))
	writeBackupFile(t, dir, stale)
	a.maybeBackup(backupNow)
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, []string{stale, testUsersBackupFile(backupNow)}) {
		t.Fatalf("snapshots = %v", names)
	}
}

func TestUsersBackupFutureSnapshotDoesNotBlock(t *testing.T) {
	a, dir := backupService(t, 7)
	future := testUsersBackupFile(backupNow.Add(48 * time.Hour))
	writeBackupFile(t, dir, future)
	a.maybeBackup(backupNow)
	want := []string{testUsersBackupFile(backupNow), future}
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, want) {
		t.Fatalf("snapshots = %v; want %v", names, want)
	}
	// The snapshot just written is the newest one not in the future: fresh.
	a.maybeBackup(backupNow.Add(time.Hour))
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, want) {
		t.Fatalf("after an hour = %v; want %v", names, want)
	}
}

func TestUsersBackupRotationNeverDeletesTheNewSnapshot(t *testing.T) {
	a, dir := backupService(t, 1)
	future := testUsersBackupFile(backupNow.Add(48 * time.Hour))
	writeBackupFile(t, dir, future)
	a.maybeBackup(backupNow)
	want := []string{testUsersBackupFile(backupNow)}
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, want) {
		t.Fatalf("snapshots = %v; want %v", names, want)
	}
}

func TestUsersBackupRotationKeepsNewestAndForeignFiles(t *testing.T) {
	a, dir := backupService(t, 3)
	var old []string // oldest first
	for i := 5; i >= 1; i-- {
		n := testUsersBackupFile(backupNow.Add(-time.Duration(i) * 25 * time.Hour))
		writeBackupFile(t, dir, n)
		old = append(old, n)
	}
	foreign := []string{"notes.txt", "users-before-upgrade.db", "users.db", testUsersBackupFile(backupNow.Add(-300*time.Hour)) + ".tmp"}
	for _, n := range foreign {
		writeBackupFile(t, dir, n)
	}
	a.maybeBackup(backupNow)
	want := []string{old[3], old[4], testUsersBackupFile(backupNow)}
	if names := snapshotNames(t, dir); !reflect.DeepEqual(names, want) {
		t.Fatalf("after rotation = %v; want %v", names, want)
	}
	for _, n := range foreign {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("foreign file %s was touched: %v", n, err)
		}
	}
}

func TestUsersBackupSweepsOldTempFiles(t *testing.T) {
	a, dir := backupService(t, 7)
	setAge := func(name string, age time.Duration) {
		t.Helper()
		at := backupNow.Add(-age)
		if err := os.Chtimes(filepath.Join(dir, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	orphan := testUsersBackupFile(backupNow.Add(-30*time.Hour)) + ".tmp"
	recent := testUsersBackupFile(backupNow.Add(-time.Hour)) + ".tmp"
	foreign := []string{"other.db.tmp", "users-20261001-000000.db.tmp.bak", "users-before-upgrade.db.tmp"}
	for _, n := range append([]string{orphan, recent}, foreign...) {
		writeBackupFile(t, dir, n)
		setAge(n, 25*time.Hour)
	}
	setAge(recent, time.Hour)
	dirLike := testUsersBackupFile(backupNow.Add(-40*time.Hour)) + ".tmp"
	if err := os.Mkdir(filepath.Join(dir, dirLike), 0o700); err != nil {
		t.Fatal(err)
	}
	setAge(dirLike, 25*time.Hour)
	// A fresh snapshot: no new one is due, the sweep still runs.
	writeBackupFile(t, dir, testUsersBackupFile(backupNow.Add(-time.Hour)))

	a.maybeBackup(backupNow)
	if _, err := os.Stat(filepath.Join(dir, orphan)); !os.IsNotExist(err) {
		t.Errorf("orphaned temp file older than 24h still there: %v", err)
	}
	for _, n := range append([]string{recent, dirLike}, foreign...) {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was touched: %v", n, err)
		}
	}
}

func TestUsersBackupFailureKeepsOldSnapshots(t *testing.T) {
	a, dir := backupService(t, 1)
	stale := testUsersBackupFile(backupNow.Add(-48 * time.Hour))
	writeBackupFile(t, dir, stale)
	a.st.Close() // every store call fails from here; the fixture's cleanup closes again, which is a no-op
	a.maybeBackup(backupNow)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != stale {
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("directory after a failed snapshot = %v; want only %s", got, stale)
	}
}

func TestUsersBackupDisabled(t *testing.T) {
	a, dir := backupService(t, 7)
	a.set.backup.enabled = false
	a.maybeBackup(backupNow)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("backup directory created while disabled: %v", err)
	}
}

func TestInitUserManagementBacksUpAtStartup(t *testing.T) {
	a, _ := newTestBackupAuthService(t)
	dir := filepath.Join(t.TempDir(), "backups")
	u := validUM()
	u.DatabaseURL = a.set.databaseURL
	u.Backup = &UsersBackupConfig{Dir: dir}
	srv := &Server{cfg: &Config{UserManagement: u}}
	if err := srv.initUserManagement(testDatabaseDSN(t)); err != nil {
		t.Fatal(err)
	}
	srv.closeUserManagement()
	if names := snapshotNames(t, dir); len(names) != 1 {
		t.Fatalf("snapshots after startup = %v; want 1", names)
	}
}

var usersBackupFilenameRE = regexp.MustCompile(`^attachment; filename="corescope-users-\d{8}-\d{6}\.(?:db|dump)"$`)

func TestAdminUsersBackupDownload(t *testing.T) {
	f := newBackupAuthFixture(t, "boss@example.org")
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	uma := f.registerAndActivate(t, "uma@example.org", "Uma", pw)
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	expectStatus(t, f.do("GET", "/api/admin/users-backup", nil), 401)
	expectStatus(t, f.do("GET", "/api/admin/users-backup", nil, as(uma)), 403)

	w := f.do("GET", "/api/admin/users-backup", nil, as(boss))
	expectStatus(t, w, 200)
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if cd := w.Header().Get("Content-Disposition"); !usersBackupFilenameRE.MatchString(cd) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, []byte(postgresMagic)) {
		t.Fatalf("body is not a PostgreSQL custom archive (%d bytes)", len(body))
	}
	if cl := w.Header().Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q; body is %d bytes", cl, len(body))
	}
	path := filepath.Join(t.TempDir(), "download.dump")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	db := restorePostgresTestBackup(t, path)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("users in the download = %d, %v; want 2", n, err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temporary files left in the temp dir: %d", len(left))
	}
	entries, err := f.st.AuditList(users.AuditFilter{Actions: []string{"user.backup"}})
	if err != nil || len(entries) != 1 {
		t.Fatalf("user.backup rows = %d, %v; want 1", len(entries), err)
	}
	if e := entries[0]; e.ActorUserID == nil || *e.ActorUserID != boss.me.ID || e.TargetUserID != nil {
		t.Fatalf("user.backup row = %+v", e)
	}
}

func testUsersBackupFile(stamp time.Time) string { return usersBackupFile(stamp, testBackendValue()) }
