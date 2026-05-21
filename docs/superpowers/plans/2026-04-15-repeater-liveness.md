# Repeater Liveness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Distinguish repeaters that are actively relaying traffic from those that are alive but idle, using a precomputed O(log n) sorted-timestamp index.

**Architecture:** Add `relayTimes map[string][]int64` to `PacketStore`, maintained in lockstep with `byPathHop`. Relay counts for 1h/24h windows are computed via binary search at query time and injected into the existing `/api/nodes/bulk-health` and `/api/nodes/{pubkey}/health` responses. Frontend extends `getNodeStatus` to return a third state (`'relaying'`) for repeaters with recent relay activity.

**Tech Stack:** Go (backend), vanilla JS (frontend), Node.js (frontend tests), SQLite (no schema changes needed)

---

## File Map

| File | Action | What changes |
|---|---|---|
| `cmd/server/store.go` | Modify | Add `relayTimes` field; add `addTxToRelayTimeIndex`, `removeFromRelayTimeIndex`, `relayMetrics` functions; wire into `buildPathHopIndex`, `addTxToPathHopIndex`, `removeTxFromPathHopIndex`; enrich `GetBulkHealth` and `GetNodeHealth` |
| `cmd/server/relay_liveness_test.go` | Create | Go unit + integration tests for all relay index functions and API enrichment |
| `public/roles.js` | Modify | Extend `getNodeStatus` with optional third param `relayCount24h`; add `'relaying'` return value |
| `public/nodes.js` | Modify | Update `getStatusInfo`, `getStatusTooltip`, node list row, node detail pane stats table |
| `public/style.css` | Modify | Add `.last-seen-idle` CSS class |
| `test-repeater-liveness.js` | Create | Frontend unit tests for the three-state logic |

---

## Task 1: Backend — `relayTimes` index field and pure functions

**Files:**
- Modify: `cmd/server/store.go`
- Create: `cmd/server/relay_liveness_test.go`

- [ ] **Step 1: Write failing tests for the pure index functions**

Create `cmd/server/relay_liveness_test.go`:

