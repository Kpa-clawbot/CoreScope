# Geofilter Docs Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve geofilter documentation at `/geofilter-docs.html` and update the builder's help link to point there instead of GitHub.

**Architecture:** Pure static HTML file placed in `public/` — the existing `http.FileServer` in `cmd/server/main.go` already serves all files in that directory with no changes needed. No server changes, no build steps, no JS dependencies.

**Tech Stack:** HTML, inline CSS only. No frameworks, no external scripts.

---

## File map

| Action | File | Purpose |
|---|---|---|
| Create | `public/geofilter-docs.html` | App-served docs page |
| Modify | `public/geofilter-builder.html` line 73 | Update help-bar link |
| Modify | `tools/geofilter-builder.html` line 73 | Update help-bar link (offline copy) |

Note: there are no automated tests for static HTML pages. Verification is manual (open in browser via the running server).

---

## Task 1: Create `public/geofilter-docs.html`

**Files:**
- Create: `public/geofilter-docs.html`

- [ ] **Step 1: Create the file with this exact content**

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>GeoFilter Docs — CoreScope</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; background: #1a1a2e; color: #e0e0e0; min-height: 100vh; display: flex; flex-direction: column; }
  header { padding: 12px 16px; background: #0f0f23; border-bottom: 1px solid #333; display: flex; align-items: center; gap: 16px; }
  header h1 { font-size: 1rem; font-weight: 600; color: #4a9eff; }
  #back-link { font-size: 0.8rem; color: #4a9eff; text-decoration: none; white-space: nowrap; }
  #back-link:hover { text-decoration: underline; }
  main { flex: 1; max-width: 800px; margin: 0 auto; padding: 32px 24px; width: 100%; }
  h2 { font-size: 1.1rem; font-weight: 600; color: #4a9eff; margin: 32px 0 12px; border-bottom: 1px solid #222; padding-bottom: 6px; }
  h2:first-of-type { margin-top: 0; }
  h3 { font-size: 0.95rem; font-weight: 600; color: #c0c0c0; margin: 20px 0 8px; }
  p { font-size: 0.9rem; line-height: 1.6; color: #ccc; margin-bottom: 10px; }
  ul { padding-left: 20px; margin-bottom: 10px; }
  li { font-size: 0.9rem; line-height: 1.7; color: #ccc; }
  code { font-family: monospace; font-size: 0.85rem; color: #7ec8e3; background: #111; border: 1px solid #333; border-radius: 3px; padding: 1px 5px; }
  pre { background: #111; border: 1px solid #333; border-radius: 6px; padding: 14px 16px; overflow-x: auto; margin: 10px 0 16px; }
  pre code { background: none; border: none; padding: 0; font-size: 0.82rem; color: #7ec8e3; }
  .note { background: #1a2a1a; border: 1px solid #2a4a2a; border-radius: 6px; padding: 10px 14px; margin: 12px 0; }
  .note p { color: #aaddaa; margin: 0; }
  .warn { background: #2a1a0a; border: 1px solid #5a3a0a; border-radius: 6px; padding: 10px 14px; margin: 12px 0; }
  .warn p { color: #ddbb88; margin: 0; }
  table { width: 100%; border-collapse: collapse; margin: 10px 0 16px; font-size: 0.88rem; }
  th { background: #0f0f23; color: #888; font-weight: 500; text-align: left; padding: 8px 12px; border: 1px solid #333; }
  td { padding: 8px 12px; border: 1px solid #222; color: #ccc; vertical-align: top; }
  td code { font-size: 0.82rem; }
</style>
</head>
<body>

<header>
  <a href="/geofilter-builder.html" id="back-link">← GeoFilter Builder</a>
  <h1>GeoFilter Docs</h1>
</header>

<main>

<h2>How it works</h2>
<p>Geographic filtering restricts which nodes are ingested and returned in API responses. It operates at two levels:</p>
<ul>
  <li><strong>Ingest time</strong> — ADVERT packets carrying GPS coordinates are rejected by the ingestor if the node falls outside the configured area. The node never reaches the database.</li>
  <li><strong>API responses</strong> — Nodes already in the database are filtered from the <code>/api/nodes</code> response if they fall outside the area. This covers nodes ingested before the filter was configured.</li>
</ul>
<div class="note"><p>Nodes with no GPS fix (<code>lat=0, lon=0</code> or missing coordinates) always pass the filter regardless of configuration.</p></div>

<h2>Configuration</h2>
<p>Add a <code>geo_filter</code> block to <code>config.json</code>:</p>
<pre><code>"geo_filter": {
  "polygon": [
    [51.55, 3.80],
    [51.55, 5.90],
    [50.65, 5.90],
    [50.65, 3.80]
  ],
  "bufferKm": 20
}</code></pre>
<table>
  <thead><tr><th>Field</th><th>Type</th><th>Description</th></tr></thead>
  <tbody>
    <tr><td><code>polygon</code></td><td><code>[[lat, lon], ...]</code></td><td>Array of at least 3 coordinate pairs defining the boundary</td></tr>
    <tr><td><code>bufferKm</code></td><td>number</td><td>Extra distance (km) around the polygon edge that is also accepted. <code>0</code> = exact boundary</td></tr>
  </tbody>
</table>
<p>Both the server and the ingestor read <code>geo_filter</code> from <code>config.json</code>. Restart both after changing this section.</p>
<p>To disable filtering entirely, remove the <code>geo_filter</code> block.</p>

<h2>Coordinate ordering</h2>
<div class="warn"><p><strong>Important:</strong> Coordinates are <code>[lat, lon]</code> — latitude first, longitude second. This is the opposite of GeoJSON, which uses <code>[lon, lat]</code>. Swapping them will place your polygon in the wrong location.</p></div>

<h2>Multi-polygon</h2>
<p>Only a single polygon is supported. If your deployment area consists of multiple disconnected regions, draw a single convex hull that covers all of them, or use the largest region with a generous <code>bufferKm</code> value.</p>

<h2>Examples</h2>
<h3>Belgium (bounding rectangle)</h3>
<pre><code>"geo_filter": {
  "polygon": [
    [51.55, 3.80],
    [51.55, 5.90],
    [50.65, 5.90],
    [50.65, 3.80]
  ],
  "bufferKm": 20
}</code></pre>
<h3>Irregular shape</h3>
<pre><code>"geo_filter": {
  "polygon": [
    [51.10, 3.70],
    [51.55, 4.20],
    [51.30, 5.10],
    [50.80, 5.50],
    [50.50, 4.80],
    [50.70, 3.90]
  ],
  "bufferKm": 10
}</code></pre>

<h2>Legacy bounding box</h2>
<p>An older bounding box format is also supported as a fallback when no <code>polygon</code> is present:</p>
<pre><code>"geo_filter": {
  "latMin": 50.65,
  "latMax": 51.55,
  "lonMin": 3.80,
  "lonMax": 5.90
}</code></pre>
<p>Prefer the polygon format — it supports irregular shapes and the <code>bufferKm</code> margin.</p>

<h2>Cleaning up historical nodes</h2>
<p>The ingestor prevents new out-of-bounds nodes from being ingested, but does not retroactively remove nodes stored before the filter was configured. Use the prune script for that:</p>
<pre><code># Dry run — shows what would be deleted without making any changes
python3 scripts/prune-nodes-outside-geo-filter.py --dry-run

# Default paths: /app/data/meshcore.db and /app/config.json
python3 scripts/prune-nodes-outside-geo-filter.py

# Custom paths
python3 scripts/prune-nodes-outside-geo-filter.py /path/to/meshcore.db \
  --config /path/to/config.json

# In Docker — run inside the container
docker exec -it meshcore-analyzer \
  python3 /app/scripts/prune-nodes-outside-geo-filter.py --dry-run</code></pre>
<p>The script reads <code>geo_filter.polygon</code> and <code>geo_filter.bufferKm</code> from config, lists nodes that fall outside, then asks for <code>yes</code> confirmation before deleting. Nodes without coordinates are always kept.</p>
<p>This is a one-time migration tool — run it once after first configuring <code>geo_filter</code> to clean up pre-filter data.</p>

</main>
</body>
</html>
```

- [ ] **Step 2: Commit**

```bash
git add public/geofilter-docs.html
git commit -m "feat(geofilter-docs): add app-served docs page (#820)"
```

---

## Task 2: Update help-bar link in both builder files

**Files:**
- Modify: `public/geofilter-builder.html` line 73
- Modify: `tools/geofilter-builder.html` line 73

- [ ] **Step 1: Replace the GitHub link in `public/geofilter-builder.html`**

Find line 73:
```html
  &nbsp;·&nbsp; <a href="https://github.com/Kpa-clawbot/CoreScope/blob/master/docs/user-guide/geofilter.md" target="_blank">Documentation ↗</a>
```

Replace with:
```html
  &nbsp;·&nbsp; <a href="/geofilter-docs.html">Documentation</a>
```

- [ ] **Step 2: Apply the same change to `tools/geofilter-builder.html`**

Find the same GitHub URL line in `tools/geofilter-builder.html` (also line 73) and apply the identical replacement:
```html
  &nbsp;·&nbsp; <a href="/geofilter-docs.html">Documentation</a>
```

Note: `tools/geofilter-builder.html` is the offline version opened directly as a file, so `/geofilter-docs.html` won't resolve without a server. This is acceptable — the offline tool is secondary; the `public/` version is the primary one served by the app.

- [ ] **Step 3: Commit**

```bash
git add public/geofilter-builder.html tools/geofilter-builder.html
git commit -m "fix(geofilter-builder): point docs link to local /geofilter-docs.html (#820)"
```

---

## Manual verification checklist

After both tasks are complete:

1. Start the server: `go run ./cmd/server`
2. Open `http://localhost:<port>/geofilter-docs.html` — page should load with dark theme, all sections visible
3. Open `http://localhost:<port>/geofilter-builder.html` — click "Documentation" in the help bar → should navigate to `/geofilter-docs.html` (same tab, no new window)
4. Check that the docs page "← GeoFilter Builder" link navigates back to `/geofilter-builder.html`
