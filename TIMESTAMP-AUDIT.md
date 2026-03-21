# Timestamp Audit — Device vs Server Timestamps

**Date:** 2026-03-21
**Problem:** MeshCore nodes have wildly inaccurate clocks (off by hours, or epoch-near values like `4`). Device-originated timestamps (`sender_timestamp`, `advert.timestamp`) are unreliable and should not be used for logic, sorting, or deduplication.

## Findings

### 1. `decoder.js:104-108` — Advert timestamp decoding
- **What:** Parses 4-byte LE unix timestamp from ADVERT packets into `timestamp` and `timestampISO`
- **Used for:** Decode output only. The value is stored in `decoded_json` but never used for node storage or sorting.
- **Risk:** None — `server.js:773` replaces it with `Date.now()` when creating bridge adverts, and `validateAdvert()` (line 314) explicitly skips timestamp validation.
- **Action:** None needed.

### 2. `decoder.js:158` — `sender_timestamp: result.data.timestamp`
- **What:** Extracts device timestamp from decrypted channel messages (GRP_TXT)
- **Used for:** Passed through to API responses
- **Risk:** Low — just decoding. But downstream usage matters (see #3).
- **Action:** None needed at decode layer.

### 3. ⚠️ `server.js:2214` — **FIXED** — Dedupe key used `sender_timestamp`
- **What:** `const ts = decoded.sender_timestamp || pkt.timestamp; const dedupeKey = sender:${ts}`
- **Risk:** **HIGH** — If device clock is wrong, messages could fail to deduplicate (different sender_timestamp for same message seen by multiple observers) or incorrectly collide (same sender_timestamp for different messages).
- **Fix:** Changed dedupe key to use `pkt.hash` instead of any timestamp: `const dedupeKey = sender:${pkt.hash}`. The packet hash is the correct deduplication identifier.

### 4. `server.js:2238` — `sender_timestamp` in API response
- **What:** Returns `sender_timestamp` as a field in channel message API responses
- **Used for:** Informational display only (frontend shows it as metadata)
- **Risk:** Low — labeled as `sender_timestamp`, not used for sorting or logic
- **Action:** None needed. Keeping it for debugging/informational purposes is fine.

### 5. `public/packets.js:1047` — Hex viewer display
- **What:** Shows `sender_timestamp` in packet hex breakdown
- **Used for:** Developer-facing hex analysis display
- **Risk:** None — purely informational in hex decoder view
- **Action:** None needed.

### 6. `public/channels.js:454` — Stores `sender_timestamp` on live message objects
- **What:** Stores `sender_timestamp` on WebSocket message objects
- **Used for:** Not actively used for sorting or display — `timestamp` (server) is used for all time displays
- **Risk:** None
- **Action:** None needed.

### 7. `decoder.js:314` — validateAdvert skips timestamp
- **What:** Comment explicitly says "timestamp: decoded but not currently used for node storage — skip validation"
- **Risk:** None — already properly handled
- **Action:** None needed.

## All other `timestamp` references

All other `.timestamp` references in the codebase (`pkt.timestamp`, `p.timestamp`, `row.timestamp`, etc.) refer to the **server observation timestamp** — the ISO string set at packet ingestion time (`db.js:222`, `db.js:243`, `server.js:682/784/817/863`). These are reliable and correctly used for sorting, filtering, display, and analytics.

## Summary

| File | Line | Field | Usage | Risk | Action |
|------|------|-------|-------|------|--------|
| decoder.js | 104-108 | advert.timestamp | Decode | None | — |
| decoder.js | 158 | sender_timestamp | Decode | None | — |
| server.js | 2214 | sender_timestamp | **Dedupe key** | **HIGH** | **FIXED** |
| server.js | 2238 | sender_timestamp | API response | Low | — |
| public/packets.js | 1047 | sender_timestamp | Hex display | None | — |
| public/channels.js | 454 | sender_timestamp | Store only | None | — |
