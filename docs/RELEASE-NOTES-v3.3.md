# CoreScope v3.3 Release Notes

## Headline: Neighbor Affinity, Virtual Scroll, and a Performance Overhaul

v3.3 is the biggest release since launch — 50 PRs merged, touching every layer of the stack. The packets page now handles 30K+ rows without breaking a sweat, nodes show their RF neighbors, and the customizer got a complete rewrite.

---

## 🎯 New Features

- **Neighbor affinity graph** — see which nodes hear each other and how well, rendered as an interactive graph in analytics (#507, #508, #513)
- **Neighbors section on node detail page** — every node now shows its direct RF neighbors with signal quality (#510)
- **Affinity-aware hop resolution** — hop paths now resolve using real RF neighbor data instead of guessing (#511)
- **"Show direct neighbors" map filter** — click a node on the map to highlight only its neighbors (#480)
- **Customizer v2** — completely rewritten with event-driven state management, cleaner UX (#503)
- **Auto-inject cache busters at server startup** — no more manual `__BUST__` bumps or merge conflicts (#481)
- **Git-derived versioning** — version now comes from git tags, not package.json (#486)
- **manage.sh supports pinning to release tags** — deploy a specific version instead of always latest (#456)

## ⚡ Performance

- **Virtual scroll for packets table** — 30K+ packets render smoothly, no more DOM explosion (#402)
- **Debounced WebSocket renders** — coalesced updates prevent render storms on busy meshes (#402)
- **Cached JSON.parse results** — packet data parsed once, reused everywhere (#400)
- **In-place node upsert on ADVERT** — skip full reload when a node advertises (#461)
- **Map lookups replace linear scans** — observers.find() → O(1) Map lookups (#468)
- **Bounded memory growth on packets page** — eviction prevents unbounded DOM/data growth (#421)
- **Server-side collision analysis** — moved from client to server, fixes UI freezes on large meshes (#415)
- **Client-side "My Nodes" filter** — eliminated a server round-trip (#401)
- **Targeted analytics cache invalidation** — surgical invalidation instead of blowing the whole cache (#379)
- **Skip JSON parse when no pubkey fields present** — fast path for the common case (#499)
- **requestAnimationFrame replaces setInterval** — smoother live page animations, capped concurrency (#470)

## 🐛 Bug Fixes

- **Region filter was silently ignored on GetNodes** — nodes now actually filter by region (#497)
- **Region filtering missing from hash-collisions endpoint** — fixed (#477)
- **Haversine replaces Euclidean distance** in analytics hop distances — no more wildly wrong distances (#478)
- **Color-coded hex breakdown restored** in packet detail view (#500)
- **Channel hash displayed as hex** instead of confusing decimal (#471)
- **VCR timeline respects UTC/local timezone setting** (#459)
- **Observer last_seen updates on packet ingestion** — observers no longer appear stale (#479)
- **Packet timestamps used in bufferPacket** instead of arrival time — fixes time-travel bugs (#491)
- **Zero-hop adverts skipped** when checking node hash size (#493)
- **Null-guard fixes** — pathHops detail pane crash (#454), animLayer/liveAnimCount after destroy (#462), rAF callbacks in live page (#506)
- **Stale parsed cache cleared** on observation packets (#505)
- **Score/direction extracted from MQTT** with proper unit stripping and type safety (#371)
- **String/uint/uint64 type handling** in toFloat64 (#352)
- **Reset restores home steps** after SITE_CONFIG contamination (#460)
- **Duplicate return statement removed** in _cumulativeRowOffsets (#476)
- **Mutex added to PerfStats** — eliminates data races (#469)
- **Graceful container shutdown** for reliable deployments (#453)
- **Staging config always refreshed from prod** (#467)

## 🧪 Testing

- **100+ new app.js tests** — comprehensive SPA router coverage (#490)
- **71 new live.js tests** — live page fully covered (#489)
- **64 new packets.js tests** (#488)
- **nodes.js P0 coverage** — sort, status, timestamps, sync (#487)
- **Ingestor coverage 70% → 84%** (#492)
- **Playwright packets test stabilized** with explicit time window (#348)

## 🔧 Internal

- **Docker cleanup before CI build** — prevents disk space exhaustion (#473)

---

*50 PRs. Zero new dependencies. Still no build step.*
