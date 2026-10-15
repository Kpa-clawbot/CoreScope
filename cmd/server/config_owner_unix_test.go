//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSaveGeoFilterKeepsOwner checks that a save keeps the owner and group of
// config.json. The image runs the server as root, and a bind-mounted config
// that came back as root:root 0600 would lock the operator out of their own
// file.
func TestSaveGeoFilterKeepsOwner(t *testing.T) {
	// Pick an owner the save would not produce by itself. Root can use any
	// uid and gid; anyone else can only switch to a supplementary group.
	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		uid, gid = 4242, 4242
	} else if groups, err := os.Getgroups(); err == nil {
		for _, g := range groups {
			if g != os.Getegid() {
				gid = g
				break
			}
		}
	}
	if gid == -1 {
		t.Skip("not root and no supplementary group to chown to")
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"port":3000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(cfg, uid, gid); err != nil {
		t.Fatalf("chown %s: %v", cfg, err)
	}

	gf := &GeoFilterConfig{Polygon: [][2]float64{{51.0, 4.0}, {51.0, 5.0}, {50.5, 4.0}}}
	if err := SaveGeoFilter(dir, gf); err != nil {
		t.Fatalf("SaveGeoFilter: %v", err)
	}

	fi, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if uid != -1 && int(st.Uid) != uid {
		t.Errorf("owner of %s = %d, want %d", cfg, st.Uid, uid)
	}
	if int(st.Gid) != gid {
		t.Errorf("group of %s = %d, want %d", cfg, st.Gid, gid)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode of %s = %#o, want 0600", cfg, perm)
	}
}

// TestSaveGeoFilterFifoSymlinkTarget pins the regular-file check on the
// symlink target: a config.json that links to a fifo is not followed, so the
// save never replaces the fifo with a regular file. A directory target would
// not test this, since reading the config already fails there.
func TestSaveGeoFilterFifoSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "feed")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(fifo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// SaveGeoFilter is the fifo's reader; feed it one config.
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		f.WriteString(`{"port":3000}`)
		f.Close()
	}()
	if err := SaveGeoFilter(dir, nil); err != nil {
		t.Fatalf("SaveGeoFilter: %v", err)
	}

	fi, err := os.Lstat(fifo)
	if err != nil {
		t.Fatalf("lstat %s: %v", fifo, err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("%s was replaced (mode %s) — the link to a fifo was followed", fifo, fi.Mode())
	}
}
