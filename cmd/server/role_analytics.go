package main

import (
	"net/http"
)

// RoleStats summarises one role's population and clock-skew posture.
type RoleStats struct {
	Role               string  `json:"role"`
	NodeCount          int     `json:"nodeCount"`
	WithSkew           int     `json:"withSkew"`
	MeanAbsSkewSec     float64 `json:"meanAbsSkewSec"`
	MedianAbsSkewSec   float64 `json:"medianAbsSkewSec"`
	OkCount            int     `json:"okCount"`
	WarningCount       int     `json:"warningCount"`
	CriticalCount      int     `json:"criticalCount"`
	AbsurdCount        int     `json:"absurdCount"`
	NoClockCount       int     `json:"noClockCount"`
}

// RoleAnalyticsResponse is the payload returned by /api/analytics/roles.
type RoleAnalyticsResponse struct {
	TotalNodes int         `json:"totalNodes"`
	Roles      []RoleStats `json:"roles"`
}

// normalizeRole canonicalises a role string (stub: real impl in green commit).
func normalizeRole(r string) string { return r }

// computeRoleAnalytics groups nodes by role and aggregates clock-skew per role.
// Stub returns empty result so tests fail on assertions, not import errors.
func computeRoleAnalytics(nodesByPubkey map[string]string, skewByPubkey map[string]*NodeClockSkew) RoleAnalyticsResponse {
	return RoleAnalyticsResponse{Roles: []RoleStats{}}
}

// handleAnalyticsRoles serves /api/analytics/roles. Stub for red commit.
func (s *Server) handleAnalyticsRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, RoleAnalyticsResponse{Roles: []RoleStats{}})
}
