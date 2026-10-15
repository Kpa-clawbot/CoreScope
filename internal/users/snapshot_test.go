package users

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
)

func TestSnapshotCopiesRows(t *testing.T) {
	st, clk := newTestStoreAt(t, testTarget(t, true))
	u := mustCreate(t, st, "a@example.org", "Aaa")
	if err := st.Activate(u.ID, RoleUser, nil); err != nil {
		t.Fatal(err)
	}
	rawSession, session, err := st.CreateSession(u.ID, 24*time.Hour, "backup-test")
	if err != nil {
		t.Fatal(err)
	}
	usedToken, err := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ConsumeToken(usedToken, PurposeReset); err != nil {
		t.Fatal(err)
	}
	liveToken, err := st.IssueToken(u.ID, PurposeEmailChange, time.Hour, "new@example.org")
	if err != nil {
		t.Fatal(err)
	}
	uid := u.ID
	if err := st.Audit(&uid, "user.register", &uid, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snap.dump")
	if err := st.Snapshot(path); err != nil {
		t.Fatal(err)
	}
	target := path
	if st.backend == dbconfig.Postgres {
		target = testTarget(t, true)
		env, err := pgutil.CommandEnv(target)
		if err != nil {
			t.Fatal(err)
		}
		// --dbname is needed for restore mode; use a credential-free database name.
		cfg, err := pgutil.ParseConfig(target)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("pg_restore", "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "--dbname="+cfg.Database, path)
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			t.Fatal("native account restore failed")
		}
	}
	cp, err := Open(testRuntimeURL(t, target))
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	cp.SetClock(clk.Now)
	got, err := cp.GetByEmail("a@example.org")
	if err != nil || got.ID != u.ID || got.DisplayName != "Aaa" {
		t.Fatalf("user in snapshot = %+v, %v", got, err)
	}
	if v, err := cp.SchemaVersion(); err != nil || v != schemaVersion(t) {
		t.Fatalf("snapshot schema version = %d, %v; want %d", v, err, schemaVersion(t))
	}
	if e, err := cp.AuditAllFor(u.ID); err != nil || len(e) != 1 {
		t.Fatalf("audit in snapshot = %d, %v", len(e), err)
	}
	if got.PasswordHash != u.PasswordHash || got.Status != StatusActive {
		t.Fatal("password hash or account status changed")
	}
	if restored, err := cp.LookupSession(rawSession); err != nil || restored.ID != session.ID || restored.CSRFToken != session.CSRFToken {
		t.Fatal("restored session/CSRF changed")
	}
	if _, err := cp.TokenUser(usedToken, PurposeReset); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("used token revived after restore")
	}
	if id, email, err := cp.ConsumeToken(liveToken, PurposeEmailChange); err != nil || id != u.ID || email != "new@example.org" {
		t.Fatal("unconsumed token did not survive restore")
	}
}

func TestSnapshotFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	st, _ := newTestStoreAt(t, testTarget(t, true))
	path := filepath.Join(t.TempDir(), "snap.dump")
	if err := st.Snapshot(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v; want 0600", fi.Mode().Perm())
	}
}

func TestSnapshotRefusesExistingTarget(t *testing.T) {
	st, _ := newTestStoreAt(t, testTarget(t, true))
	path := filepath.Join(t.TempDir(), "snap.dump")
	// Existing empty files must also be preserved.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := st.Snapshot(path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v; want already exists", err)
	}
	if fi, _ := os.Stat(path); fi == nil || fi.Size() != 0 {
		t.Fatal("the existing file was changed")
	}
}

func TestSnapshotFailureLeavesNoFile(t *testing.T) {
	st, _ := newTestStoreAt(t, testTarget(t, true))
	st.Close()
	path := filepath.Join(t.TempDir(), "snap.dump")
	if err := st.Snapshot(path); err == nil {
		t.Fatal("snapshot of a closed store succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial snapshot left behind: %v", err)
	}
}

func TestSnapshotCanceledLeavesNoFile(t *testing.T) {
	st, _ := newTestStoreAt(t, testTarget(t, true))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "cancel.dump")
	if err := st.SnapshotContext(ctx, path); err == nil {
		t.Fatal("canceled snapshot succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceled snapshot left a file")
	}
}
