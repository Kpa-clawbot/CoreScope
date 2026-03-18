# New User Experience

## Overview
The home page (`#/` or `#/home`) is the front door to the analyzer — a diagnostic companion for mesh users to check if their node is being heard, and a dashboard for regulars to monitor favorites.

## Features

### Hero Search
- Prefix search with auto-dropdown as you type (name or public key)
- Quick stats bar: active nodes · packets today · observers online
- Results link to node health card or node detail page

### Node Health Cards
Shown after search or on home page for favorited nodes.

**Status logic:**
- 🟢 HEALTHY: heard by observers in the last hour
- 🟡 DEGRADED: last heard 1-24h ago
- 🔴 SILENT: not heard in 24+ hours

**Contents:** last heard, observer count, avg SNR with quality label, hop count, packets today, status reasoning text.

### Favorites ("Your Nodes")
- Star/unstar nodes from any node detail view
- Stored in localStorage
- "Your Nodes" section on home page with live status cards
- Quick access to node detail via click

### Quick Setup Checklist
Collapsible accordion for troubleshooting:
1. Correct preset / frequency
2. Send a flood advert
3. Check repeat count
4. Nearby repeaters (link to map)
5. Frequency mismatch warning

### Navigation Footer
Links to full dashboard pages: Map, Channels, Packet Inspector, Observers

## API Endpoints

### GET /api/nodes/search?q=<query>
Prefix search by name or public key, returns top 10 matches.

**Note:** Static path `/api/nodes/search` must be defined BEFORE parameterized `/api/nodes/:pubkey` in Express routes.

### GET /api/nodes/:pubkey/health
Returns health summary: node info, status, observer list with SNR/RSSI, stats, recent packets.

## Files
- `public/home.js` + `public/home.css` — home page module
- `server.js` — search + health endpoints
- `db.js` — `searchNodes()` + `getNodeHealth()` queries
- `public/app.js` — default route = home
