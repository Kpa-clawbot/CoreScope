package main

import (
	"fmt"
	"testing"
	"time"
)

const coverageNodePK = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

// setupCoverageStore holds one packet per hour for the node over the last
// memHours hours, and records that window as loaded the way a bounded load
// does (oldestLoaded): the in-memory window starts memHours ago (#748 M3a).
func setupCoverageStore(t *testing.T, memHours int) *PacketStore {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	if _, err := db.conn.Exec("INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)", coverageNodePK, "Coverage", "repeater"); err != nil {
		t.Fatal(err)
	}
	ps := NewPacketStore(db, nil)
	now := time.Now().UTC()
	pt := 4
	ps.mu.Lock()
	for h := memHours - 1; h >= 0; h-- {
		tx := &StoreTx{
			ID:          memHours - h,
			Hash:        fmt.Sprintf("cov%03d", h),
			PayloadType: &pt,
			FirstSeen:   now.Add(-time.Duration(h)*time.Hour - time.Minute).Format(time.RFC3339),
		}
		ps.packets = append(ps.packets, tx)
		ps.byHash[tx.Hash] = tx
		ps.byTxID[tx.ID] = tx
		ps.byNode[coverageNodePK] = append(ps.byNode[coverageNodePK], tx)
	}
	if memHours > 0 {
		ps.oldestLoaded = ps.packets[0].FirstSeen
	}
	ps.mu.Unlock()
	return ps
}

