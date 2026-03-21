# Release Notes Draft — v2.4.0 (WIP)

*Changes since v2.3.0 (March 21, 2026)*

---

## 🗺️ Map Improvements

- **Hash prefix labels on map** — repeater markers show their short hash ID (e.g. `5B`, `BEEF`) with byte-size indicator
- **Label overlap prevention** — spiral deconfliction algorithm with callout lines for dense marker areas
- **Hash prefix in node popup** — bold display with byte size
- **Configurable map defaults** — center/zoom now configurable via `config.json` `mapDefaults` (#115)
- **View on Map from distance leaderboard** — hop and path entries in distance analytics are clickable to view on map
- **Route view improvements** — hide default markers, show origin node, label deconfliction on route markers
- **ADVERT node names are clickable** — link to node detail page from map popups

## 📊 Analytics

- **Distance/Range analytics tab** — new tab with summary cards, link-type breakdown (R↔R, C↔R, C↔C), distance histogram, top 20 longest hops leaderboard, top 10 longest multi-hop paths
- **300km max hop distance cap** — filters bogus hops from gateway artifacts (LoRa world record is ~250km)
- **Channel hash displayed as hex** in analytics (#103)
- **RF analytics region filtering fixed** — separated from SNR filtering, correct packet counts (#111)

## 🔒 Channels

- **Channel name resolution fixed** — uses decryption key, not just hash byte (#108)
- **Simplified channel key scheme** — plain hash keys, no composite `ch_`/`unk_` prefixes
- **`hashChannels` config** — derive channel keys from names via SHA256 instead of hardcoding hex keys
- **Rainbow table** — pre-computed keys for ~30 common MeshCore channel names
- **Encrypted messages hidden** — channel views only show successfully decrypted messages
- **CHAN packet detail renderer** — dedicated display for decrypted channel messages
- **Live channel updates** — channels page refreshes immediately on new messages via WebSocket

## 🌐 Regional Filters

- **Regional filters on all tabs** — packets, nodes, analytics, channels (#111)
- **ADVERT-based node region filtering** — uses local broadcast data instead of data packet hashes for accurate geographic filtering
- **Dropdown mode** — auto-switches to dropdown for >4 regions, forced dropdown on packets page
- **Region filter UI** — labels, ARIA accessibility, consistent styling

## ⭐ Favorites

- **Favorites filter on live map** (#106) — filter packet animations and feed list for packets involving favorited nodes
- **Packet-level filtering only** — all node markers stay visible regardless of filter

## 📦 Packet Handling

- **Realistic packet propagation mode** — "Realistic" toggle buffers WS packets by hash, animates all paths simultaneously
- **Replay sends all observations** — ▶ button uses realistic propagation animation
- **Propagation time in detail pane** — shows time spread across observers
- **Paths-through section** — added to both desktop and mobile node detail panels
- **Dedup observations** — UNIQUE constraint on (hash, observer_id, path_json), INSERT OR IGNORE
- **ADVERT validation** — validates pubkey, lat/lon, name, role, timestamp before upserting nodes (#112)
- **Tab backgrounding fix** — skip animations when tab hidden, resume cleanly (#114)

## 🚀 Performance

- **Client-side hop resolution** — eliminated all `/api/resolve-hops` server calls; hops resolved locally from cached node list
- **3→1 API calls on packet group expand** — single fetch serves expand + detail panel
- **In-memory packet store optimization** — build insert rows from incoming data instead of DB round-trip
- **SQLite WAL auto-checkpoint disabled** — manual PASSIVE checkpoint every 5 minutes eliminates random 200ms+ event loop spikes
- **Startup pre-warm deferred** — 5s delay lets initial client requests complete first; all pre-warm via HTTP self-requests with event loop yielding
- **Shared cached node list** — replaced 8 separate `SELECT FROM nodes` queries across endpoints
- **Cached path JSON parse** — avoid repeated JSON.parse on path_json fields
- **Precomputed hash_size map** — `/api/nodes` from 50ms to 2ms
- **Node paths endpoint rewrite** — uses `disambiguateHops()` with prefix index, 560ms→190ms
- **Analytics distance optimization** — 3s→630ms cold cache
- **Analytics topology optimization** — 289ms→193ms
- **Observers endpoint optimization** — 3s→130ms
- **Event loop max latency** — 3.2s→105ms (startup), steady-state p95 under 200ms

## 🔧 Infrastructure

- **`packets_v` SQL view** — JOINs transmissions+observations, replacing direct queries to legacy `packets` table (prep for table drop)
- **Hash-based URLs** — all user-facing URLs use `#/` routing for stability across restarts
- **API key required** for POST `/api/packets` and `/api/perf/reset`
- **HTTPS support** merged (PR #105, lincomatic)
- **Graceful shutdown** merged (PR #109, lincomatic)
- **`hashChannels` config** merged (PR #107, lincomatic)
- **`config.example.json` updated** with hashChannels examples

## 🐛 Bug Fixes

- Hash size display shows "?" for null instead of defaulting to "1B"
- Hash size uses newest ADVERT, not oldest or first-seen
- Feed panel position raised to clear VCR bar
- Hop disambiguation anchored from sender origin, not just observer
- Packet hash case normalized for deeplink lookups
- Region filter no longer resets analytics tab to overview
- Network status missing `?` in region query string fixed
- Null safety for analytics stats in empty regions
- BYOP button accessibility (aria-labels, focus styles)
- btn-icon contrast on dark backgrounds

## ⚠️ Known Issues / Still TODO

- Legacy `packets` table (308K rows) and `paths` table (1.7M rows) still in DB — pending drop after bake period (will shrink DB from ~381MB to ~80MB)
- Channel decryption in prod needs hashChannels config update
- Three-tier cache TTLs agreed but not yet applied
- Per-region subpath precomputation at startup not yet implemented
- Hardcoded health thresholds in `home.js` and `observer-detail.js`
- Issues #28-32 still open (dead code, Leaflet SRI, empty states, dark mode)

---

*67 commits since v2.3.0*
