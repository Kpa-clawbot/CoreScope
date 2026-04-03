# CoreScope v3.4 Release Notes

**The neighbor affinity release.** CoreScope now understands how nodes relate to each other — not just that they exist, but how strongly they're connected. This powers smarter hop resolution, richer node detail pages, and a new graph visualization in analytics.

---

## 🎯 Features

### Neighbor Affinity System (7 milestones)
A complete neighbor relationship engine, from backend graph building to frontend visualization:

- **Affinity graph builder** — computes neighbor relationships and connection strength from packet traffic (#507)
- **Affinity API endpoints** — REST endpoints to query neighbor data (#508)
- **Show Neighbors via affinity API** — the existing Show Neighbors feature now uses real affinity data instead of raw packet heuristics (#512, fixes #484)
- **Affinity-aware hop resolution** — hop resolver uses neighbor affinity to pick better paths (#511)
- **Node detail neighbors section** — dedicated neighbors panel on the node detail page (#510)
- **Affinity debugging tools** — inspect and troubleshoot affinity calculations (#521)
- **Neighbor graph visualization** — interactive neighbor graph in the analytics tab (#513)

### Customizer v2
- Event-driven state management replaces the old imperative approach — cleaner, more predictable theme/config updates (#503)

---

## 🐛 Bug Fixes

- **Stale parsed cache on observation packets** — observation packets now correctly invalidate the JSON parse cache (#505)
- **Null-guard rAF callbacks** — live page no longer crashes when `requestAnimationFrame` callbacks fire after cleanup (#506)
- **Customizer v2 phantom overrides** — fixed phantom config entries, missing defaults, and stale dark mode state (#520)
- **Neighbor affinity empty results** — fixed pubKey field name mismatch causing empty affinity graphs (#524)
- **Home defaults in server theme** — server-side theme config now includes home page defaults (#526)
- **Neighbor UI crash + dark mode** — fixed Show Neighbors crash and improved dark mode contrast (#527)
- **Home page steps + FAQ** — both steps AND FAQ now render correctly on the home page (#529)

---

## ⚡ Performance

- **Cached JSON.parse for packet data** — packet payloads are parsed once and cached, avoiding redundant `JSON.parse` calls on repeated access (#400)

---

## Known Limitations

- **Affinity graph scales with traffic volume** — networks with very low packet rates may show weak or missing neighbor relationships until enough data accumulates
- **Debugging tools are developer-facing** — the affinity debug panel (#521) is functional but not polished for end-user consumption
- **Customizer v2 migration** — custom themes saved under v1 may need to be re-applied after upgrade
