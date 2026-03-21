# Changelog

## [2.4.0] - 2026-03-21

### Added
- **Observation-level deeplinks** — `#/packets/HASH?obs=OBSERVER_ID` links directly to a specific observation; auto-expands group and selects the right row
- **Observation detail pane** — clicking a child observation row shows that observation's data (observer, SNR, path) instead of the parent packet
- **Channel name tags** in packet detail column — decrypted CHAN messages show a blue pill with channel name (#test, #sf, etc.)
- **Distance/Range analytics tab** — haversine distance calculations, link-type breakdown (R↔R, C↔R, C↔C), distance histogram, top 20 longest hops leaderboard, top 10 multi-hop paths
- **View on Map buttons** for distance leaderboard hops and paths
- **Realistic packet propagation mode** on live map — "Realistic" toggle buffers WS packets by hash, animates all paths simultaneously
- **Packet propagation time** shown in detail pane (time spread across observers)
- **Replay sends all observations** — ▶ button uses realistic propagation animation
- **Paths-through section** on node detail panel (both desktop and mobile)
- **Regional filters on all tabs** — shared RegionFilter component with pill/dropdown modes
- **Configurable map defaults** via `config.json` `mapDefaults` + `/api/config/map` endpoint
- **Favorites filter on live map** — filter animations and feed list for packets involving favorited nodes
- **Hash prefix labels** on map markers with deconfliction (spiral offsets, callout lines)
- **Shareable channel URLs** (`#/channels/HASH`)
- **Channel rainbow table** — pre-computed keys for common MeshCore channel names
- **Zero-API live channel updates** via WebSocket — no API re-fetches on new messages
- **Channel message dedup** by packet hash (multiple observers → one message entry)
- **1-second ticking timeAgo labels** on channel list (was 30s full re-render)
- **API key required** for POST `/api/packets` and `/api/perf/reset`
- **HTTPS support** (merged from lincomatic PR #105)
- **Graceful shutdown** (merged from lincomatic PR #109)

### Changed
- Channel key architecture simplified — `channelKeys` for pre-computed hex keys, `hashChannels` for channel names (auto-derived via SHA256)
- Channel keys use plain `String(channelHash)` instead of composite `ch_`/`unk_` prefixes
- Node region filtering uses ADVERT-based `_advertByObserver` index instead of data packet hashes (much more accurate)
- Observation sort in expanded packet groups: grouped by observer, earliest-observer first
- Transmission header row updates observer + path when earlier observation arrives
- Max hop distance filter tightened from 1000km to 300km (LoRa world record ~250km)
- Route view labels use deconflicted divIcons with callout lines
- Channels page only shows decrypted messages, hides encrypted garbage

### Fixed
- **Header row shows first observer's path** — removed server-side "longest path" override that replaced the transmission's path with the longest observation path
- **Transmission header observer mismatch** — when a later observation has an earlier timestamp, observer_id/observer_name/path_json now update to match
- **Observation sort field names** — sort was using wrong API field names (observer/rx_at vs observer_name/timestamp), producing random order
- **Auto-seeding on empty DB disabled** — fake data no longer inserted automatically; requires `--seed` flag or `SEED_DB=true`
- **Channel "10h ago" timestamp bug** — WS handler was using `packet.timestamp` (first_seen from earliest observation) instead of current time for lastActivity
- **Stale UI / packets not updating** — `insert()` used wrong ID type for packet lookup after insert (packets table ID vs transmissions view ID)
- **ADVERT timestamp validation removed** — field isn't stored; was rejecting valid nodes with slightly-off clocks
- **Channels page API spam** — removed unnecessary `invalidateApiCache()` calls; WS updates are now zero-API
- **Duplicate observations** in expanded packet view — missing dedup check in second insert code path
- **Analytics RF 500 error** — `Math.min(...arr)` stack overflow with 193K observations; replaced with for-loop helpers
- **Region filter bugs** — broken SQL using non-existent `sender_key` column, tab reset on filter change, missing from packets page
- **Channel hash display** — decimal→hex in analytics, keyed by decrypted name instead of hash byte
- **Corrupted repeater entries** — ADVERT validation at ingestion (pubkey, lat/lon, name, role)
- **Hash_size** — uses newest ADVERT (not oldest), precomputed at startup for O(1) lookups
- **Tab backgrounding** — skip animations when tab hidden, resume on return
- **Feed panel position** — raised from 58px to 68px to clear VCR bar
- **Hop disambiguation** — anchored from sender origin, not just observer position
- **btn-icon contrast** — text nearly invisible on dark background
- **Packet hash case normalization** for deeplink lookups

### Performance
- `/api/analytics/distance`: 3s → 630ms
- `/api/analytics/topology`: 289ms → 193ms
- `/api/observers`: 3s → 130ms
- `/api/nodes`: 50ms → 2ms (hash_size precompute)
- Event loop max latency: 3.2s → 903ms (startup only)
- Startup pre-warm yields event loop between endpoints via `setImmediate`
- Client-side hop resolution (moved from server)
- SQLite manual PASSIVE checkpointing (disabled auto-checkpoint)
- Single API call for packet expand (was 3)

## [2.3.0] - 2026-03-20

### Added
- **Packet Deduplication**: Normalized storage with `transmissions` and `observations` tables — packets seen by multiple observers are stored once with linked observation records
- **Observation count badges**: Packets page shows 👁 badge indicating how many observers saw each transmission
- **`?expand=observations`**: API query param to include full observation details on packet responses
- **`totalTransmissions` / `totalObservations`**: Health and analytics APIs return both deduped and raw counts
- **Migration script**: `scripts/migrate-dedup.js` for converting existing packet data to normalized schema
- **Live map deeplinks**: Node detail panel links to full node detail, observer detail, and filtered packets
- **CI validation**: `setup-node` added to deploy workflow for JS syntax checking

### Changed
- In-memory packet store restructured around transmissions (primary) with observation indexes
- Packets API returns unique transmissions by default (was returning inflated observation rows)
- Home page shows "Transmissions" instead of "Packets" for network stats
- Analytics overview uses transmission counts for throughput metrics
- Node health stats include `totalTransmissions` alongside legacy `totalPackets`
- WebSocket broadcasts include `observation_count`

### Fixed
- Packet expand showing only the collapsed row instead of individual observations
- Live page "Heard By" showing "undefined pkts" (wrong field name)
- Recent packets deeplink using query param instead of route path
- Migration script handling concurrent dual-write during live deployment

### Performance
- **8.19× dedup ratio on production** (117K observations → 14K transmissions)
- RAM usage reduced proportionally — store loads transmissions, not inflated observations
