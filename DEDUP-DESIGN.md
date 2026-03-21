# Packet Deduplication Design

## The Problem

A single physical RF transmission gets recorded as N rows in the DB, where N = number of observers that heard it. Each row has the same `hash` but different `path_json` and `observer_id`.

### Example
```
Pkt 1 repeat 1: Path: A→B→C→D→E   (observer E)
Pkt 1 repeat 2: Path: A→B→F→G      (observer G)
Pkt 1 repeat 3: Path: A→C→H→J→K    (observer K)
```

- Repeater A sent 1 packet, not 3
- Repeater B sent 1 packet, not 2 (C and F both heard the same broadcast)
- The hash is identical across all 3 rows

### Why the hash works

`computeContentHash()` = `SHA256(header_byte + payload)`, skipping path hops. Two observations of the same original packet through different paths produce the same hash. This is the dedup key.

## What's inflated (and what's not)

| Context | Current (inflated?) | Correct behavior |
|---------|-------------------|------------------|
| Node "total packets" | COUNT(*) — inflated | COUNT(DISTINCT hash) for transmissions |
| Packets/hour on observer page | Raw count | Correct — each observer DID receive it |
| Node analytics throughput | Inflated | DISTINCT hash |
| Live map animations | N animations per physical packet | 1 animation? Or 1 per path? TBD |
| "Heard By" table | Observations per observer | Correct as-is |
| RF analytics (SNR/RSSI) | Mixes observations | Each observation has its own SNR — all valid |
| Topology/path analysis | All paths shown | All paths are valuable — don't discard |
| Packet list (grouped mode) | Groups by hash already | Probably fine |
| Packet list (ungrouped) | Shows every observation | Maybe show distinct, expand for repeats? |

## Key Principle

**Observations are valuable data — never discard them.** The paths tell you about mesh topology, coverage, and redundancy. But **counts displayed to users should reflect reality** (1 transmission = 1 count).

## Design Decisions Needed

1. **What does "packets" mean in node detail?** Unique transmissions? Total observations? Both?
2. **Live map**: 1 animation with multiple path lines? Or 1 per observation?
3. **Analytics charts**: Should throughput charts show transmissions or observations?
4. **Packet list default view**: Group by hash by default?
5. **New metric: "observation ratio"?** — avg observations per transmission tells you about mesh redundancy/coverage

## Work Items

- [ ] **DB/API: Add distinct counts** — `findPacketsForNode()` and health endpoint should return both `totalTransmissions` (DISTINCT hash) and `totalObservations` (COUNT(*))
- [ ] **Node detail UI** — show "X transmissions seen Y times" or similar
- [ ] **Bulk health / network status** — use distinct hash counts
- [ ] **Node analytics charts** — throughput should use distinct hashes
- [ ] **Packets page default** — consider grouping by hash by default
- [ ] **Live map** — decide on animation strategy for repeated observations
- [ ] **Observer page** — observation count is correct, but could add "unique packets" column
- [ ] **In-memory store** — add hash→[packets] index if not already there (check `pktStore.byHash`)
- [ ] **API: packet siblings** — `/api/packets/:id/siblings` or `?groupByHash=true` (may already exist)
- [ ] **RF analytics** — keep all observations for SNR/RSSI (each is a real measurement) but label counts correctly
- [ ] **"Coverage ratio" metric** — avg(observations per unique hash) per node/observer — measures mesh redundancy

## Live Map Animation Design

### Current behavior
Every observation triggers a separate animation. Same packet heard by 3 observers = 3 independent route animations. Looks like 3 packets when it was 1.

### Options considered

**Option A: Single animation, all paths simultaneously (PREFERRED)**
When a hash first arrives, buffer briefly (500ms-2s) for sibling observations, then animate all paths at once. One pulse from origin, multiple route lines fanning out simultaneously. Most accurate — this IS what physically happened: one RF burst propagating through the mesh along multiple paths at once.

Timing challenge: observations don't arrive simultaneously (seconds apart). Need to buffer the first observation, wait for siblings, then render all together. Adds slight latency to "live" feel.

**Option B: Single animation, "best" path only** — REJECTED
Pick shortest/highest-SNR path, animate only that. Clean but loses coverage/redundancy info.

**Option C: Single origin pulse, staggered path reveals** — REJECTED
Origin pulses once, paths draw in sequence with delay. Dramatic but busy, and doesn't reflect reality (the propagation is simultaneous).

**Option D: Animate first, suppress siblings** — REJECTED (pragmatic but inaccurate)
First observation gets animation, subsequent same-hash observations silently logged. Simple but you never see alternate paths on the live map.

### Implementation notes (for when we build this)
- Need a client-side hash buffer: `Map<hash, {timer, packets[]}>` 
- On first WS packet with new hash: start timer (configurable, ~1-2s)
- On subsequent packets with same hash: add to buffer, reset/extend timer
- On timer expiry: animate all buffered paths for that hash simultaneously
- Feed sidebar could show consolidated entry: "1 packet, 3 paths" with expand
- Buffer window should be configurable (config.json)

## Status

**Discussion phase** — no code changes yet. User wants to finalize design before implementation. Live map changes tabled for later.
