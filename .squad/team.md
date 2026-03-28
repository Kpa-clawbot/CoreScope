# Squad — CoreScope

## Project Context

**Project:** CoreScope — Real-time LoRa mesh packet analyzer
**Stack:** Node.js 18+, Express 5, SQLite (better-sqlite3), vanilla JS frontend, Leaflet maps, WebSocket (ws), MQTT (mqtt)
**User:** User
**Description:** Self-hosted alternative to analyzer.letsmesh.net. Ingests MeshCore mesh network packets via MQTT, decodes with custom parser (decoder.js), stores in SQLite with in-memory indexing (packet-store.js), and serves a rich SPA with live visualization, packet analysis, node analytics, channel chat, observer health, and theme customizer. ~18K lines, 14 test files, 85%+ backend coverage. Production at v2.6.0.

**Key files:** server.js (Express API + MQTT + WebSocket), decoder.js (packet parser), packet-store.js (in-memory store), db.js (SQLite), server-helpers.js (shared helpers), public/ (22 frontend modules)

**Rules:** Read AGENTS.md before any work. No commit without tests. Cache busters always bumped. Plan before implementing. One commit per logical change. Explicit git add only.

## Members

| Name | Role | Model | Emoji |
|------|------|-------|-------|
| Kobayashi | Lead | auto | 🏗️ |
| Hicks | Backend Dev | auto | 🔧 |
| Newt | Frontend Dev | auto | ⚛️ |
| Bishop | Tester | auto | 🧪 |
| Hudson | DevOps Engineer | auto | ⚙️ |
| Ripley | Support Engineer | auto | 🛟 |
| Scribe | Session Logger | claude-haiku-4.5 | 📋 |
| Ralph | Work Monitor | — | 🔄 |
