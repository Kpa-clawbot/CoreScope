# Cornmeister fork — what's here that evo doesn't have

This document lists the meaningful divergences between this fork (`Cornmeister/CoreScope`) and the upstream
`marcelverdult/corescope-evo`. It exists so Marcel (or anyone) can cherry-pick back without re-inventing
anything.

Last synced from upstream: `evo/main` as of **2026-05-18** (through commit `0f5dd32`).

---

## 1. GPU / WebGPU keygen

We kept (and extended) the original WebGPU-accelerated Monte-Carlo key generator that Marcel removed in his
`d8f00fc` / `1bcb40a` / `87366fe` / `d1de5bd` series. The relevant files:

- `public/mc-keygen.js` — full GPU path intact; falls back to CPU when WebGPU is unavailable
- `public/vendor/mc-keygen-gpu.js` (vendored WebGPU build)

If you want GPU keygen back in evo, cherry-pick from here rather than rebuilding from scratch.

---

## 2. Ingestor I/O graphs (perf page)

Added in `fb12f8f` (`feat(perf): add ingestor I/O card and graph-tab chart groups`).

Backend already existed in `perf_io.go` (your commit `0042833`). This is purely frontend work in
`public/perf.js`:

- **`ingestorIORates(ingestor)`** — client-side delta computation from cumulative `PerfIOSample` counters
  (`readBytes`, `writeBytes`, `syscR`, `syscW`, `sampledAt`). Mirrors the `writeSourceRates()` pattern.
- **Ingestor I/O stat card** in `renderCards()` — shown when `ioStats.ingestor` is non-null; displays
  Read/Write B/s and Syscalls Read/Write per second.
- **3 new `CHART_GROUPS` entries** in the `'Ingestor'` category:
  - `ingestor-throughput` — Read B/s + Write B/s
  - `ingestor-syscalls` — Syscalls Read/s + Syscalls Write/s
  - `ingestor-write-sources` — Tx/s + Obs/s + WAL commits/s
- **`writeWalRate`** added to `writeSourceRates()` using `rate('walCommits')`.

---

## 3. Critical panic fix: rebuildDistIndexMaps after eviction

Commit: `c0b2199` (from a previous session, already on master before this sync).

`cmd/server/store.go` — in `evictStaleInternal()`, after the distance-slice compaction:

```go
s.distPaths = newDistPaths
s.rebuildDistIndexMaps()  // ← this line was missing upstream
```

Without it, `distHopsByTx` / `distPathsByTx` position maps go stale after eviction. The next call to
`removeDistRecordsForTxs()` uses an out-of-bounds index and panics. This was a real production crash.

---

## 4. Observer DB methods preserved in db.go

During the `c229e03` cherry-pick, that commit's diff stripped several observer-specific methods we had
added. We restored them in `4411e69`. These methods are in `cmd/server/db.go`:

- `Stats.OnlineObservers` field
- `Observer.Repeat` field
- `DB.hasObserverLastPacket` / `hasObserverRepeat` schema-detection fields
- `GetObservers()` / `GetObserverByID()` with dynamic column lists
- `GetObserverCounts()` returning `*ObserverCounts`
- `GetObserverSources()` for MQTT broker tracking
- `ObserverPacketWindows` + `GetObserverAllPacketCounts()`
- `GetRecentDirectPacketsForNode()`
- SQLite stats cache (`sqliteStatsCacheMu`, `GetDBSizeStatsTyped()`, `cloneSqliteStats()`)

These were added to support the Netherlands-specific observer dashboard. The API routes that use them are
in `routes.go` (`/api/observers`, `/api/observer/:id`, `/api/nodes/:id/packets`, etc.).

---

## 5. prefetchResolvedPathsForTxs in resolved_index.go

Commit: `38ce5d9`. Added to `cmd/server/resolved_index.go` to satisfy the `store_refactor_test.go` brought
in by `8954797`. Implements a batch-prefetch of resolved paths for a slice of TX IDs using chunked
`IN (...)` queries (499-item chunks to stay under SQLite's variable limit), with LRU population after
all chunks complete.

---

## 6. NGGYU audio voice

`public/audio-v7-nggyu.js` — an 8-bit square-wave voice that advances through the NGGYU chorus melody
(A major → key-change pickup → B major) one note per incoming packet. Registered as `MeshAudio.registerVoice('NGGYU', ...)`.

Script tag wired in `public/index.html`. Hidden easter egg — commit message says nothing about what it plays.

---

## 7. All audio voices wired in index.html

`public/index.html` was missing `<script>` tags for voices v2–v6. Fixed: all seven voices (constellation,
pulse, drone, chiptune, blaster, warzone, NGGYU) now load.

---

## 8. Netherlands channel data

`cmd/server/data/channels.json` contains 318 channels sourced from the Netherlands MeshCore mesh. Your
commit `839c887` merged a subset of these. The full set lives here.
