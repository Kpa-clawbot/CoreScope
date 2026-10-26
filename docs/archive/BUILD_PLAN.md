# MeshCore Analyzer — Build Plan

> **Archived plan.** Historical planning material; proposals, status claims, and
> instructions below may be outdated or unimplemented. For current guidance, see
> the [README](../../README.md) and [deployment guide](../../DEPLOY.md).

## What Is This
Open-source, self-hosted MeshCore mesh network packet analyzer. Community alternative to the closed-source `analyzer.letsmesh.net`.

**Live instance:** https://analyzer.00id.net

## Tech Stack
- **Frontend**: SPA, vanilla HTML/CSS/JS, Leaflet maps, WebSocket live feed, Canvas animations
- **Backend**: Node.js + Express + better-sqlite3 + ws + mqtt
- **Decoder**: Custom `decoder.js` (from MeshCore Packet.h spec)
- **Data**: SQLite, MQTT ingestion, REST API, manual packet injection

## Architecture

### Custom Decoder
The `@michaelhart/meshcore-decoder` npm library has a path parsing bug — treats `path_length` as raw byte count. Per `Packet.h`, it encodes `hash_size` (top 2 bits) + `hash_count` (lower 6 bits). We wrote `decoder.js` from scratch.

### Packet Ingestion
- MQTT subscriber (configurable broker/topic)
- Companion bridge (BLE → MQTT via `meshcore_observer.py`)
- POST `/api/packets` for manual injection
- WebSocket broadcast to all connected clients

### Channel Decryption
- Hashtag channel keys derived via `sha256("#name")[:16]`
- 1-byte channel hash means collisions — must verify by successful decryption
- Known PSKs configurable in `config.json`

## Completed Milestones

### M1: Custom Packet Decoder ✅
### M2: SQLite Schema ✅
### M3: Server + MQTT + WebSocket + API ✅
### M4: SPA Shell + Packets Page ✅
- Grouped/ungrouped views, detail panel, color-coded byte breakdown
- Resizable columns with localStorage persistence
- Packet hash click → detail with hex dump, field table

### M5: Map Page ✅
- Leaflet dark tiles, node markers by role, clustering
- Last-heard filters, region quick-jump
- Click marker → popup with node info

### M6: Channels Page ✅
- Chat-style UI with channel sidebar, message feed
- Decryption using known PSKs, @mention highlighting
- Hash collision filtering (encrypted packets excluded from named channels)

### M7: Nodes Page ✅
- Searchable directory with role tabs, detail panel
- QR code sharing, advert timeline
- Node health cards with status reasoning
- Favorites system (localStorage stars, "Your Nodes" home section)
- Responsive mobile layout, full-screen single-node detail via `#/nodes/PUBKEY`
- Prefix search dropdown on home page

### M8: Trace Routes ✅
### M9: Observer Status ✅
### M10: Polish ✅
- Dark mode toggle, global search (Ctrl+K)
- Config file support, README

### M11: Synthetic Packet Generator ✅
### M12: End-to-End Validation ✅
### M13: Frontend Smoke Tests ✅

### M14: Accessibility ✅
- Semantic HTML, ARIA attributes, keyboard navigation
- WCAG AA contrast verification on map and channel screens
- Proper focus management, screen reader landmarks

### M15: Dark Mode Overhaul ✅
- Explicit `data-theme` attribute (never remove — `prefers-color-scheme` trap)
- Consistent dark theme across all pages

### M16: Loading States & Visual Polish ✅
### M17: Mobile Responsive ✅
- Fully responsive layout, hamburger menu
- Touch-friendly controls, viewport fixes

### M18: Home Page ✅
- Hero section, node health cards, prefix search dropdown
- "Your Nodes" favorites section, "View packets" filtered link

### M19: Analytics Dashboard ✅
- Deep mesh network insights, charts, activity heatmaps

### M20: Live Page ✅
- Real-time animated map with contrail trails and shockwave pulses
- Traveling dots along hop paths, ghost hop interpolation
- Sound effects per packet type (toggleable)
- Heat map overlay, packet feed with detail cards
- Replay button on feed cards
- Auto-hide nav bar on inactivity
- Nav bar height-aware layout with Leaflet invalidateSize

### M21: VCR Controls ✅
- Pause/Play/Rewind/Speed (1x/2x/4x/8x)
- Buffer-based architecture — WS always stores, display consumes from playhead
- Option C unpause: "You missed N packets. [▶ Replay] [⏭ Skip to live]"
- Timeline scrubber with density sparkline, red playhead, click-to-seek
- Scope selector: 1h / 6h / 12h / 24h
- Rewind fetches from DB, prepends to buffer
- Replay uses real timestamp gaps (capped 2s) / speed multiplier

## File Structure
```
meshcore-analyzer/
├── package.json
├── config.json         (MQTT broker, channel keys, regions)
├── server.js           (Express + WS + MQTT + API)
├── decoder.js          (custom packet decoder)
├── db.js               (SQLite schema + queries)
├── data/
│   └── meshcore.db
├── public/
│   ├── index.html      (SPA shell + nav)
│   ├── style.css       (global theme)
│   ├── app.js          (router, WS client, utils)
│   ├── home.js/css     (home page)
│   ├── packets.js      (packets browser)
│   ├── map.js          (Leaflet map)
│   ├── channels.js     (channel chat)
│   ├── nodes.js        (node directory)
│   ├── traces.js       (packet traces)
│   ├── observers.js    (observer status)
│   ├── analytics.js    (analytics dashboard)
│   ├── live.js/css     (live view + VCR)
│   └── vendor/         (third-party libs)
└── tools/
    ├── generate-packets.js
    ├── e2e-test.js
    └── frontend-test.js
```

## Default Test Data
Public channel key: `8b3387e9c5cdea6ac9e5edbaa115cd72`
