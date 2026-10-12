package main

import (
	"encoding/json"
	"testing"
)

func TestRequireLinkedCompanionConfig(t *testing.T) {
	cases := []struct {
		js      string
		set, on bool
	}{
		{`{}`, false, false},
		{`{"clientRxCoverage": {"enabled": true}}`, false, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": false}, "userManagement": {"enabled": true}}`, false, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}}`, true, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}, "userManagement": {"enabled": false}}`, true, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}, "userManagement": {"enabled": true}}`, true, true},
	}
	for _, c := range cases {
		var cfg Config
		if err := json.Unmarshal([]byte(c.js), &cfg); err != nil {
			t.Fatalf("%s: %v", c.js, err)
		}
		if got := cfg.RequireLinkedCompanionSet(); got != c.set {
			t.Errorf("%s: RequireLinkedCompanionSet = %v, want %v", c.js, got, c.set)
		}
		if got := cfg.RequireLinkedCompanion(); got != c.on {
			t.Errorf("%s: RequireLinkedCompanion = %v, want %v", c.js, got, c.on)
		}
	}
}