```go
package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func makeRp(s string) *string { return &s }

func TestAddTxToRelayTimeIndex_SingleNode(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().Add(-30 * time.Minute).UTC()
	tx := &StoreTx{
		FirstSeen:    ts.Format(time.RFC3339),
		ResolvedPath: []*string{makeRp(pk)},
	}
	addTxToRelayTimeIndex(idx, tx)
	if len(idx[pk]) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(idx[pk]))
	}
	wantMs := ts.UnixMilli()
	// RFC3339 has second precision, so allow ±1000ms
	if diff := idx[pk][0] - wantMs; diff < -1000 || diff > 1000 {
		t.Errorf("timestamp mismatch: got %d, want ~%d", idx[pk][0], wantMs)
	}
}

func TestAddTxToRelayTimeIndex_SortedOrder(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	t1 := time.Now().Add(-2 * time.Hour).UTC()
	t2 := time.Now().Add(-30 * time.Minute).UTC()

	// Insert newer first, expect sorted ascending
	tx2 := &StoreTx{FirstSeen: t2.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pk)}}
	tx1 := &StoreTx{FirstSeen: t1.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pk)}}
	addTxToRelayTimeIndex(idx, tx2)
	addTxToRelayTimeIndex(idx, tx1)

	if len(idx[pk]) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(idx[pk]))
	}
	if !sort.SliceIsSorted(idx[pk], func(i, j int) bool { return idx[pk][i] < idx[pk][j] }) {
		t.Error("relayTimes slice not sorted ascending")
	}
}

func TestAddTxToRelayTimeIndex_MultipleNodes(t *testing.T) {
	idx := make(map[string][]int64)
	pk1 := "aabbccdd11223344"
	pk2 := "eeff001122334455"
	ts := time.Now().Add(-10 * time.Minute).UTC()
	tx := &StoreTx{
		FirstSeen:    ts.Format(time.RFC3339),
		ResolvedPath: []*string{makeRp(pk1), makeRp(pk2)},
	}
	addTxToRelayTimeIndex(idx, tx)
	if len(idx[pk1]) != 1 {
		t.Errorf("pk1: expected 1 entry, got %d", len(idx[pk1]))
	}
	if len(idx[pk2]) != 1 {
		t.Errorf("pk2: expected 1 entry, got %d", len(idx[pk2]))
	}
}

func TestAddTxToRelayTimeIndex_NilResolvedPath(t *testing.T) {
	idx := make(map[string][]int64)
	tx := &StoreTx{FirstSeen: time.Now().UTC().Format(time.RFC3339), ResolvedPath: nil}
	addTxToRelayTimeIndex(idx, tx) // must not panic
	if len(idx) != 0 {
		t.Error("expected empty index for nil ResolvedPath")
	}
}

func TestAddTxToRelayTimeIndex_DuplicatePubkeyInPath(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().UTC()
	tx := &StoreTx{
		FirstSeen:    ts.Format(time.RFC3339),
		ResolvedPath: []*string{makeRp(pk), makeRp(pk)}, // same pubkey twice
	}
	addTxToRelayTimeIndex(idx, tx)
	if len(idx[pk]) != 1 {
		t.Errorf("duplicate pubkey should produce only 1 entry, got %d", len(idx[pk]))
	}
}

func TestRemoveFromRelayTimeIndex_RemovesEntry(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().Add(-1 * time.Hour).UTC()
	tx := &StoreTx{FirstSeen: ts.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pk)}}

	addTxToRelayTimeIndex(idx, tx)
	if len(idx[pk]) != 1 {
		t.Fatal("setup: expected 1 entry")
	}
	removeFromRelayTimeIndex(idx, tx)
	if _, ok := idx[pk]; ok {
		t.Error("expected key deleted after last entry removed")
	}
}

func TestRemoveFromRelayTimeIndex_PartialRemove(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	t1 := time.Now().Add(-2 * time.Hour).UTC()
	t2 := time.Now().Add(-30 * time.Minute).UTC()
	tx1 := &StoreTx{FirstSeen: t1.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pk)}}
	tx2 := &StoreTx{FirstSeen: t2.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pk)}}

	addTxToRelayTimeIndex(idx, tx1)
	addTxToRelayTimeIndex(idx, tx2)
	removeFromRelayTimeIndex(idx, tx1)

	if len(idx[pk]) != 1 {
		t.Errorf("expected 1 entry after removing one, got %d", len(idx[pk]))
	}
}

func TestRelayMetrics_Counts(t *testing.T) {
	now := time.Now().UnixMilli()
	times := []int64{
		now - 90*60*1000, // 90 min ago — inside 24h, outside 1h
		now - 30*60*1000, // 30 min ago — inside both
		now - 10*60*1000, // 10 min ago — inside both
	}
	c1h, c24h, lastRelayed := relayMetrics(times, now)
	if c1h != 2 {
		t.Errorf("relay_count_1h: expected 2, got %d", c1h)
	}
	if c24h != 3 {
		t.Errorf("relay_count_24h: expected 3, got %d", c24h)
	}
	if lastRelayed == "" {
		t.Error("last_relayed should not be empty")
	}
}

func TestRelayMetrics_EmptySlice(t *testing.T) {
	c1h, c24h, lastRelayed := relayMetrics(nil, time.Now().UnixMilli())
	if c1h != 0 || c24h != 0 || lastRelayed != "" {
		t.Errorf("empty slice: expected zeros and empty string, got %d %d %q", c1h, c24h, lastRelayed)
	}
}

func TestRelayMetrics_AllOutsideWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	times := []int64{now - 30*24*60*60*1000} // 30 days ago
	c1h, c24h, _ := relayMetrics(times, now)
	if c1h != 0 || c24h != 0 {
		t.Errorf("expected 0/0 for old entry, got %d/%d", c1h, c24h)
	}
}

func TestAddTxToRelayTimeIndex_LowercasesKey(t *testing.T) {
	idx := make(map[string][]int64)
	pkUpper := "AABBCCDD11223344"
	pkLower := strings.ToLower(pkUpper)
	ts := time.Now().UTC()
	tx := &StoreTx{FirstSeen: ts.Format(time.RFC3339), ResolvedPath: []*string{makeRp(pkUpper)}}
	addTxToRelayTimeIndex(idx, tx)
	if len(idx[pkLower]) != 1 {
		t.Errorf("expected index keyed by lowercase, found %d entries at lowercase key", len(idx[pkLower]))
	}
	if len(idx[pkUpper]) != 0 {
		t.Errorf("expected no entry at uppercase key")
	}
}
```

- [ ] **Step 2: Run tests to confirm they all fail**

```bash
cd cmd/server && go test -run "TestAddTxToRelayTimeIndex|TestRemoveFromRelayTimeIndex|TestRelayMetrics" ./... 2>&1
```

Expected: compile error — `addTxToRelayTimeIndex`, `removeFromRelayTimeIndex`, `relayMetrics` undefined.

- [ ] **Step 3: Add `relayTimes` field to `PacketStore` struct**

In `cmd/server/store.go`, find the struct field block around line 134 that has `byPathHop`:

```go
byPathHop     map[string][]*StoreTx      // lowercase hop/pubkey → transmissions with that hop in path
```

Add after it:

```go
relayTimes    map[string][]int64         // lowercase pubkey → sorted unix-millis of relay events (full pubkeys only)
```

- [ ] **Step 4: Initialize `relayTimes` in `newPacketStore`**

Find the block around line 289 that initializes `byPathHop`:

```go
byPathHop:     make(map[string][]*StoreTx),
```

