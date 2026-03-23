# AGENTS.md — MeshCore Analyzer

Guide for AI agents working on this codebase. Read this before writing any code.

## Architecture

Single Node.js server + static frontend. No build step. No framework. No bundler.

```
server.js          — Express API + MQTT ingestion + WebSocket broadcast
decoder.js         — MeshCore packet parser (header, path, payload, adverts)
packet-store.js    — In-memory packet store + query engine (backed by SQLite)
db.js              — SQLite schema + prepared statements
public/            — Frontend (vanilla JS, one file per page)
  app.js           — SPA router, shared globals, theme loading
  roles.js         — ROLE_COLORS, TYPE_COLORS, health thresholds, shared helpers
  nodes.js         — Nodes list + side pane + full detail page
  map.js           — Leaflet map with markers, legend, filters
  packets.js       — Packets table + detail pane + hex breakdown
  packet-filter.js — Wireshark-style filter engine (standalone, testable)
  customize.js     — Theme customizer panel (self-contained IIFE)
  analytics.js     — Analytics tabs (RF, topology, hash issues, etc.)
  channels.js      — Channel message viewer
  live.js          — Live packet feed + VCR mode
  home.js          — Home/onboarding page
  hop-resolver.js  — Client-side hop prefix → node name resolution
  style.css        — Main styles, CSS variables for theming
  live.css         — Live page styles
  home.css         — Home page styles
  index.html       — SPA shell, script/style tags with cache busters
```

### Data Flow
1. MQTT brokers → server.js ingests packets → decoder.js parses → packet-store.js stores in memory + SQLite
2. WebSocket broadcasts new packets to connected browsers
3. Frontend fetches via REST API, filters/sorts client-side

## Rules — Read These First

### 1. No commit without tests
Every change that touches logic MUST have unit tests. Run `node test-packet-filter.js && node test-aging.js` before pushing. If you add new logic, add tests to the appropriate test file or create a new one. No exceptions.

### 2. No commit without browser validation
After pushing, verify the change works in an actual browser. Use `browser profile=openclaw` against the running instance. Take a screenshot if the change is visual. If you can't validate it, say so — don't claim it works.

### 3. Cache busters — ALWAYS bump them
Every time you change a `.js` or `.css` file in `public/`, bump the cache buster in `index.html`. This has caused 7 separate production regressions. Use:
```bash
NEWV=$(date +%s) && sed -i "s/v=[0-9]*/v=$NEWV/g" public/index.html
```
Do this in the SAME commit as the code change, not as a follow-up.

### 4. Verify API response shape before building UI
Before writing client code that consumes an API endpoint, check what the endpoint ACTUALLY returns. Use `curl` or check the server code. Don't assume fields exist — grouped packets (`groupByHash=true`) have different fields than raw packets. This has caused multiple breakages.

### 5. Plan before implementing
Present a plan with milestones to the human. Wait for sign-off before starting. The plan must include:
- What changes in each milestone
- What tests will be written
- What browser validation will be done
- What config/customizer implications exist (see rule 8)

Do NOT start coding until the human says "go" or "start" or equivalent.

### 6. One commit per logical change
Don't push half-finished work. Don't push "let me try this" experiments. Get it right locally, test it, THEN push ONE commit. The QR overlay took 6 commits because each one was pushed without looking at the result. That's 6x the review burden for one visual change.

### 7. Understand before fixing
When something doesn't work as expected, INVESTIGATE before "fixing." Read the firmware source. Check the actual data. Understand WHY before changing code. The hash_size saga (21 commits) happened because we guessed at behavior instead of reading the MeshCore source.

### 8. Config values belong in the customizer eventually
If a feature introduces configurable values (thresholds, timeouts, display limits), note in the plan that these should be exposed in the customizer in a later milestone. It's OK to hardcode initially, but don't forget — track it in the plan.

### 9. Explicit git add only
Never use `git add -A` or `git add .`. Always list files explicitly: `git add file1.js file2.js`. Review with `git diff --cached --stat` before committing.

### 10. Don't regress performance
The packets page loads 30K+ packets. Don't add per-packet API calls. Don't add O(n²) loops. Client-side filtering is preferred over server-side. If you need data from the server, fetch it once and cache it.