func rate(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func addCoverageTx(ps *PacketStore, id int, firstSeen time.Time, front bool) {
	pt := 4
	tx := &StoreTx{ID: id, Hash: fmt.Sprintf("extra%d", id), PayloadType: &pt, FirstSeen: firstSeen.Format(time.RFC3339)}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if front {
		ps.packets = append([]*StoreTx{tx}, ps.packets...)
	} else {
		ps.packets = append(ps.packets, tx)
	}
	ps.byHash[tx.Hash] = tx
	ps.byTxID[tx.ID] = tx
	ps.byNode[coverageNodePK] = append([]*StoreTx{tx}, ps.byNode[coverageNodePK]...)
}

func TestNodeAnalyticsCoverageWithinMemory(t *testing.T) {
	// 10 days in memory, 7 requested: the full range is covered.
	ps := setupCoverageStore(t, 10*24)
	a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if a.TimeRange.Partial || a.TimeRange.DataFrom != "" {
		t.Errorf("7d inside a 10d window: partial=%v dataFrom=%q", a.TimeRange.Partial, a.TimeRange.DataFrom)
	}
	if rate(a.ComputedStats.AvailabilityPct) != 100 {
		t.Errorf("availability = %v, want 100 (one packet every hour)", rate(a.ComputedStats.AvailabilityPct))
	}
	if rate(a.ComputedStats.AvgPacketsPerDay) != 24 {
		t.Errorf("packets/day = %v, want 24", rate(a.ComputedStats.AvgPacketsPerDay))
	}
}

func TestNodeAnalyticsCoverageBeyondMemory(t *testing.T) {
	// 3 days in memory, 30 requested: stats must be computed over the 3
	// days that exist, not diluted over 30 (live showed 42.6% for a node
	// that was 88.7% available).
	ps := setupCoverageStore(t, 3*24)
	a, err := ps.GetNodeAnalytics(coverageNodePK, 30)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if !a.TimeRange.Partial {
		t.Fatal("30d over a 3d window must be partial")
	}
	ps.mu.RLock()
	oldest := ps.packets[0].FirstSeen
	ps.mu.RUnlock()
	if a.TimeRange.DataFrom != oldest {
		t.Errorf("dataFrom = %q, want the oldest packet in memory %q", a.TimeRange.DataFrom, oldest)
	}
	if a.TimeRange.Days != 30 {
		t.Errorf("days = %d, want the requested 30", a.TimeRange.Days)
	}
	if got := rate(a.ComputedStats.AvailabilityPct); got < 98 || got > 100 {
		t.Errorf("availability = %v, want ~100 over the covered 3 days", got)
	}
	if got := rate(a.ComputedStats.AvgPacketsPerDay); got < 23.5 || got > 24.5 {
		t.Errorf("packets/day = %v, want ~24 over the covered 3 days", got)
	}
}

func TestNodeAnalyticsCoverageEmptyStore(t *testing.T) {
	// Nothing in memory: no partial claim, stats over the requested range.
	ps := setupCoverageStore(t, 0)
	a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if a.TimeRange.Partial || rate(a.ComputedStats.AvailabilityPct) != 0 {
		t.Errorf("empty store: partial=%v availability=%v", a.TimeRange.Partial, rate(a.ComputedStats.AvailabilityPct))
	}
}

// TestNodeAnalyticsWindowIgnoresLocalZone: the window bound is compared as a
// string with UTC FirstSeen values, so it must be formatted in UTC. With a
// +02:00 local zone the old code dropped the oldest two hours of a 7d range.
func TestNodeAnalyticsWindowIgnoresLocalZone(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("UTC+2", 2*3600)
	t.Cleanup(func() { time.Local = saved })

	ps := setupCoverageStore(t, 10*24)
	a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if a.ComputedStats.TotalPackets != 7*24 {
		t.Errorf("7d window holds %d packets, want %d", a.ComputedStats.TotalPackets, 7*24)
	}
	if a.TimeRange.From[len(a.TimeRange.From)-1] != 'Z' {
		t.Errorf("from = %q, want a UTC timestamp", a.TimeRange.From)
	}
	h, err := ps.GetNodeHopAnalytics(coverageNodePK, 7)
	if err != nil || h == nil {
		t.Fatal(err)
	}
	if h.TimeRange.From[len(h.TimeRange.From)-1] != 'Z' {
		t.Errorf("hop analytics from = %q, want a UTC timestamp", h.TimeRange.From)
	}
}

// A transmission first heard long ago and heard again recently is loaded
// with a recent chunk and sits at the head of s.packets. It must neither
// move the window edge back nor show up as a lone bucket before the window.
func TestNodeAnalyticsCoverageIgnoresReheardOldTx(t *testing.T) {
	ps := setupCoverageStore(t, 3*24)
	ps.mu.Lock()
	ps.retentionHours = 3 * 24
	ps.mu.Unlock()
	addCoverageTx(ps, 9001, time.Now().UTC().Add(-85*24*time.Hour), true)

	a, err := ps.GetNodeAnalytics(coverageNodePK, 365)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	from, err := time.Parse(time.RFC3339, a.TimeRange.DataFrom)
	if err != nil || time.Since(from) > 73*time.Hour {
		t.Errorf("dataFrom = %q, want the 72h retention edge", a.TimeRange.DataFrom)
	}
	if a.ComputedStats.TotalPackets != 3*24 {
		t.Errorf("totalPackets = %d, want %d (the re-heard old one left out)", a.ComputedStats.TotalPackets, 3*24)
	}
	for _, b := range a.ActivityTimeline {
		if b.Bucket != nil && *b.Bucket < a.TimeRange.DataFrom[:13] {
			t.Errorf("bucket %s lies before dataFrom %s", *b.Bucket, a.TimeRange.DataFrom)
		}
	}
}

// Memory-pressure eviction cuts into the retention window; the edge it left
// is the start of the coverage.
func TestNodeAnalyticsCoverageAfterMemoryEviction(t *testing.T) {
	ps := setupCoverageStore(t, 3*24)
	edge := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	ps.mu.Lock()
	ps.retentionHours = 3 * 24
	ps.evictedThrough = edge.Format(time.RFC3339)
	ps.mu.Unlock()

	a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if a.TimeRange.DataFrom != edge.Format(time.RFC3339) {
		t.Errorf("dataFrom = %q, want the eviction edge %q", a.TimeRange.DataFrom, edge.Format(time.RFC3339))
	}
}

func TestEvictionRecordsEvictedThrough(t *testing.T) {
	ps := setupCoverageStore(t, 48)
	ps.mu.Lock()
	ps.retentionHours = 24
	n := ps.EvictStale()
	edge := ps.evictedThrough
	remaining := ps.packets[0].FirstSeen
	ps.mu.Unlock()
	if n == 0 {
		t.Fatal("nothing evicted")
	}
	if edge == "" || edge >= remaining {
		t.Errorf("evictedThrough = %q, want just before the oldest remaining %q", edge, remaining)
	}
}

// Retention equal to the requested range is full coverage, not partial
// (staging: retentionHours 168 marked every 7d request partial). An edge a
// few seconds after the start stays within coverageSlack.
func TestNodeAnalyticsCoverageRetentionEqualsRange(t *testing.T) {
	ps := setupCoverageStore(t, 10*24)
	ps.mu.Lock()
	// The retention edge lies 30 s after the 7d start: inside the slack.
	ps.retentionHours = 7*24 - 30.0/3600
	ps.oldestLoaded = time.Now().UTC().Add(-7*24*time.Hour - time.Minute).Format(time.RFC3339)
	ps.mu.Unlock()
	for i := 0; i < 3; i++ {
		a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
		if err != nil || a == nil {
			t.Fatal(err)
		}
		if a.TimeRange.Partial {
			t.Fatalf("run %d: 7d with 168h retention marked partial (dataFrom %q, from %q)", i, a.TimeRange.DataFrom, a.TimeRange.From)
		}
	}
	if a, _ := ps.GetNodeAnalytics(coverageNodePK, 30); a == nil || !a.TimeRange.Partial {
		t.Error("30d with 168h retention must be partial")
	}
}

// The coverage notice names why the data starts late (#748 review): a store
// holding everything from the oldest packet in the database is not a memory
// limit.
func TestNodeAnalyticsCoverageReason(t *testing.T) {
	cases := []struct {
		name  string
		setup func(ps *PacketStore)
		want  string
	}{
		{"database younger than the range", func(ps *PacketStore) {}, "database"},
		{"retention window", func(ps *PacketStore) { ps.retentionHours = 2 * 24 }, "memory"},
		{"memory eviction", func(ps *PacketStore) {
			ps.evictedThrough = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
		}, "memory"},
		{"background load still running", func(ps *PacketStore) {
			ps.hotStartupHours = 24
			ps.oldestLoaded = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
		}, "loading"},
	}
	for _, c := range cases {
		ps := setupCoverageStore(t, 3*24)
		ps.mu.Lock()
		c.setup(ps)
		ps.mu.Unlock()
		a, err := ps.GetNodeAnalytics(coverageNodePK, 30)
		if err != nil || a == nil {
			t.Fatal(err)
		}
		if !a.TimeRange.Partial || a.TimeRange.DataFromReason != c.want {
			t.Errorf("%s: partial=%v reason=%q, want %q", c.name, a.TimeRange.Partial, a.TimeRange.DataFromReason, c.want)
		}
	}
	ps := setupCoverageStore(t, 10*24)
	if a, _ := ps.GetNodeAnalytics(coverageNodePK, 7); a == nil || a.TimeRange.DataFromReason != "" {
		t.Errorf("full coverage must carry no reason, got %+v", a.TimeRange)
	}
}

// Under minCoveredHours the rates are extrapolation (5 packets in 6 min read
// as 1200/day), so they are left out rather than reported (#748 review).
func TestNodeAnalyticsCoverageTooShortForRates(t *testing.T) {
	ps := setupCoverageStore(t, 6)
	a, err := ps.GetNodeAnalytics(coverageNodePK, 7)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if !a.TimeRange.Partial {
		t.Fatal("6 h of data under a 7d range must be partial")
	}
	if a.ComputedStats.AvailabilityPct != nil || a.ComputedStats.AvgPacketsPerDay != nil {
		t.Errorf("rates over 6 h: availability=%v packets/day=%v, want both null",
			rate(a.ComputedStats.AvailabilityPct), rate(a.ComputedStats.AvgPacketsPerDay))
	}
	if a.ComputedStats.TotalPackets != 6 {
		t.Errorf("totalPackets = %d, want 6 (counts stay)", a.ComputedStats.TotalPackets)
	}

	ps = setupCoverageStore(t, 25)
	if a, _ := ps.GetNodeAnalytics(coverageNodePK, 7); a == nil || a.ComputedStats.AvailabilityPct == nil {
		t.Error("25 h of data must yield rates")
	}
}

// A load cut short by the memory cap leaves a gap between the last loaded
// row and what the poller picks up, so the store is only complete from the
// end of the load; packets[0] or the retention edge would overstate it.
func TestLoadChunkedMemoryCapSetsCoverageEdge(t *testing.T) {
	store := openReverseTimeStore(t, 1100)
	defer store.db.conn.Close()
	store.maxMemoryMB = 1 // floors to 1000 packets
	store.retentionHours = 30 * 24
	before := time.Now().UTC().Truncate(time.Second)
	if err := store.LoadChunked(500); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) >= 1100 {
		t.Fatalf("cap did not apply: %d packets loaded", len(store.packets))
	}
	store.mu.RLock()
	start, reason := store.coverageStart(time.Now().UTC())
	store.mu.RUnlock()
	if start.Before(before) || reason != "memory" {
		t.Errorf("capped load: coverage start %v (%q), want at or after %v (\"memory\")", start, reason, before)
	}
}

