# v3.6.0 - The Forensics

CoreScope just got eyes everywhere. This release drops **path inspection**, **color-by-hash markers**, **clock skew detection**, **full channel encryption**, an **observer graph**, and a pile of robustness fixes that make your mesh network feel like it's being watched by someone who actually cares.

134 commits, 105 PRs merged, 18K+ lines added. Here's what shipped.

---

## 🚀 New Features

### Path-Prefix Candidate Inspector (#944, #945)
The marquee feature. Click any path segment and CoreScope opens an interactive inspector showing every candidate node that could match that hop prefix - plotted on a map with scoring by neighbor-graph affinity and geographic centroid. Ambiguous hops? Now you can see *why* they're ambiguous and pick the right one.

**Why you'll love it:** No more guessing which `0xA3` is the real repeater. The inspector lays out every candidate, scores them, and lets you drill in visually.

### Color-by-Hash Packet Markers (#948, #951)
Every packet type gets a vivid, hash-derived color - on the live feed, map polylines, and flying-packet animations. Bright fill with dark outline for contrast. No more monochrome blobs - you can visually track packet flows by color at a glance.

### Node Filter on Live Page (#924, #771)
Filter the live packet stream to show only traffic flowing through a specific node. Pick a repeater, see exactly what it's carrying. That simple.

