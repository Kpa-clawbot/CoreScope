package main

import (
	"testing"
)

func TestIngestorIsObserverBlacklisted(t *testing.T) {
	cfg := &Config{
		ObserverBlacklist: []string{"OBS1", "obs2"},
	}

	tests := []struct {
		id   string
		want bool
	}{
		{"OBS1", true},
		{"obs1", true},
		{"OBS2", true},
		{"obs3", false},
		{"", false},
	}

	for _, tt := range tests {
		got := cfg.IsObserverBlacklisted(tt.id)
		if got != tt.want {
			t.Errorf("IsObserverBlacklisted(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestIngestorIsObserverBlacklistedEmpty(t *testing.T) {
	cfg := &Config{}
	if cfg.IsObserverBlacklisted("anything") {
		t.Error("empty blacklist should not match")
	}
}

func TestIngestorIsObserverBlacklistedNil(t *testing.T) {
	var cfg *Config
	if cfg.IsObserverBlacklisted("anything") {
		t.Error("nil config should not match")
	}
}