func TestLoadChunkedUncappedCoverageIsDatabase(t *testing.T) {
	store := openReverseTimeStore(t, 50)
	defer store.db.conn.Close()
	if err := store.LoadChunked(20); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	start, reason := store.coverageStart(time.Now().UTC())
	oldest := store.packets[0].FirstSeen
	store.mu.RUnlock()
	if start.Format(time.RFC3339) != oldest || reason != "database" {
		t.Errorf("uncapped load: coverage start %v (%q), want %s (\"database\")", start, reason, oldest)
	}
}

// Eviction removes a head prefix of s.packets; the edge is the newest
// FirstSeen in it, not whatever sits last should the slice be out of order.
func TestEvictionRecordsNewestEvicted(t *testing.T) {
	ps := setupCoverageStore(t, 48)
	ps.mu.Lock()
	ps.retentionHours = 24
	// Index 23 is the last packet past the 24 h retention (h=24); make it
	// older than its neighbour so the slice is out of order there.
	newest := ps.packets[22].FirstSeen
	ps.packets[23].FirstSeen = time.Now().UTC().Add(-100 * time.Hour).Format(time.RFC3339)
	ps.EvictStale()
	edge := ps.evictedThrough
	ps.mu.Unlock()
	if edge != newest {
		t.Errorf("evictedThrough = %q, want the newest evicted %q", edge, newest)
	}
}