### Clock Skew Detection (#746, #752, #828, #850)
Full pipeline: backend computes drift using Theil-Sen regression with outlier rejection (#828), the UI shows per-node badges, detail sparklines, and fleet-wide analytics (#752). Bimodal clock severity (#850) surfaces flaky-RTC nodes that toggle between accurate and drifted - instead of hiding them as "No Clock."

**Why you'll love it:** Nodes with bad clocks silently corrupt your timeline. Now they glow red before they ruin your analysis.

### Observer Graph (M1+M2) (#774)
Observers are now first-class graph citizens. CoreScope builds a neighbor graph from observation overlaps, scores hop-resolver candidates by graph edges (#876), and uses geographic centroid for tiebreaking. The observer topology is visible and queryable.

### Channel Encryption - Full Stack (#726, #733, #750, #760)
Three milestones landed as one: DB-backed channel message history (#726), client-side PSK decryption in the browser (#733), and PSK channel management with add/remove UX and message caching (#750). Add a channel key in the UI, and CoreScope decrypts messages client-side - no server-side key storage. The add-channel button (#760) makes it dead simple.

**Why you'll love it:** Encrypted channels are no longer black boxes. Add your PSK, see the messages, search history - all without exposing keys to the server.

### Hash Collision Inspector (#758)
The Hash Usage Matrix now shows collision details for all hash sizes. When two nodes share a prefix, you see exactly who collides and at what size.

### Geofilter Builder - In-App (#735, #900)
The geofilter polygon builder is now served directly from CoreScope with a full docs page (#900). No more hunting for external tools. Link from the customizer, draw your polygon, done.

### Node Blacklist (#742)
`nodeBlacklist` in config hides abusive or troll nodes from all views. They're gone.

### Observer Retention (#764)
Stale observers are automatically pruned after a configurable number of days. Your observer list stays clean without manual intervention.

### Advert Signature Validation (#794)
Corrupt packets with invalid advert signatures are now rejected at ingest. Bad data never hits your store.

### Bounded Cold Load (#790)
`Load()` now respects a memory budget - no more OOM on cold start with a fat database. Combined with retention-hours cutoff (#917), cold start is safe on constrained hardware.

### Multi-Arch Docker Images (#869)
Official images now publish `amd64` + `arm64` in a single multi-arch manifest. Raspberry Pi operators: pull and run. No special tags needed.

### /nodes Detail Panel + Search (#868)
The nodes detail panel ships with search improvements (#862) - find nodes fast, see their full detail in a slide-out panel.

### Deduplicated Top Longest Hops (#848)
Longest hops are now deduplicated by pair with observation count and SNR cues. No more seeing the same link 47 times.

---

## 🔥 Performance Wins

### StoreTx ResolvedPath Elimination (#806)
The per-transaction `ResolvedPath` computation is gone - replaced by a membership index with on-demand decode. This was one of the hottest paths in the ingestor.

### Node Packet Queries (#803)
Raw JSON text search for node packets replaced with a proper `byNode` index (#673). Night and day.

### Channel Query Performance (#762, #763)
New `channel_hash` column enables SQL-level channel filtering. No more full-table scan to find messages in a channel.

### SQLite Auto-Vacuum (#919, #920)
Incremental auto-vacuum enabled - the database file actually shrinks after retention pruning. No more 2GB database holding 200MB of live data.

### Retention-Hours Cutoff on Load (#917)
`Load()` now applies `retentionHours` at read time, preventing OOM when the DB has more history than memory allows.

---

## 🛡️ Security & Robustness

### MQTT Reconnect with Bounded Backoff (#947, #949)
The ingestor now reconnects to MQTT brokers with exponential backoff, observability logging, and bounded retry. No more silent disconnects that kill your data stream.

---

## 🐛 Bugs Squashed

This release exterminates **40+ bugs** — from protocol-level hash mismatches to pixel-level CSS breakage. Operators told us what hurt; we listened.

- **Path inspector "Show on Map" missed origin and first hop** (#950) - map view now includes all hops
- **Content hash used full header byte** (#787) - content hashing now uses payload type bits only, fixing hash collisions between packets that differ only in header flags
- **Encrypted channel deep links showed broken UI** (#825, #826, #815) - deep links to encrypted channels now show a lock message instead of broken UI when you don't have the key
- **Geofilter longitude wrapping** (#925) - geofilter builder wraps longitude to [-180, 180]; southern hemisphere polygons no longer invert
- **Hash filter bypasses saved region filter** (#939) - hash lookups now skip the geo filter as intended
- **Companion-as-repeater excluded from path hops** (#935, #936) - non-repeater nodes no longer pollute hop resolution
- **Customize panel re-renders while typing** (#927) - text fields keep focus during config changes
- **Per-observation raw_hex** (#881, #882) - each observer's hex dump now shows what *that observer* actually received
- **Per-observation children in packet groups** (#866, #880) - expanded groups show per-obs data, not cross-observer aggregates
- **Full-page obs-switch** (#866, #870) - switching observers updates hex, path, and direction correctly
- **Packet detail shows wrong observation** (#849, #851) - clicking a specific observation opens *that* observation
- **Byte breakdown hop count** (#844, #846) - derived from `path_len`, not aggregated `_parsedPath`
- **Transport-route path_len offset** (#852, #853) - correct offset calculation + CSS variable fix
- **Packets/hour chart bars + x-axis** (#858, #865) - bars render correctly, x-axis labels properly decimated
- **Channel timeline capped to top 8** (#860, #864) - no more 47-channel chart spaghetti
- **Reachability row opacity removed** (#859, #863) - clean rows without misleading gradient
- **Sticky table headers on mobile** (#861, #867) - restored after regression
- **Map popup 'Show Neighbors' on iOS Safari** (#840, #841) - link actually works now
- **Node detail Recent Packets invisible text** (#829, #830) - CSS fix
- **/api/packets/{hash} falls back to DB** (#827, #831) - when in-memory store misses, DB catches it
- **IATA filter bypass for status messages** (#694, #802) - status packets no longer filtered out by airport codes
- **Desktop node click URL hash** (#676, #739) - clicking a node updates the URL for deep linking
- **Filter params in URL hash** (#682, #740) - all filter state serialized for shareable links
- **Hide undecryptable channel messages** (#727, #728) - clean default view
- **TRACE path_json uses path_sz** (#732) - correct field from flags byte, not header hash_size
- **Multi-byte adopters** (#754, #767) - all node types, role column, advert precedence
- **Channel key case sensitivity** (#761) - Public decode works correctly
- **Transport route field offsets** (#766) - correct offsets in field table
- **Clock skew sanity checks** (#769) - filter epoch-0, cap drift, require minimum samples
- **Neighbor graph slider persistence** (#776) - default 0.7, persisted to localStorage
- **Node detail panel navigation** (#779, #785) - Details/Analytics links actually navigate
- **Channel key removal** (#898) - user-added keys for server-known channels can be removed
- **Side-panel Details on desktop** (#892) - opens full-screen correctly
- **Hex-dump byte ranges client-side** (#891) - computed from per-obs raw_hex
- **path_json derived from raw_hex at ingest** (#886, #887) - single source of truth
- **Path pill and byte breakdown hop agreement** (#885) - they match now
- **Mobile close button + toolbar scroll** (#797, #805) - accessible and scrollable
- **/health.recentPackets resolved_path fallback** (#810, #821) - falls back to longest sibling observation
- **Channel filter on Packets page** (#812, #816) - UI and API both fixed
- **Clock-skew section in side panel** (#813, #814) - renders correctly
- **Real RSS in /api/stats** (#832, #835) - surface actual RSS alongside tracked store bytes
- **Hash size detection for transport routes + zero-hop adverts** (#747) - correct detection
- **Repeater+observer merged map marker** (#745) - single marker, not two overlapping

---

## 🎨 UI Polish

- QA findings applied across the board (#832, #833, #836, #837, #838) - dozens of small UX fixes from systematic QA pass

---

## 📦 Upgrading

```bash
git pull
docker compose down
docker compose build prod
docker compose up -d prod
```

Your existing `config.json` works as-is. New optional config keys:
- `nodeBlacklist` - array of node hashes to hide
- `observerRetentionDays` - days before stale observers are pruned
- `memoryBudgetMB` - cap on in-memory packet store

### Verify

```bash
curl -s http://localhost/api/health | jq .version
# "3.6.0"
```

---

## 🙏 External Contributors

- **#735** ([@efiten](https://github.com/efiten)) - Serve geofilter builder from app, link from customizer
- **#739** ([@efiten](https://github.com/efiten)) - Desktop node click updates URL hash for deep linking
- **#740** ([@efiten](https://github.com/efiten)) - Serialize filter params in URL hash for shareable links
- **#742** ([@Joel-Claw](https://github.com/Joel-Claw)) - Add nodeBlacklist config to hide abusive/troll nodes
- **#761** ([@copelaje](https://github.com/copelaje)) - Fix channel key case sensitivity for Public decode
- **#764** ([@Joel-Claw](https://github.com/Joel-Claw)) - Add observer retention - prune stale observers after configurable days
- **#802** ([@efiten](https://github.com/efiten)) - Bypass IATA filter for status messages, fill SNR on duplicate observations
- **#803** ([@efiten](https://github.com/efiten)) - Replace raw JSON text search with byNode index for node packet queries
- **#805** ([@efiten](https://github.com/efiten)) - Mobile close button accessible + toolbar scrollable
- **#900** ([@efiten](https://github.com/efiten)) - App-served geofilter docs page
- **#917** ([@efiten](https://github.com/efiten)) - Apply retentionHours cutoff in Load() to prevent OOM on cold start
- **#924** ([@efiten](https://github.com/efiten)) - Node filter on live page - show only traffic through a specific node
- **#925** ([@efiten](https://github.com/efiten)) - Fix geobuilder longitude wrapping for southern hemisphere polygons
- **#927** ([@efiten](https://github.com/efiten)) - Skip customize panel re-render while text field has focus

---

## ⚠️ Breaking Changes

**None.** All API endpoints remain backwards-compatible. New fields are additive only.

---

## 📊 By the Numbers

| Stat | Count |
|------|-------|
| Commits | 134 |
| PRs merged | 105 |
| Lines added | 18,480 |
| Lines removed | 1,632 |
| Files changed | 110 |
| Contributors | 4 |

---

*Previous release: [v3.5.2](https://github.com/Kpa-clawbot/CoreScope/releases/tag/v3.5.2)*
