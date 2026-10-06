package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsAllowlistShape(t *testing.T) {
	seen := map[string]bool{}
	sets := map[string]string{}
	for _, k := range settingsAllowlist {
		if seen[k.Key] {
			t.Errorf("duplicate allowlist key %q", k.Key)
		}
		seen[k.Key] = true
		switch k.Kind {
		case settingsKindSet:
			sets[k.Key] = k.ID
		case settingsKindScalar:
			if k.ID != "" {
				t.Errorf("scalar %q has an identity field", k.Key)
			}
		default:
			t.Errorf("key %q has kind %q", k.Key, k.Kind)
		}
		if settingsDenied(k.Key) {
			t.Errorf("allowlist contains denied key %q", k.Key)
		}
	}
	want := map[string]string{"meshcore-my-nodes": "pubkey", "meshcore-favorites": "", "corescope_saved_filters_v1": "name"}
	if len(sets) != len(want) {
		t.Fatalf("set keys = %v; want %v", sets, want)
	}
	for k, id := range want {
		if got, ok := sets[k]; !ok || got != id {
			t.Errorf("set %q identity = %q (present %v); want %q", k, got, ok, id)
		}
	}
	if len(settingsAllowlist) != 62 {
		t.Errorf("allowlist has %d keys; the spec table has 62", len(settingsAllowlist))
	}
}

func TestSettingsDenied(t *testing.T) {
	for _, k := range []string{"meshcore-api-key", "corescope_channel_keys", "corescope_channel_labels", "corescope_channel_cache", "corescope_channel_anything"} {
		if !settingsDenied(k) {
			t.Errorf("settingsDenied(%q) = false", k)
		}
	}
	for _, k := range []string{"meshcore-favorites", "corescope_saved_filters_v1", "meshcore-api-key-hint", "corescope_channel"} {
		if settingsDenied(k) {
			t.Errorf("settingsDenied(%q) = true", k)
		}
	}
}

func TestSyncedSettingsKeysDropsDeniedKeys(t *testing.T) {
	saved := settingsAllowlist
	t.Cleanup(func() { settingsAllowlist = saved })
	settingsAllowlist = append(append([]settingsKey{}, saved...),
		settingsKey{Key: "corescope_channel_keys", Kind: settingsKindScalar},
		settingsKey{Key: "meshcore-api-key", Kind: settingsKindScalar})
	got := syncedSettingsKeys()
	if len(got) != len(saved) {
		t.Fatalf("syncedSettingsKeys has %d keys; want %d", len(got), len(saved))
	}
	for _, k := range got {
		if settingsDenied(k.Key) {
			t.Errorf("denied key %q returned", k.Key)
		}
	}
}

// Every synced key must still be used by the frontend: a renamed key would
// otherwise sync a value nothing reads. Keys are matched as quoted string
// literals, so a key that is a prefix of another does not pass by accident.
func TestSettingsAllowlistKeysOccurInPublic(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "public", "*.js"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no public/*.js found (%v)", err)
	}
	var all strings.Builder
	for _, f := range files {
		if filepath.Base(f) == "settings-sync.js" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	src := all.String()
	for _, k := range settingsAllowlist {
		if !strings.Contains(src, "'"+k.Key+"'") && !strings.Contains(src, `"`+k.Key+`"`) {
			t.Errorf("allowlisted key %q no longer occurs as a string literal in public/*.js; remove it or follow the rename", k.Key)
		}
	}
}