Add after it:

```go
relayTimes:    make(map[string][]int64),
```

- [ ] **Step 5: Implement `addTxToRelayTimeIndex`, `removeFromRelayTimeIndex`, `relayMetrics`**

Add these three functions to `cmd/server/store.go` directly after the `addTxToPathHopIndex` function (around line 2452):

```go
// addTxToRelayTimeIndex records the relay timestamp for each full pubkey in
// tx.ResolvedPath. Maintains sorted ascending order for O(log n) window queries.
// Must be called with s.mu held (or during build before store is live).
func addTxToRelayTimeIndex(idx map[string][]int64, tx *StoreTx) {
	if len(tx.ResolvedPath) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, tx.FirstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(tx.ResolvedPath))
	for _, rp := range tx.ResolvedPath {
		if rp == nil {
			continue
		}
		pk := strings.ToLower(*rp)
		if seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		slice = append(slice, 0)
		copy(slice[i+1:], slice[i:])
		slice[i] = millis
		idx[pk] = slice
	}
}

// removeFromRelayTimeIndex removes the relay timestamp for each full pubkey in
// tx.ResolvedPath. Inverse of addTxToRelayTimeIndex.
func removeFromRelayTimeIndex(idx map[string][]int64, tx *StoreTx) {
	if len(tx.ResolvedPath) == 0 {
		return
	}
	ms, err := time.Parse(time.RFC3339, tx.FirstSeen)
	if err != nil {
		return
	}
	millis := ms.UnixMilli()
	seen := make(map[string]bool, len(tx.ResolvedPath))
	for _, rp := range tx.ResolvedPath {
		if rp == nil {
			continue
		}
		pk := strings.ToLower(*rp)
		if seen[pk] {
			continue
		}
		seen[pk] = true
		slice := idx[pk]
		i := sort.Search(len(slice), func(j int) bool { return slice[j] >= millis })
		if i < len(slice) && slice[i] == millis {
			idx[pk] = append(slice[:i], slice[i+1:]...)
			if len(idx[pk]) == 0 {
				delete(idx, pk)
			}
		}
	}
}

// relayMetrics computes relay_count_1h, relay_count_24h, and last_relayed from a
// sorted unix-millis slice. now is time.Now().UnixMilli(). O(log n).
func relayMetrics(times []int64, now int64) (count1h, count24h int, lastRelayed string) {
	if len(times) == 0 {
		return 0, 0, ""
	}
	i1h := sort.Search(len(times), func(i int) bool { return times[i] >= now-3600000 })
	i24h := sort.Search(len(times), func(i int) bool { return times[i] >= now-86400000 })
	count1h = len(times) - i1h
	count24h = len(times) - i24h
	lastRelayed = time.UnixMilli(times[len(times)-1]).UTC().Format(time.RFC3339)
	return
}
```

- [ ] **Step 6: Run tests — expect pass**

```bash
cd cmd/server && go test -run "TestAddTxToRelayTimeIndex|TestRemoveFromRelayTimeIndex|TestRelayMetrics" ./... -v 2>&1
```

