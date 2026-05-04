package main

import (
	"testing"
)

// TestComputeRoleAnalytics_Distribution verifies that computeRoleAnalytics
// groups nodes by role, normalises empty/case-different roles, and sorts the
// output largest-population first. Asserts on the public RoleAnalyticsResponse
// shape so the bar is "behaviour", not "compiles".
func TestComputeRoleAnalytics_Distribution(t *testing.T) {
	nodes := map[string]string{
		"pk_a": "Repeater",
		"pk_b": "repeater",
		"pk_c": "companion",
		"pk_d": "",
		"pk_e": "ROOM_SERVER",
	}
	got := computeRoleAnalytics(nodes, nil)

	if got.TotalNodes != 5 {
		t.Fatalf("TotalNodes = %d, want 5", got.TotalNodes)
	}
	if len(got.Roles) != 4 {
		t.Fatalf("len(Roles) = %d, want 4 (repeater, companion, room_server, unknown), got %+v", len(got.Roles), got.Roles)
	}
	if got.Roles[0].Role != "repeater" || got.Roles[0].NodeCount != 2 {
		t.Errorf("Roles[0] = %+v, want {repeater,2}", got.Roles[0])
	}
	// Empty roles should bucket as "unknown".
	foundUnknown := false
	for _, r := range got.Roles {
		if r.Role == "unknown" {
			foundUnknown = true
			if r.NodeCount != 1 {
				t.Errorf("unknown bucket NodeCount = %d, want 1", r.NodeCount)
			}
		}
	}
	if !foundUnknown {
		t.Errorf("no 'unknown' bucket for empty roles in %+v", got.Roles)
	}
}

// TestComputeRoleAnalytics_SkewAggregation verifies per-role clock-skew
// aggregation: counts by severity, mean and median absolute skew.
func TestComputeRoleAnalytics_SkewAggregation(t *testing.T) {
	nodes := map[string]string{
		"pk_1": "repeater",
		"pk_2": "repeater",
		"pk_3": "repeater",
	}
	skews := map[string]*NodeClockSkew{
		"pk_1": {Pubkey: "pk_1", RecentMedianSkewSec: 10, Severity: SkewOK},
		"pk_2": {Pubkey: "pk_2", RecentMedianSkewSec: -400, Severity: SkewWarning},
		"pk_3": {Pubkey: "pk_3", RecentMedianSkewSec: 7200, Severity: SkewCritical},
	}
	got := computeRoleAnalytics(nodes, skews)
	if len(got.Roles) != 1 {
		t.Fatalf("len(Roles) = %d, want 1; got %+v", len(got.Roles), got.Roles)
	}
	r := got.Roles[0]
	if r.WithSkew != 3 {
		t.Errorf("WithSkew = %d, want 3", r.WithSkew)
	}
	if r.OkCount != 1 || r.WarningCount != 1 || r.CriticalCount != 1 {
		t.Errorf("severity counts = ok %d, warn %d, crit %d; want 1/1/1", r.OkCount, r.WarningCount, r.CriticalCount)
	}
	// mean(|10|, |−400|, |7200|) = 7610/3 ≈ 2536.67
	if r.MeanAbsSkewSec < 2536 || r.MeanAbsSkewSec > 2537 {
		t.Errorf("MeanAbsSkewSec = %v, want ~2536.67", r.MeanAbsSkewSec)
	}
	// median(10, 400, 7200) = 400
	if r.MedianAbsSkewSec != 400 {
		t.Errorf("MedianAbsSkewSec = %v, want 400", r.MedianAbsSkewSec)
	}
}
