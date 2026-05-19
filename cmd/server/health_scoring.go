package main

type CoverageScore struct {
	SeenCount     int     `json:"seenCount"`
	ExpectedCount int     `json:"expectedCount"`
	Percentage    float64 `json:"percentage"`
	Label         string  `json:"label"`
}

func ScoreSession(session *HealthSession, receipts []HealthReceipt, activeObserverKeys []string) CoverageScore {
	expectedCount := len(session.ExpectedObserverKeys)
	if !session.AllowlistEnabled || expectedCount == 0 {
		expectedCount = len(activeObserverKeys)
	}

	seen := make(map[string]struct{}, len(receipts))
	for _, r := range receipts {
		seen[r.ObserverKey] = struct{}{}
	}
	seenCount := len(seen)

	var pct float64
	if expectedCount > 0 {
		pct = float64(seenCount) / float64(expectedCount) * 100
	}

	label := "POOR"
	switch {
	case pct >= 80:
		label = "VERY HEALTHY"
	case pct >= 60:
		label = "GOOD"
	case pct >= 30:
		label = "FAIR"
	}

	return CoverageScore{
		SeenCount:     seenCount,
		ExpectedCount: expectedCount,
		Percentage:    pct,
		Label:         label,
	}
}