Expected: all 10 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/store.go cmd/server/relay_liveness_test.go
git commit -m "feat(store): add relayTimes index and relay metrics functions (#662)"
```

---

## Task 2: Backend — wire `relayTimes` into ingest/evict/build paths

**Files:**
- Modify: `cmd/server/store.go`

- [ ] **Step 1: Wire into `addTxToPathHopIndex`**

Find the function `addTxToPathHopIndex` (around line 2432). It ends with the closing `}`. Add a call to `addTxToRelayTimeIndex` — but `addTxToPathHopIndex` takes `idx map[string][]*StoreTx`, not the store itself. The relay index needs to be passed separately.

The three call sites of `addTxToPathHopIndex` all hold `s.mu`. Change each call site instead of the function signature — add a paired call right after each `addTxToPathHopIndex`:

**Site 1** — ingest (around line 1485):
```go
addTxToPathHopIndex(s.byPathHop, tx)
addTxToRelayTimeIndex(s.relayTimes, tx)
```

**Site 2** — path resolution update (around line 1871):
```go
addTxToPathHopIndex(s.byPathHop, tx)
addTxToRelayTimeIndex(s.relayTimes, tx)
```

- [ ] **Step 2: Wire into `removeTxFromPathHopIndex` call sites**

Find `removeTxFromPathHopIndex(s.byPathHop, tx)` (two call sites — eviction around line 2793 and path-reindex around line 1862). Add paired remove call after each:

**Site 1** — eviction (around line 2793):
```go
removeTxFromPathHopIndex(s.byPathHop, tx)
removeFromRelayTimeIndex(s.relayTimes, tx)
```

**Site 2** — path reindex (around line 1862):
```go
removeTxFromPathHopIndex(s.byPathHop, tx)
removeFromRelayTimeIndex(s.relayTimes, tx)
```

- [ ] **Step 3: Wire into `buildPathHopIndex`**

Find `buildPathHopIndex` (around line 2422). It currently does:

```go
func (s *PacketStore) buildPathHopIndex() {
	s.byPathHop = make(map[string][]*StoreTx, 4096)
	for _, tx := range s.packets {
		addTxToPathHopIndex(s.byPathHop, tx)
	}
	log.Printf("[store] Built path-hop index: %d unique keys", len(s.byPathHop))
}
```

Replace with:

```go
func (s *PacketStore) buildPathHopIndex() {
	s.byPathHop = make(map[string][]*StoreTx, 4096)
	s.relayTimes = make(map[string][]int64, 4096)
	for _, tx := range s.packets {
		addTxToPathHopIndex(s.byPathHop, tx)
		addTxToRelayTimeIndex(s.relayTimes, tx)
	}
	log.Printf("[store] Built path-hop index: %d unique keys, %d relay-time keys", len(s.byPathHop), len(s.relayTimes))
}
```

- [ ] **Step 4: Write integration test for wired ingest**

Add to `cmd/server/relay_liveness_test.go`:

```go
func TestRelayTimesWiredIntoIngest(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.store.mu.RLock()
	hopKeys := len(srv.store.byPathHop)
	relayKeys := len(srv.store.relayTimes)
	srv.store.mu.RUnlock()

	if hopKeys == 0 {
		t.Skip("no path-hop data in test store — skipping relay wiring test")
	}
	// relayTimes will only be populated if test packets have ResolvedPath entries.
	// At minimum it must not panic and must be initialised.
	if srv.store.relayTimes == nil {
		t.Fatal("relayTimes map is nil after load")
	}
	t.Logf("byPathHop keys: %d, relayTimes keys: %d", hopKeys, relayKeys)
}
```

- [ ] **Step 5: Run all relay tests**

```bash
cd cmd/server && go test -run "TestAddTxToRelayTimeIndex|TestRemoveFromRelayTimeIndex|TestRelayMetrics|TestRelayTimesWired" ./... -v 2>&1
```

Expected: all pass.

- [ ] **Step 6: Run full backend test suite**

```bash
cd cmd/server && go test ./... 2>&1
```

Expected: no regressions.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/store.go cmd/server/relay_liveness_test.go
git commit -m "feat(store): wire relayTimes into ingest, evict, and build paths (#662)"
```

---

## Task 3: Backend API — enrich bulk-health and node-health with relay metrics

**Files:**
- Modify: `cmd/server/store.go`

- [ ] **Step 1: Write failing tests for relay fields in API responses**

Add to `cmd/server/relay_liveness_test.go`:

```go
func TestGetBulkHealthRepeaterRelayFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Insert a synthetic repeater node into the DB if none exists
	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('relay662test0001', 'TestRepeater662', 'repeater', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	// Inject a relay timestamp within the last hour
	pk := "relay662test0001"
	now := time.Now().UnixMilli()
	recentMs := now - 10*60*1000 // 10 min ago
	srv.store.mu.Lock()
	srv.store.relayTimes[pk] = []int64{recentMs}
	srv.store.mu.Unlock()

	results := srv.store.GetBulkHealth(200, "")

	var found map[string]interface{}
	for _, r := range results {
		if r["public_key"] == pk {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("test repeater not found in GetBulkHealth results")
	}

	stats, ok := found["stats"].(map[string]interface{})
	if !ok {
		t.Fatal("missing stats map in result")
	}

	if v, ok := stats["relay_count_1h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_1h: expected 1, got %v", stats["relay_count_1h"])
	}
	if v, ok := stats["relay_count_24h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_24h: expected 1, got %v", stats["relay_count_24h"])
	}
	if _, ok := stats["last_relayed"].(string); !ok {
		t.Errorf("last_relayed: expected string, got %T", stats["last_relayed"])
	}
}

func TestGetBulkHealthCompanionNoRelayFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('comp662test0001', 'TestCompanion662', 'companion', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	// Give the companion a relay entry (should be ignored by role gate)
	pk := "comp662test0001"
	srv.store.mu.Lock()
	srv.store.relayTimes[pk] = []int64{time.Now().UnixMilli() - 5*60*1000}
	srv.store.mu.Unlock()

	results := srv.store.GetBulkHealth(200, "")
	for _, r := range results {
		if r["public_key"] == pk {
			stats, _ := r["stats"].(map[string]interface{})
			if _, present := stats["relay_count_24h"]; present {
				t.Error("relay_count_24h should be absent for companion nodes")
			}
			return
		}
	}
	t.Fatal("test companion not found in GetBulkHealth results")
}

func TestGetBulkHealthRepeaterNoRelayActivity(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('relay662idle001', 'IdleRepeater662', 'repeater', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	// No entry in relayTimes for this node
	results := srv.store.GetBulkHealth(200, "")
	for _, r := range results {
		if r["public_key"] == "relay662idle001" {
			stats, _ := r["stats"].(map[string]interface{})
			if v, ok := stats["relay_count_24h"].(int); !ok || v != 0 {
				t.Errorf("relay_count_24h: expected 0, got %v", stats["relay_count_24h"])
			}
			if _, present := stats["last_relayed"]; present {
				t.Error("last_relayed should be absent when no relay activity")
			}
			return
		}
	}
	t.Fatal("idle repeater not found in results")
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd cmd/server && go test -run "TestGetBulkHealth" ./... 2>&1
```

