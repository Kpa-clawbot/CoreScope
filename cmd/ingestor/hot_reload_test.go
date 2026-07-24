package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writeTestConfig writes a minimal config.json with the given hashChannels/
// hashRegions to a temp file and returns its path.
func writeTestConfig(t *testing.T, hashChannels, hashRegions []string) string {
	t.Helper()
	cfg := struct {
		HashChannels []string `json:"hashChannels"`
		HashRegions  []string `json:"hashRegions"`
	}{HashChannels: hashChannels, HashRegions: hashRegions}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func TestHotKeys_ReloadPicksUpNewChannelsAndRegions(t *testing.T) {
	configPath := writeTestConfig(t, []string{"#alpha"}, []string{"#alpha"})

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("initial LoadConfig: %v", err)
	}
	hk := newHotKeys(loadChannelKeys(cfg, configPath), loadRegionKeys(cfg))

	if _, ok := hk.Channels()["#alpha"]; !ok {
		t.Fatalf("initial channel keys = %v, want #alpha present", hk.Channels())
	}
	if _, ok := hk.Regions()["#beta"]; ok {
		t.Fatalf("#beta should not be present before reload")
	}

	// Rewrite config with an additional channel + region, then reload.
	if err := os.WriteFile(configPath, mustJSON(t, struct {
		HashChannels []string `json:"hashChannels"`
		HashRegions  []string `json:"hashRegions"`
	}{HashChannels: []string{"#alpha", "#beta"}, HashRegions: []string{"#alpha", "#beta"}}), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	if err := hk.reload(configPath); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if _, ok := hk.Channels()["#beta"]; !ok {
		t.Fatalf("channel keys after reload = %v, want #beta present", hk.Channels())
	}
	if _, ok := hk.Regions()["#beta"]; !ok {
		t.Fatalf("region keys after reload = %v, want #beta present", hk.Regions())
	}
	// The original key must still be present — reload adds, doesn't
	// require re-deriving keys already known.
	if _, ok := hk.Channels()["#alpha"]; !ok {
		t.Fatalf("channel keys after reload lost #alpha: %v", hk.Channels())
	}
}

func TestHotKeys_ReloadKeepsPreviousKeysOnParseError(t *testing.T) {
	configPath := writeTestConfig(t, []string{"#alpha"}, []string{"#alpha"})
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("initial LoadConfig: %v", err)
	}
	hk := newHotKeys(loadChannelKeys(cfg, configPath), loadRegionKeys(cfg))
	before := hk.Channels()

	// Corrupt the config file — reload must fail without blanking the keys.
	if err := os.WriteFile(configPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
	if err := hk.reload(configPath); err == nil {
		t.Fatal("reload with malformed config should return an error")
	}
	if len(hk.Channels()) != len(before) {
		t.Fatalf("keys changed after failed reload: before=%v after=%v", before, hk.Channels())
	}
}

// TestStartSIGHUPReload_ActuallyReloadsOnSignal is an end-to-end check that
// a real SIGHUP delivered to this process triggers hk.reload via the
// goroutine wired up in startSIGHUPReload.
func TestStartSIGHUPReload_ActuallyReloadsOnSignal(t *testing.T) {
	configPath := writeTestConfig(t, nil, []string{"#alpha"})
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("initial LoadConfig: %v", err)
	}
	hk := newHotKeys(loadChannelKeys(cfg, configPath), loadRegionKeys(cfg))

	stop := startSIGHUPReload(hk, configPath)
	defer stop()

	if err := os.WriteFile(configPath, mustJSON(t, struct {
		HashRegions []string `json:"hashRegions"`
	}{HashRegions: []string{"#alpha", "#gamma"}}), 0o644); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := hk.Regions()["#gamma"]; ok {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("region keys after SIGHUP = %v, want #gamma present within 2s", hk.Regions())
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
