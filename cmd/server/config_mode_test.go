package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// SaveGeoFilter rewrites config.json through a temp file. The file holds the
// API key and broker passwords, so the rewrite must keep whatever mode the
// operator set rather than resetting it to 0644.
func TestSaveGeoFilterPreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support POSIX file permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"apiKey":"secret-key-that-is-long-enough"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gf := &GeoFilterConfig{Polygon: [][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}}}
	if err := SaveGeoFilter(dir, gf); err != nil {
		t.Fatalf("SaveGeoFilter: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Fatalf("config.json mode after save = %o, want 0600", got)
	}
	// Default stays 0644 for a file that was 0644.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveGeoFilter(dir, nil); err != nil {
		t.Fatalf("SaveGeoFilter(nil): %v", err)
	}
	fi, _ = os.Stat(path)
	if got := fi.Mode().Perm(); got != 0644 {
		t.Fatalf("config.json mode after save = %o, want 0644", got)
	}
}