## MeshCore Protocol

### Advert Flags
The flags byte lower nibble is a **4-bit enum type**, NOT individual bit flags:
- 0 = none, 1 = companion, 2 = repeater, 3 = room, 4 = sensor
- Upper bits: 0x10 = hasLocation, 0x80 = hasName
- `flags & 0x0F` gives the type. `flags & 0x04` does NOT mean "room."

### Hash Sizes
- Path byte bits 7-6 encode hash size: `((pathByte >> 6) & 0x3) + 1` → 1-3 bytes
- Firmware bug pre-1.14.1: automatic adverts could emit wrong hash size (0x00 path byte)
- Use newest advert's hash size as current. Track all sizes to detect flip-flopping.

### Node Behavior
- Repeaters/rooms flood-advertise every 12-24h (configurable)
- Companions only advertise when user initiates — going silent is NORMAL
- Room servers use ADV_TYPE 3 but have same advert interval as repeaters

### Route Types
- 0: TRANSPORT_FLOOD, 1: FLOOD, 2: DIRECT, 3: TRANSPORT_DIRECT

## Frontend Conventions

### Theming
All colors MUST use CSS variables. Never hardcode `#hex` values outside of `:root` definitions. The customizer controls colors via `THEME_CSS_MAP` in customize.js. If you add a new color, add it as a CSS variable and map it in the customizer.

### Shared Helpers (roles.js)
- `getNodeStatus(role, lastSeenMs)` → 'active' | 'stale'
- `getHealthThresholds(role)` → `{ staleMs, degradedMs, silentMs }`
- `ROLE_COLORS`, `ROLE_STYLE`, `TYPE_COLORS` — global color maps

### Shared Helpers (nodes.js)
- `getStatusInfo(n)` → `{ status, statusLabel, explanation, roleColor, ... }`
- `renderNodeBadges(n, roleColor)` → HTML string
- `renderStatusExplanation(n)` → HTML string

### last_heard vs last_seen
- `last_seen` = DB timestamp, only updates on adverts/direct upserts
- `last_heard` = from in-memory packet store, updates on ALL traffic
- Always prefer `n.last_heard || n.last_seen` for display and status calculation

### Packet Filter (packet-filter.js)
Standalone module. No dependencies on app globals (copies what it needs). Testable in Node.js:
```bash
node test-packet-filter.js
```
Uses firmware-standard type names (GRP_TXT, TXT_MSG, REQ) with aliases for convenience.

## Testing

### Unit Tests
```bash
node test-packet-filter.js   # 62 tests — filter engine
node test-aging.js            # 29 tests — node aging system
```
Run BOTH before every push. Add tests for new logic.

### Browser Validation
After pushing, verify in the browser:
1. Does the page load without errors? (Check console)
2. Does the new feature work as intended?
3. Did you break anything else? (Quick check of adjacent features)
4. Does it work in both light and dark mode?

### What Needs Tests
- Parsers and decoders (packet-filter, decoder)
- Threshold/status calculations (aging, health)
- Data transformations (hash size computation, field resolvers)
- Anything with edge cases (null handling, boundary values)

## Common Pitfalls

| Pitfall | Times it happened | Prevention |
|---------|-------------------|------------|
| Forgot cache busters | 7 | Always bump in same commit |
| Grouped packets missing fields | 3 | curl the actual API first |
| last_seen vs last_heard mismatch | 4 | Always use `last_heard \|\| last_seen` |
| CSS selectors don't match SVG | 2 | Manipulate SVG in JS after generation |
| Feature built on wrong assumption | 5+ | Read source/data before coding |
| Pushed without testing | 5+ | Run tests + browser check every time |

## File Naming
- Tests: `test-{feature}.js` in repo root
- No build step, no transpilation — write ES2020 for server, ES5/6 for frontend (broad browser support)

## What NOT to Do
- Don't add npm dependencies without asking
- Don't create a build step
- Don't add framework abstractions (React, Vue, etc.)
- Don't hardcode colors — use CSS variables
- Don't make per-packet server API calls from the frontend
- Don't push without running tests
- Don't start implementing without plan approval
