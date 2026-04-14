# Geographic Filtering

CoreScope supports geographic filtering to restrict which nodes are ingested and returned in API responses. This is useful for public-facing deployments that should only show activity in a specific region.

## How it works

Geographic filtering operates at two levels:

- **Ingest time** — ADVERT packets carrying GPS coordinates are rejected by the ingestor if the node falls outside the configured area. The node never reaches the database.
- **API responses** — Nodes already in the database are filtered from the `/api/nodes` response if they fall outside the area. This covers nodes ingested before the filter was configured.

Nodes with no GPS fix (`lat=0, lon=0` or missing coordinates) always pass the filter regardless of configuration.

## Configuration

Add a `geo_filter` block to `config.json`:

```json
"geo_filter": {
  "polygon": [
    [51.55, 3.80],
    [51.55, 5.90],
    [50.65, 5.90],
    [50.65, 3.80]
  ],
  "bufferKm": 20
}
```

| Field | Type | Description |
|-------|------|-------------|
| `polygon` | `[[lat, lon], ...]` | Array of at least 3 coordinate pairs defining the boundary |
| `bufferKm` | number | Extra distance (km) around the polygon edge that is also accepted. `0` = exact boundary |

Both the server and the ingestor read `geo_filter` from `config.json`. Restart both after changing this section.

To disable filtering entirely, remove the `geo_filter` block.

### Legacy bounding box

An older bounding box format is also supported as a fallback when no `polygon` is present:

```json
"geo_filter": {
  "latMin": 50.65,
  "latMax": 51.55,
  "lonMin": 3.80,
  "lonMax": 5.90
}
```

Prefer the polygon format — it supports irregular shapes and the `bufferKm` margin.

## API endpoint

The current geo filter configuration is exposed at:

```
GET /api/config/geo-filter
```

The frontend reads this endpoint to display the active filter. No authentication is required (the endpoint returns config, not private data).

## GeoFilter Builder

The simplest way to create a polygon is the included visual builder:

**File:** `tools/geofilter-builder.html`

Open it directly in a browser — it runs entirely client-side, no server required:

```bash
# From the project root
open tools/geofilter-builder.html          # macOS
xdg-open tools/geofilter-builder.html     # Linux
start tools/geofilter-builder.html        # Windows
```

**Workflow:**

1. The map opens centered on Belgium by default. Navigate to your region.
2. Click on the map to add polygon vertices. Each click adds a numbered point.
3. Add at least 3 points to form a closed polygon.
4. Adjust **Buffer km** (default 20) to add a margin around the polygon edge.
5. The generated JSON block appears at the bottom of the page — copy it directly into `config.json`.
6. Use **↩ Undo** to remove the last point, **✕ Clear** to start over.

The output is a complete `{ "geo_filter": { ... } }` block ready to paste into `config.json`.

## Cleaning up historical nodes

The ingestor prevents new out-of-bounds nodes from being ingested, but it does not retroactively remove nodes that were stored before the filter was configured. For that, use the prune script.

**File:** `scripts/prune-nodes-outside-geo-filter.py`

```bash
# Dry run — shows what would be deleted without making any changes
python3 scripts/prune-nodes-outside-geo-filter.py --dry-run

# Default paths: /app/data/meshcore.db and /app/config.json
python3 scripts/prune-nodes-outside-geo-filter.py

# Custom paths
python3 scripts/prune-nodes-outside-geo-filter.py /path/to/meshcore.db \
  --config /path/to/config.json

# In Docker — run inside the container
docker exec -it meshcore-analyzer \
  python3 /app/scripts/prune-nodes-outside-geo-filter.py --dry-run
```

The script reads `geo_filter.polygon` and `geo_filter.bufferKm` from config, lists the nodes that fall outside, then asks for `yes` confirmation before deleting. Nodes without coordinates are always kept.

This is a **one-time migration tool** — run it once after first configuring `geo_filter` to clean up pre-filter data. The ingestor handles all subsequent filtering automatically at ingest time.