Expected: FAIL — relay fields missing from stats map.

- [ ] **Step 3: Enrich `GetBulkHealth` with relay metrics**

In `GetBulkHealth` (around line 5945), find where the `stats` map is built (around line 6099–6106):

```go
results = append(results, map[string]interface{}{
    "public_key": n.pk,
    "name":       nilIfEmpty(n.name),
    "role":       nilIfEmpty(n.role),
    "lat":        n.lat,
    "lon":        n.lon,
    "stats": map[string]interface{}{
        "totalTransmissions": len(packets),
        "totalObservations":  totalObservations,
        "totalPackets":       len(packets),
        "packetsToday":       packetsToday,
        "avgSnr":             avgSnr,
        "lastHeard":          lhVal,
    },
    "observers": observerRows,
})
```

Replace the `stats` map construction with:

```go
statsMap := map[string]interface{}{
    "totalTransmissions": len(packets),
    "totalObservations":  totalObservations,
    "totalPackets":       len(packets),
    "packetsToday":       packetsToday,
    "avgSnr":             avgSnr,
    "lastHeard":          lhVal,
}
if strings.ToLower(n.role) == "repeater" {
    c1h, c24h, lastRel := relayMetrics(s.relayTimes[n.pk], time.Now().UnixMilli())
    statsMap["relay_count_1h"] = c1h
    statsMap["relay_count_24h"] = c24h
    if lastRel != "" {
        statsMap["last_relayed"] = lastRel
    }
}
results = append(results, map[string]interface{}{
    "public_key": n.pk,
    "name":       nilIfEmpty(n.name),
    "role":       nilIfEmpty(n.role),
    "lat":        n.lat,
    "lon":        n.lon,
    "stats":      statsMap,
    "observers":  observerRows,
})
```

- [ ] **Step 4: Enrich `GetNodeHealth` with relay metrics**

