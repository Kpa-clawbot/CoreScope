# Performance — v2.1.0

**Dataset:** 28,014 packets, ~650 nodes, 2 observers  
**Hardware:** ARM64 (MikroTik CCR2116), single-core Node.js

## A/B Benchmark: v2.0.1 (before) vs v2.1.0 (after)

All times are averages over 3 runs. "Cached" = warm TTL cache hit.

| Endpoint | v2.0.1 | v2.1.0 (cold) | v2.1.0 (cached) | Speedup |
|---|---|---|---|---|
| **Bulk Health** | 7,059 ms | 3 ms | 1 ms | **7,059×** |
| **Node Analytics** | 381 ms | 2 ms | 1 ms | **381×** |
| **Hash Sizes** | 353 ms | 193 ms | 1 ms | **353×** |
| **Topology** | 685 ms | 579 ms | 2 ms | **342×** |
| **RF Analytics** | 253 ms | 235 ms | 1 ms | **253×** |
| **Channels** | 206 ms | 77 ms | 1 ms | **206×** |
| **Node Health** | 195 ms | 1 ms | 1 ms | **195×** |
| **Node Detail** | 133 ms | 1 ms | 1 ms | **133×** |
| **Channel Analytics** | 95 ms | 73 ms | 2 ms | **47×** |
| **Packets (grouped)** | 76 ms | 33 ms | 28 ms | **2×** |
| **Stats** | 2 ms | 1 ms | 1 ms | 2× |
| **Nodes List** | 3 ms | 2 ms | 2 ms | 1× |
| **Observers** | 1 ms | 8 ms | 1 ms | 1× |

## Architecture

### Two-Layer Performance Stack

1. **In-Memory Packet Store** (`packet-store.js`)
   - All packets loaded from SQLite into RAM on startup (~28K packets = ~12MB)
   - Indexed by `id`, `hash`, `observer`, and `node` (Map-based O(1) lookup)
   - Ring buffer with configurable max memory (default 1GB, ~2.3M packets)
   - SQLite becomes **write-only** for packets — reads never touch disk
   - New packets from MQTT written to both RAM + SQLite

2. **TTL Cache** (`server.js`)
   - Computed API responses cached with configurable TTLs (via `config.json`)
   - Smart invalidation: packet bursts only invalidate channels/observers; analytics expire by TTL only
   - Pre-warmed on startup: subpaths, RF, topology, channels, hash-sizes, bulk-health
   - Result: most API responses served in **1-2ms** from cache

### Key Optimizations

- **Eliminated all `LIKE '%pubkey%'` queries**: Every node-specific endpoint was doing full-table scans on the packets table via `decoded_json LIKE '%pubkey%'`. Replaced with O(1) `pktStore.byNode` Map lookups.
- **Single-pass computations**: Channels, analytics, and subpaths computed in one loop instead of multiple SQL queries.
- **Client-side WebSocket prepend**: New packets appended to the table without re-fetching the API.
- **RF response compression**: Server-side histograms + scatter downsampling (1MB → 15KB).
- **Configurable everything**: All TTLs, packet store limits, and thresholds in `config.json`.

### What Didn't Work

- **Background refresh (`setInterval`)**: Attempted to re-warm caches at 80% TTL. Blocked the event loop — Node.js is single-threaded. Response times went from 3ms to 1,200ms. Reverted immediately.
- **Worker threads**: `structuredClone` overhead of 416ms for 28K packets negated the compute savings. Only viable at 10× data growth or with `SharedArrayBuffer` (zero-copy).

## Running the Benchmark

```bash
# Stop the production server first
supervisorctl stop meshcore-analyzer

# Run A/B benchmark (launches two servers: old v2.0.1 vs current)
./benchmark-ab.sh

# Restart production
supervisorctl start meshcore-analyzer
```
