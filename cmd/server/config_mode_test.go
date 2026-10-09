package main

import (
	"os"
	"path/filepath"
	"testing"
)

// SaveGeoFilter rewrites config.json through a temp file. The file holds the
// API key and broker passwords, so the rewrite must keep whatever mode the
// operator set rather than resetting it to 0644.
func TestSaveGeoFilterPreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"apiKey":"secret-key-that-is-long-enough"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := before.Mode().Perm()
	gf := &GeoFilterConfig{Polygon: [][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}}}
	if err := SaveGeoFilter(dir, gf); err != nil {
		t.Fatalf("SaveGeoFilter: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("config.json mode after save = %o, want %o", got, want)
	}
	// Default stays 0644 for a file that was 0644.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	before, _ = os.Stat(path)
	want = before.Mode().Perm()
	if err := SaveGeoFilter(dir, nil); err != nil {
		t.Fatalf("SaveGeoFilter(nil): %v", err)
	}
	fi, _ = os.Stat(path)
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("config.json mode after save = %o, want %o", got, want)
	}
}
