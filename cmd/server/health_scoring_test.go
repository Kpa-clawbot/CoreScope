package main

import (
	"math"
	"testing"
)

const pctEpsilon = 1e-9

func TestScoreSession(t *testing.T) {
	tests := []struct {
		name             string
		seenKeys         []string
		allowlistEnabled bool
		expectedKeys     []string
		activeObs        []string
		wantLabel        string
		wantPct          float64
	}{
		{
			name:             "VERY HEALTHY — 80%+ with allowlist",
			seenKeys:         []string{"a", "b", "c", "d"},
			allowlistEnabled: true,
			expectedKeys:     []string{"a", "b", "c", "d", "e"},
			wantLabel:        "VERY HEALTHY",
			wantPct:          80.0,
		},
		{
			name:             "GOOD — 60–79%",
			seenKeys:         []string{"a", "b", "c"},
			allowlistEnabled: true,
			expectedKeys:     []string{"a", "b", "c", "d", "e"},
			wantLabel:        "GOOD",
			wantPct:          60.0,
		},
		{
			name:             "FAIR — 30–59%",
			seenKeys:         []string{"a"},
			allowlistEnabled: true,
			expectedKeys:     []string{"a", "b", "c"},
			wantLabel:        "FAIR",
			wantPct:          float64(1) / float64(3) * 100,
		},
		{
			name:             "POOR — under 30%",
			seenKeys:         []string{"a"},
			allowlistEnabled: true,
			expectedKeys:     []string{"a", "b", "c", "d", "e", "f"},
			wantLabel:        "POOR",
			wantPct:          float64(1) / float64(6) * 100,
		},
		{
			name:             "no allowlist — uses active observers",
			seenKeys:         []string{"a", "b", "c", "d"},
			allowlistEnabled: false,
			activeObs:        []string{"a", "b", "c", "d", "e"},
			wantLabel:        "VERY HEALTHY",
			wantPct:          80.0,
		},
		{
			name:             "zero expected — returns POOR with 0%",
			seenKeys:         []string{},
			allowlistEnabled: false,
			activeObs:        []string{},
			wantLabel:        "POOR",
			wantPct:          0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var receipts []HealthReceipt
			for _, k := range tc.seenKeys {
				receipts = append(receipts, HealthReceipt{ObserverKey: k})
			}
			sess := &HealthSession{
				AllowlistEnabled:     tc.allowlistEnabled,
				ExpectedObserverKeys: tc.expectedKeys,
			}
			score := ScoreSession(sess, receipts, tc.activeObs)
			if score.Label != tc.wantLabel {
				t.Errorf("label: want %q got %q (%.2f%%)", tc.wantLabel, score.Label, score.Percentage)
			}
			if math.Abs(score.Percentage-tc.wantPct) > pctEpsilon {
				t.Errorf("pct: want %.10f got %.10f", tc.wantPct, score.Percentage)
			}
		})
	}
}
