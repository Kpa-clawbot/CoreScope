---
issue: 820
date: 2026-04-23
status: approved
---

# Geofilter Docs Page — Design Spec

## Goal

Serve geofilter documentation at `/geofilter-docs.html` so the builder's Help link points to a local, app-served page instead of raw GitHub markdown.

## Files changed

| File | Change |
|---|---|
| `public/geofilter-docs.html` | New — self-contained static docs page |
| `public/geofilter-builder.html` | Update help-bar link: GitHub URL → `/geofilter-docs.html` |
| `tools/geofilter-builder.html` | Same link update (mirrors the public version) |

No server changes required — `http.FileServer` already serves all files in `public/`.

## Approach

Self-contained static HTML. Content sourced from `docs/user-guide/geofilter.md` — no new content invented. The markdown remains the canonical reference; the HTML page is maintained in sync manually.

## Visual style

Dark theme matching the geofilter builder:
- Background: `#1a1a2e`
- Panel/header background: `#0f0f23`
- Accent: `#4a9eff`
- Code text: `#7ec8e3`
- Code background: `#111`
- No external JS dependencies

## Page structure

```
header
  ← GeoFilter Builder (link back to /geofilter-builder.html)
  title: GeoFilter Docs

scrollable content
  How it works
    - Ingest-time filter (ADVERT packets rejected if out of bounds)
    - API-response filter (nodes already in DB filtered from /api/nodes)
    - Nodes with no GPS fix always pass

  Configuration
    - polygon syntax: [[lat, lon], ...] — at least 3 pairs
    - bufferKm: extra margin in km around the polygon edge (0 = exact)
    - Full config.json example

  Coordinate ordering
    - Explicit note: [lat, lon] order — NOT [lon, lat] like GeoJSON
    - Common source of confusion; worth calling out

  Multi-polygon
    - Not supported — only a single polygon is accepted
    - Workaround: use a convex hull that covers all desired areas

  Examples
    - Belgium bounding rectangle
    - Irregular shape example

  Legacy bounding box
    - latMin/latMax/lonMin/lonMax format still accepted as fallback
    - Prefer polygon format

  Cleaning up historical nodes
    - prune-nodes-outside-geo-filter.py script
    - dry-run usage
    - Docker usage

  Disabling the filter
    - Remove the geo_filter block from config.json
```

## Acceptance criteria

- `/geofilter-docs.html` renders geofilter docs in the app's dark theme
- Builder's "Documentation ↗" link points to `/geofilter-docs.html` (no `target="_blank"`)
- Page covers: polygon syntax, lat/lon ordering, multi-polygon clarification, examples
- No server changes, no external JS dependencies
