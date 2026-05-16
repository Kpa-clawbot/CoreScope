# Live Regional-Link Audit (May 16, 2026)

## Scope
- Baseline branch/test inventory
- Live path generation + rendering flow audit
- IATA region filter wiring audit for live view
- Regression coverage inventory for impossible-link suppression

## Findings
1. **Live link rendering path is frontend-driven in `public/live.js`**:
   - WebSocket/packet buffering enters `renderPacketTree`, then per-observation path extraction (`path_json` preferred), then hop resolution via `resolveHopPositions`, then plausibility guard (`pathPlausibilityReport`), then map draw/animation.
2. **IATA region filtering for live feed is present and wired**:
   - `/api/observers` + `/api/iata-coords` loaded by `loadObserverRegionContext`.
   - `observer_id -> IATA` map built by `buildObserverIataMap`.
   - Live packet gating uses `packetMatchesRegion(...)` before rendering packet groups.
3. **Cross-country impossible-link suppression is present**:
   - `pathPlausibilityReport` + `LIVE_RF_SEGMENT_MAX_KM` threshold in `public/live.js`.
   - Existing tests already assert rejection of impossible long-distance segments in `test-live.js` and Go-side neighbor graph geo sanity in `cmd/server/neighbor_graph_geo_test.go`.
4. **Coverage gap status**:
   - No immediate missing regression discovered for the specific IATA live filter wiring path; dedicated regressions already exist (`test-live-region-filter.js`, `test-issue-1136-observer-iata-map.js`, `test-issue-1136-live-region-e2e.js`).

## Performance safety note
- No runtime code changes were made in this audit pass, so no hot-path complexity change is introduced.
