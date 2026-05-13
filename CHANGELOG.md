# Changelog

## [3.7.2] — 2026-05-06

Hotfix release branched from `v3.7.1`. Cherry-picks PR #1121 only — no other changes.

### 🐛 Bug Fixes
- **Ingestor: backfill infinite loop on `path_json='[]'` rows** (#1119, #1121) — `BackfillPathJSONAsync` re-selected observations whose `path_json` was already `'[]'`, rewrote them to `'[]'`, and looped forever. The migration marker was never recorded and the ingestor sustained 2–3 MB/s WAL writes at idle (~76% CPU in `sqlite.Exec`). Fix: drop `'[]'` from the WHERE clause so the loop terminates after one full pass and the `backfill_path_json_from_raw_hex_v1` marker is written.

## [2.5.0] "Digital Rain" — 2026-03-22

### ✨ Matrix Mode — Full Cyberpunk Map Theme
Toggle **Matrix** on the live map to transform the entire visualization:
- **Green phosphor CRT aesthetic** — map tiles are desaturated and re-tinted through a `sepia → hue-rotate(70°) → saturate` filter chain, giving roads, coastlines, and terrain a faint green wireframe look against a dark background
- **CRT scanline overlay** — subtle horizontal lines with a gentle flicker animation across the entire map
- **Node markers dim to dark green** (#008a22 at 50% opacity) so they don't compete with packet animations
- **Forces dark mode** while active (saves and restores your previous theme on toggle off)
- **Disables heat map** automatically (incompatible visual combo)
- **All UI panels themed** — feed panel, VCR controls, node detail all go green-on-black with monospace font
- New markers created during Matrix mode (e.g. VCR timeline scrub) are automatically tinted

### ✨ Matrix Hex Flight — Packet Bytes on the Wire
When Matrix mode is enabled, packet animations between nodes show the **actual hex bytes from the raw packet data** flowing along the path:
- **Real packet data** — bytes come from the packet's `raw_hex` field, not random/generated
- **White leading byte** with triple-layer green neon glow (`text-shadow: 0 0 8px, 0 0 16px, 0 0 24px`)
- **Trailing bytes fade** from bright to dim green, shrinking in size with distance from the head
- **Scrolls through all bytes** in the packet as it travels each hop
- **60fps animation** via `requestAnimationFrame` with time-based interpolation (1.1s per hop)
- **300ms fade-out** after reaching the destination node
- Replaces the standard contrail animation; toggle off to restore normal mode

### ✨ Matrix Rain — Falling Packet Columns
A separate **Rain** toggle adds a canvas-rendered overlay of falling hex byte columns, Matrix-style:
- **Each incoming packet** spawns a column of its actual raw hex bytes falling from the top of the screen
- **Fall distance proportional to hop count** — 4+ hops reach the bottom of the screen; a 1-hop packet barely drops. Matches the real mesh network: more hops = more propagation = longer rain trail
- **Fall duration scales with distance** — 5 seconds for a full-screen drop, proportional for shorter
- **Multiple observations = more rain** — each observation of a packet spawns its own column, staggered 150ms apart. A packet seen by 8 observers creates 8 simultaneous falling columns with ±1 hop variation for visual variety
- **Leading byte is bright white** with green glow; trailing bytes progressively fade to green
- **Entire column fades out** in the last 30% of its lifetime
- **Canvas-rendered at 60fps** — no DOM overhead, handles hundreds of simultaneous drops
- **Works independently or with Matrix mode** — combine both for the full effect
- **Replay support** — the ▶ Replay button on packet detail pages now includes raw hex data so replayed packets produce rain

### 🐛 Bug Fixes
- **Fixed null element errors in Matrix hex flight** — `getElement()` returns null when DivIcon hasn't been rendered to DOM yet during fast VCR replay
- **Fixed animation null-guard cascade** — `pulseNode`, `animatePath`, and `drawAnimatedLine` now bail early if map layers are null (stale `setInterval` callbacks after page navigation)
- **Fixed WS broadcast with null packet** — deduplicated observations caused `fullPacket` to be null in WebSocket broadcasts
- **Fixed pause button crash** — was killing WS handler registration
- **Fixed multi-select menu close handler** — null-guard for missing elements

### ⚡ Technical Notes
- Matrix hex flight uses Leaflet `L.divIcon` markers for each character — the smoothness ceiling is Leaflet's DOM repositioning speed. CSS transitions were tested but caused stutter due to conflicts with Leaflet's internal transform updates.
- Matrix Rain uses a raw `<canvas>` overlay at z-index 9998 for zero-DOM-overhead rendering. Each drop is a simple `{x, maxY, duration, bytes, startTime}` struct rendered in a single `requestAnimationFrame` loop.
- Map tile tinting applies CSS filters to `.leaflet-tile-pane` and green overlays via `::before`/`::after` pseudo-elements on the map container (same element as `.leaflet-container`, so selectors use `.matrix-theme.leaflet-container` not descendant `.matrix-theme .leaflet-container`).

## [2.4.1] — 2026-03-22

Hotfix release for regressions introduced in v2.4.0.

### Fixed
- Packet ingestion broken: `insert()` returned undefined after legacy table removal, causing all MQTT packets to fail silently
- Live packet updates not working: pause button `addEventListener` on null element crashed `init()`, preventing WS handler registration
- Pause button not toggling: event delegation was on `app` variable not in IIFE scope; moved to `document`
- WS broadcast had null packet data when observation was deduped (2nd+ observer of same packet)
- Multi-select filter menu close handler crashed on null `observerFilterWrap`/`typeFilterWrap` elements
- Live map animation cleanup crashed with null `animLayer`/`pathsLayer` after navigating away (setInterval kept firing)

## [2.4.0] — 2026-03-22

UI polish, client-side filtering, time window selector, DB cleanup, and bug fixes.

### Added
- Observation-level deeplinks (`#/packets/HASH?obs=OBSERVER_ID`)
- Observation detail pane (click any child row for its specific data)
- Observation sort: Observer / Path ↑↓ / Time ↑↓ with persistent preference
- Ungrouped mode flattens all observations into individual rows
- Sort help tooltip (ⓘ) explaining each mode
- Distance/Range analytics tab with haversine calculations
- View on Map buttons for distance leaderboard entries
- Realistic packet propagation mode on live map
- Packet propagation time in detail pane
- Replay sends all observations with realistic animation
- Paths-through section on node detail (desktop + mobile)
- Regional filters on all tabs (shared RegionFilter component)
- Favorites filter on live map (packet-level, not node markers)
- Configurable map defaults via `config.json`
- Hash prefix labels on map with spiral deconfliction + callout lines
- Channel rainbow table (pre-computed keys for common names)
- Zero-API live channel updates via WebSocket
- Channel message dedup by packet hash
- Channel name tags (blue pill) in packet detail column
- Shareable channel URLs (`#/channels/HASH`)
- API key required for POST endpoints
- HTTPS support (lincomatic PR #105)
- Graceful shutdown (lincomatic PR #109)
- Filter bar: logical grouping, consistent 34px height, help tooltips
- Multi-select Observer and Type filters (checkbox dropdowns, OR logic)
- Hex Paths toggle: show raw hex hash prefixes vs resolved node names
- Time window selector (15min/30min/1h/3h/6h/12h/24h/All) replaces fixed packet count limit
- Pause/resume button (⏸/▶) for live WebSocket updates with buffered packet count
- localStorage persistence for all filter/view preferences

### Changed
- Channel keys: plain `String(channelHash)`, `hashChannels` for auto-derived SHA256
- Node region filtering uses ADVERT-based index (accurate local presence vs mesh-wide routing)
- Header row reflects first sorted observation's data
- Max hop distance filter: 1000km → 300km (LoRa record ~250km)
- Route view labels use deconflicted divIcons
- Channels page hides encrypted messages, shows only decrypted
- Dark mode: active filter buttons retain accent styling
- Region dropdown: `IATA - Friendly Name` format, proper sizing
- Observer/Type filters are pure client-side (no API calls on filter change)
- Packet loading: time-window based (`since`) instead of fixed count limit
- Header row shows matching observer when observer filter is active

### Removed
- Legacy `packets` and `paths` database tables (auto-migrated on startup)
- Redundant server-side type/observer filtering (client filters in-memory)

### Fixed
- Header row showed longest path instead of first observer's path
- Observer/path mismatch when earlier observation arrives later
- Auto-seeding fake data on empty DB (now requires `--seed` flag)
- Channel "10h ago" bug (used stale `first_seen` instead of current time)
- Stale UI: wrong ID type for packet lookup after insert
- ADVERT timestamp validation rejecting valid nodes
- Channels page API spam on every WS update
- Duplicate observations in expanded view
- Analytics RF 500 error (stack overflow with 193K observations)
- Region filter SQL using non-existent column
- Channel hash: decimal→hex, keyed by decrypted name
- Corrupted repeater entries (ADVERT validation at ingestion)
- Hash_size: uses newest ADVERT, precomputed at startup
- Tab backgrounding: skip animations, resume cleanly
- Feed panel position (obscured by VCR bar)
- Hop disambiguation anchored from sender origin
- Packet hash case normalization for deeplinks
- Critical: packet ingestion broken after legacy table removal (`insert()` returned undefined)
- Sort help tooltip rendering (CSS pseudo-elements don't support newlines)

### Performance
- `/api/analytics/distance`: 3s → 630ms
- `/api/analytics/topology`: 289ms → 193ms
- `/api/observers`: 3s → 130ms
- `/api/nodes`: 50ms → 2ms (precomputed hash_size)
- Event loop max: 3.2s → 903ms (startup only)
- Pre-warm yields event loop via `setImmediate`
- Client-side hop resolution
- SQLite manual PASSIVE checkpointing
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
