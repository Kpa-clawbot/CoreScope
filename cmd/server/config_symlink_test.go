package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveGeoFilterSymlinkedConfig pins the behavior the Docker image depends
// on: /app/config.json is a symlink to the bind-mounted /app/data/config.json,
// so a geo-filter save has to land in the mounted file and leave the symlink a
// symlink. A tmp+rename on the link path instead replaces the link with a
// regular file in the container layer, and the operator's config silently
// stops being the one in use.
func TestSaveGeoFilterSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dataDir, "config.json")
	if err := os.WriteFile(target, []byte(`{"port":3000,"_comment":"keep me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	gf := &GeoFilterConfig{
		Polygon:  [][2]float64{{51.0, 4.0}, {51.0, 5.0}, {50.5, 4.0}},
		BufferKm: 20,
	}
	if err := SaveGeoFilter(dir, gf); err != nil {
		t.Fatalf("SaveGeoFilter: %v", err)
	}

	// The symlink must survive, or the next container start reads a different
	// file than the one that was written.
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat %s: %v", link, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is no longer a symlink (mode %s) — the rename replaced it", link, fi.Mode())
	}

	// The geo filter must be in the mounted file, next to what was there.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse saved config: %v", err)
	}
	if _, ok := raw["geo_filter"]; !ok {
		t.Errorf("geo_filter missing from %s: %s", target, data)
	}
	if raw["_comment"] != "keep me" {
		t.Errorf("unrelated keys not preserved: %s", data)
	}
	if !bytes.Contains(data, []byte(`"bufferKm"`)) {
		t.Errorf("bufferKm missing from saved config: %s", data)
	}

	// The 0600 the operator chose is kept (#2126), and no tmp file is left in
	// either directory.
	if fi, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode of %s = %#o, want 0600", target, perm)
	}
	for _, leftover := range []string{link + ".tmp", target + ".tmp"} {
		if _, err := os.Lstat(leftover); err == nil {
			t.Errorf("leftover temp file %s", leftover)
		}
	}
}