In `GetNodeHealth` (around line 6117), find where the final result map is assembled (look for `"lastHeard"` key in the stats sub-map — it's around line 6230–6260 depending on exact file state). The stats map will have a shape like:

```go
"stats": map[string]interface{}{
    "totalTransmissions": ...,
    "totalObservations":  ...,
    "totalPackets":       ...,
    "packetsToday":       ...,
    "avgSnr":             ...,
    "lastHeard":          lastHeardVal,
    "avgHops":            ...,
},
```

After assembling that `stats` map (name it `nodeStats` if it isn't already named), add before the final `return`:

```go
role := ""
if r, ok := node["role"].(string); ok {
    role = strings.ToLower(r)
}
if role == "repeater" {
    lowerPK := strings.ToLower(pubkey)
    c1h, c24h, lastRel := relayMetrics(s.relayTimes[lowerPK], time.Now().UnixMilli())
    nodeStats["relay_count_1h"] = c1h
    nodeStats["relay_count_24h"] = c24h
    if lastRel != "" {
        nodeStats["last_relayed"] = lastRel
    }
}
```

> Note: `GetNodeHealth` releases `s.mu` via `defer s.mu.RUnlock()` at entry — relay metrics are computed inside the lock, which is correct.

- [ ] **Step 5: Run all relay tests**

```bash
cd cmd/server && go test -run "TestGetBulkHealth|TestAddTxToRelayTimeIndex|TestRemoveFromRelayTimeIndex|TestRelayMetrics|TestRelayTimesWired" ./... -v 2>&1
```

Expected: all pass.

- [ ] **Step 6: Run full backend test suite**

```bash
cd cmd/server && go test ./... 2>&1
```

Expected: no regressions.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/store.go cmd/server/relay_liveness_test.go
git commit -m "feat(api): add relay_count_1h/24h/last_relayed to node health responses (#662)"
```

---

## Task 4: Frontend — extend `getNodeStatus` to three-state (TDD)

**Files:**
- Modify: `public/roles.js`
- Create: `test-repeater-liveness.js`

- [ ] **Step 1: Write the failing frontend test file**

Create `test-repeater-liveness.js` at the repo root:

```js
'use strict';
const vm = require('vm');
const fs = require('fs');

// Minimal browser environment
const ctx = {
  window: {},
  console,
  fetch: () => Promise.resolve({ json: () => Promise.resolve({}) }),
  Date,
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync('public/roles.js', 'utf8'), ctx);

const { getNodeStatus, HEALTH_THRESHOLDS } = ctx.window;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ok:', name); }
  catch (e) { fail++; console.log('FAIL:', name, '—', e.message); }
}
function assert(cond, msg) { if (!cond) throw new Error(msg || 'assertion failed'); }

const now = Date.now();
const recentMs = now - 1000;                                     // 1 second ago — always active
const staleMs  = now - (HEALTH_THRESHOLDS.infraSilentMs + 1);   // just past silent threshold

// --- Repeater three-state ---
test('repeater + recent + relay > 0 → relaying',
  () => assert(getNodeStatus('repeater', recentMs, 5) === 'relaying'));

test('repeater + recent + relay == 0 → active (idle)',
  () => assert(getNodeStatus('repeater', recentMs, 0) === 'active'));

test('repeater + stale + relay > 0 → stale (stale beats relay)',
  () => assert(getNodeStatus('repeater', staleMs, 99) === 'stale'));

test('repeater + stale + relay == 0 → stale',
  () => assert(getNodeStatus('repeater', staleMs, 0) === 'stale'));

// --- Non-repeater roles unaffected ---
test('companion + recent + relay 0 → active',
  () => assert(getNodeStatus('companion', recentMs, 0) === 'active'));

test('companion + recent + relay > 0 → active (relay ignored)',
  () => assert(getNodeStatus('companion', recentMs, 99) === 'active'));

test('room + recent + relay 0 → active',
  () => assert(getNodeStatus('room', recentMs, 0) === 'active'));

test('sensor + recent + relay 0 → active',
  () => assert(getNodeStatus('sensor', recentMs, 0) === 'active'));

// --- Backward compatibility: omitting third arg ---
test('getNodeStatus(repeater, recent) with no relay arg → active (not relaying)',
  () => assert(getNodeStatus('repeater', recentMs) === 'active'));

test('getNodeStatus(companion, recent) with no relay arg → active',
  () => assert(getNodeStatus('companion', recentMs) === 'active'));

console.log(`\n${pass} passed, ${fail} failed`);
if (fail > 0) process.exit(1);
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
node test-repeater-liveness.js 2>&1
```

Expected: FAIL on `repeater + recent + relay > 0 → relaying` (returns `'active'` instead of `'relaying'`).

- [ ] **Step 3: Extend `getNodeStatus` in `public/roles.js`**

Find the existing function (around line 88):

```js
window.getNodeStatus = function (role, lastSeenMs) {
    var isInfra = role === 'repeater' || role === 'room';
    var staleMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    var age = typeof lastSeenMs === 'number' ? (Date.now() - lastSeenMs) : Infinity;
    return age < staleMs ? 'active' : 'stale';
};
```

Replace with:

```js
window.getNodeStatus = function (role, lastSeenMs, relayCount24h) {
    var isInfra = role === 'repeater' || role === 'room';
    var staleMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    var age = typeof lastSeenMs === 'number' ? (Date.now() - lastSeenMs) : Infinity;
    if (age >= staleMs) return 'stale';
    if (role === 'repeater') {
      return (typeof relayCount24h === 'number' && relayCount24h > 0) ? 'relaying' : 'active';
    }
    return 'active';
};
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
node test-repeater-liveness.js 2>&1
```

Expected: all 10 tests pass.

- [ ] **Step 5: Commit**

```bash
git add public/roles.js test-repeater-liveness.js
git commit -m "feat(frontend): extend getNodeStatus to three-state for repeaters (#662)"
```

---

## Task 5: Frontend — render three-state labels, CSS, and node detail pane

**Files:**
- Modify: `public/nodes.js`
- Modify: `public/style.css`

- [ ] **Step 1: Add `.last-seen-idle` CSS class to `public/style.css`**

Find these two lines (around line 1590–1591):

```css
.last-seen-active { color: var(--status-green); }
.last-seen-stale { color: var(--text-muted); }
```

Add after them:

```css
.last-seen-idle   { color: var(--status-yellow); }
```

- [ ] **Step 2: Update `getStatusTooltip` in `public/nodes.js`**

Find the function (around line 113):

```js
function getStatusTooltip(role, status) {
    const isInfra = role === 'repeater' || role === 'room';
    const threshMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    const threshold = threshMs >= 3600000 ? Math.round(threshMs / 3600000) + 'h' : Math.round(threshMs / 60000) + 'm';
    if (status === 'active') {
      return 'Active \u2014 heard within the last ' + threshold + '.' + (isInfra ? ' Repeaters typically advertise every 12-24h.' : '');
    }
    if (role === 'companion') {
      return 'Stale \u2014 not heard for over ' + threshold + '. Companions only advertise when the user initiates \u2014 this may be normal.';
    }
    if (role === 'sensor') {
      return 'Stale \u2014 not heard for over ' + threshold + '. This sensor may be offline.';
    }
    return 'Stale \u2014 not heard for over ' + threshold + '. This ' + role + ' may be offline or out of range.';
}
```

Replace with:

```js
function getStatusTooltip(role, status) {
    const isInfra = role === 'repeater' || role === 'room';
    const threshMs = isInfra ? HEALTH_THRESHOLDS.infraSilentMs : HEALTH_THRESHOLDS.nodeSilentMs;
    const threshold = threshMs >= 3600000 ? Math.round(threshMs / 3600000) + 'h' : Math.round(threshMs / 60000) + 'm';
    if (status === 'relaying') {
      return 'Relaying \u2014 actively forwarding traffic within the last 24h.';
    }
    if (status === 'active') {
      if (role === 'repeater') {
        return 'Idle \u2014 alive (heard within ' + threshold + ') but no relay traffic observed in the last 24h. May be in a quiet area or have RF issues.';
      }
      return 'Active \u2014 heard within the last ' + threshold + '.' + (isInfra ? ' Repeaters typically advertise every 12-24h.' : '');
    }
    if (role === 'companion') {
      return 'Stale \u2014 not heard for over ' + threshold + '. Companions only advertise when the user initiates \u2014 this may be normal.';
    }
    if (role === 'sensor') {
      return 'Stale \u2014 not heard for over ' + threshold + '. This sensor may be offline.';
    }
    return 'Stale \u2014 not heard for over ' + threshold + '. This ' + role + ' may be offline or out of range.';
}
```

- [ ] **Step 3: Update `getStatusInfo` in `public/nodes.js`**

Find the function (around line 129):

```js
function getStatusInfo(n) {
    // Single source of truth for all status-related info
    const role = (n.role || '').toLowerCase();
    const roleColor = ROLE_COLORS[n.role] || '#6b7280';
    // Prefer last_heard (from in-memory packets) > _lastHeard (health API) > last_seen (DB)
    const lastHeardTime = n._lastHeard || n.last_heard || n.last_seen;
    const lastHeardMs = lastHeardTime ? new Date(lastHeardTime).getTime() : 0;
    const status = getNodeStatus(role, lastHeardMs);
    const statusTooltip = getStatusTooltip(role, status);
    const statusLabel = status === 'active' ? '🟢 Active' : '⚪ Stale';
    const statusAge = lastHeardMs ? (Date.now() - lastHeardMs) : Infinity;

    let explanation = '';
    if (status === 'active') {
      explanation = 'Last heard ' + (lastHeardTime ? renderNodeTimestampText(lastHeardTime) : 'unknown');
    } else {
      const ageDays = Math.floor(statusAge / 86400000);
      const ageHours = Math.floor(statusAge / 3600000);
      const ageStr = ageDays >= 1 ? ageDays + 'd' : ageHours + 'h';
      const isInfra = role === 'repeater' || role === 'room';
      const reason = isInfra
        ? 'repeaters typically advertise every 12-24h'
        : 'companions only advertise when user initiates, this may be normal';
      explanation = 'Not heard for ' + ageStr + ' — ' + reason;
    }

    return { status, statusLabel, statusTooltip, statusAge, explanation, roleColor, lastHeardMs, role };
}
```

Replace with:

```js
function getStatusInfo(n) {
    // Single source of truth for all status-related info
    const role = (n.role || '').toLowerCase();
    const roleColor = ROLE_COLORS[n.role] || '#6b7280';
    // Prefer last_heard (from in-memory packets) > _lastHeard (health API) > last_seen (DB)
    const lastHeardTime = n._lastHeard || n.last_heard || n.last_seen;
    const lastHeardMs = lastHeardTime ? new Date(lastHeardTime).getTime() : 0;
    const relayCount24h = (n.stats && typeof n.stats.relay_count_24h === 'number') ? n.stats.relay_count_24h : undefined;
    const relayCount1h  = (n.stats && typeof n.stats.relay_count_1h  === 'number') ? n.stats.relay_count_1h  : undefined;
    const lastRelayed   = n.stats && n.stats.last_relayed;
    const status = getNodeStatus(role, lastHeardMs, relayCount24h);
    const statusTooltip = getStatusTooltip(role, status);
    const statusAge = lastHeardMs ? (Date.now() - lastHeardMs) : Infinity;

    let statusLabel;
    if (role === 'repeater') {
      statusLabel = status === 'relaying' ? '🟢 Relaying' : status === 'active' ? '🟡 Idle' : '⚪ Stale';
    } else {
      statusLabel = status === 'active' ? '🟢 Active' : '⚪ Stale';
    }

    let explanation = '';
    if (status === 'relaying') {
      explanation = 'Relayed ' + relayCount24h + ' packet' + (relayCount24h === 1 ? '' : 's') + ' in last 24h'
        + (lastRelayed ? ', last ' + renderNodeTimestampText(lastRelayed) : '');
    } else if (status === 'active' && role === 'repeater') {
      explanation = 'Alive but no relay traffic observed in last 24h';
    } else if (status === 'active') {
      explanation = 'Last heard ' + (lastHeardTime ? renderNodeTimestampText(lastHeardTime) : 'unknown');
    } else {
      const ageDays = Math.floor(statusAge / 86400000);
      const ageHours = Math.floor(statusAge / 3600000);
      const ageStr = ageDays >= 1 ? ageDays + 'd' : ageHours + 'h';
      const isInfra = role === 'repeater' || role === 'room';
      const reason = isInfra
        ? 'repeaters typically advertise every 12-24h'
        : 'companions only advertise when user initiates, this may be normal';
      explanation = 'Not heard for ' + ageStr + ' — ' + reason;
    }

    return { status, statusLabel, statusTooltip, statusAge, explanation, roleColor, lastHeardMs, role, relayCount1h, relayCount24h, lastRelayed };
}
```

- [ ] **Step 4: Update the node list row CSS class mapping**

Find the node list rendering (around line 1030–1031):

```js
const status = getNodeStatus(n.role || 'companion', lastSeenTime ? new Date(lastSeenTime).getTime() : 0);
const lastSeenClass = status === 'active' ? 'last-seen-active' : 'last-seen-stale';
```

Replace with:

```js
const relayCount24h = (n.stats && typeof n.stats.relay_count_24h === 'number') ? n.stats.relay_count_24h : undefined;
const status = getNodeStatus((n.role || 'companion').toLowerCase(), lastSeenTime ? new Date(lastSeenTime).getTime() : 0, relayCount24h);
const lastSeenClass = status === 'relaying' ? 'last-seen-active' : status === 'active' ? ((n.role || '').toLowerCase() === 'repeater' ? 'last-seen-idle' : 'last-seen-active') : 'last-seen-stale';
```

- [ ] **Step 5: Add relay stats rows to the node detail pane**

Find the stats table in the detail pane (around line 490–494):

```js
${stats.avgHops ? `<tr><td>Avg Hops</td><td>${stats.avgHops}</td></tr>` : ''}
${hasLoc ? `<tr><td>Location</td><td>...` : ''}
```

Add relay rows after `avgHops`:

```js
${stats.avgHops ? `<tr><td>Avg Hops</td><td>${stats.avgHops}</td></tr>` : ''}
${si.role === 'repeater' ? `
  <tr><td>Relay (1h)</td><td>${typeof si.relayCount1h === 'number' ? si.relayCount1h + ' packet' + (si.relayCount1h === 1 ? '' : 's') : '—'}</td></tr>
  <tr><td>Relay (24h)</td><td>${typeof si.relayCount24h === 'number' ? si.relayCount24h + ' packet' + (si.relayCount24h === 1 ? '' : 's') : '—'}</td></tr>
  ${si.lastRelayed ? `<tr><td>Last Relayed</td><td>${renderNodeTimestampHtml(si.lastRelayed)}</td></tr>` : ''}
` : ''}
${hasLoc ? `<tr><td>Location</td><td>${Number(n.lat).toFixed(5)}, ${Number(n.lon).toFixed(5)}</td></tr>` : ''}
```

> Note: `si` is the return value of `getStatusInfo(n)` — already in scope in the detail render function. Verify the local variable name by checking the block around line 453.

- [ ] **Step 6: Find and check the second detail panel (around line 1075)**

There is a second node detail render path around line 1072. Apply the same relay rows addition there too — find the equivalent `avgHops` row and add the same relay block after it.

- [ ] **Step 7: Run frontend tests**

```bash
node test-repeater-liveness.js && node test-packet-filter.js && node test-frontend-helpers.js && node test-live.js && node test-packets.js 2>&1
```

Expected: all pass.

- [ ] **Step 8: Run backend tests**

```bash
cd cmd/server && go test ./... 2>&1
```

Expected: no regressions.

- [ ] **Step 9: Commit**

```bash
git add public/nodes.js public/style.css
git commit -m "feat(ui): three-state repeater liveness indicator and relay stats in detail pane (#662)"
```

---

## Done

All four tasks complete. Verify end-to-end:

```bash
# Backend
cd cmd/server && go test ./... -v 2>&1 | tail -20

# Frontend
node test-repeater-liveness.js && node test-packet-filter.js && node test-frontend-helpers.js && node test-live.js && node test-packets.js
```

Then start the server and open the Nodes page. A repeater with recent relay activity should show 🟢 Relaying; one that is alive but quiet should show 🟡 Idle.
