package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are ignored, cannot exercise the read-only directory fallback")
	}
}

var inplaceGF = &GeoFilterConfig{Polygon: [][2]float64{{51, 4}, {51, 5}, {50.5, 4}}, BufferKm: 5}

func writeCfg(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"port":3000,"_comment":"old"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveGeoFilterAtomicWhenDirWritable(t *testing.T) {
	dir, bak := t.TempDir(), t.TempDir()
	p := writeCfg(t, dir)
	if err := SaveGeoFilterBackup(dir, bak, inplaceGF); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "geo_filter") || !strings.Contains(string(b), "_comment") {
		t.Errorf("content wrong: %s", b)
	}
	if es, _ := os.ReadDir(bak); len(es) != 0 {
		t.Errorf("backup written on atomic path: %v", es)
	}
	es, _ := os.ReadDir(dir)
	if len(es) != 1 {
		t.Errorf("stray files in config dir: %v", es)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640", fi.Mode().Perm())
	}
}

func TestSaveGeoFilterFallbackReadOnlyDir(t *testing.T) {
	skipIfRoot(t)
	dir, bak := t.TempDir(), t.TempDir()
	p := writeCfg(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := SaveGeoFilterBackup(dir, bak, inplaceGF); err != nil {
		t.Fatalf("fallback save: %v", err)
	}
	nb, _ := os.ReadFile(p)
	if !strings.Contains(string(nb), "geo_filter") {
		t.Errorf("new content missing: %s", nb)
	}
	ob, err := os.ReadFile(filepath.Join(bak, "config.json.bak"))
	if err != nil || string(ob) != `{"port":3000,"_comment":"old"}` {
		t.Errorf("backup = %q, %v", ob, err)
	}
	if es, _ := os.ReadDir(bak); len(es) != 1 {
		t.Errorf("stray files in backup dir: %v", es)
	}
}

func TestSaveGeoFilterFallbackBackupUnwritable(t *testing.T) {
	skipIfRoot(t)
	dir, bak := t.TempDir(), t.TempDir()
	p := writeCfg(t, dir)
	for _, d := range []string{dir, bak} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
		d := d
		t.Cleanup(func() { os.Chmod(d, 0o755) })
	}
	if err := SaveGeoFilterBackup(dir, bak, inplaceGF); err == nil {
		t.Fatal("expected error when backup dir is unwritable")
	}
	if b, _ := os.ReadFile(p); string(b) != `{"port":3000,"_comment":"old"}` {
		t.Errorf("config changed despite failed backup: %s", b)
	}
}

// SaveGeoFilter resolves a symlink only when it points at a regular file, so
// a symlink can reach the in-place writer only when it points at something
// else. The writer must refuse it (Lstat sees the link itself) and write no
// backup.
func TestWriteConfigInPlaceRefusesSymlink(t *testing.T) {
	dir, bak := t.TempDir(), t.TempDir()
	target := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeConfigInPlace(link, bak, []byte("old"), []byte("new")); err == nil {
		t.Fatal("expected refusal for symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "untouched" {
		t.Errorf("symlink target modified: %s", b)
	}
	if es, _ := os.ReadDir(bak); len(es) != 0 {
		t.Errorf("backup written before refusal: %v", es)
	}
}

func TestWriteConfigInPlaceNeverCreates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := writeConfigInPlace(p, t.TempDir(), []byte("old"), []byte("new")); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("config.json was created")
	}
}
