# Area Filter

The area filter is a **GPS-based display filter** that scopes the dashboard to nodes within a defined geographic area. It is distinct from the [region filter](configuration.md#regions), which groups data by the observer's IATA location code.

## How it differs from the region filter

| | Region filter | Area filter |
|--|--|--|
| Based on | Observer's IATA code (from MQTT topic) | Transmitting node's own GPS coordinates |
| Set by | MQTT topic structure | Node's advertised GPS position |
| Use case | Separate traffic by observer location | Separate traffic by where nodes physically are |

Because the region filter is observer-based, a node broadcasting in San Jose can appear under "San Francisco" if a San Francisco observer hears it first. The area filter avoids this cross-region pollution by attributing packets to areas based on where the **sending node** is located.

## Configuration

Add an `areas` block to `config.json`:

```json
"areas": {
  "BAY": {
    "label": "Bay Area",
    "polygon": [
      [37.90, -122.55],
      [37.90, -121.75],
      [37.25, -121.75],
      [37.25, -122.55]
    ]
  },
  "SJC": {
    "label": "San Jose",
    "latMin": 37.20,
    "latMax": 37.45,
    "lonMin": -122.05,
    "lonMax": -121.75
  }
}
```

Each entry defines one area. Two shape formats are supported:

| Format | Fields | Notes |
|--------|--------|-------|
| Polygon | `polygon: [[lat, lon], ...]` | At least 3 points. Supports irregular shapes. |
| Bounding box | `latMin`, `latMax`, `lonMin`, `lonMax` | Simpler rectangles. |

The `label` field controls what appears in the filter pill bar in the UI.

Remove the `areas` block to disable the area filter entirely — the pill bar disappears automatically.

### Nodes without GPS

Nodes with no GPS fix (`lat=0, lon=0` or missing coordinates) are always allowed through regardless of the active area filter. This prevents nodes that haven't advertised a position yet from being hidden.

## Using the area filter

When `areas` is configured, a pill bar appears below the main navigation on:

- **Packets** — shows only packets where the transmitting node is within the selected area
- **Nodes** — shows only nodes whose GPS position falls within the area
- **Analytics** — all charts and tables are scoped to nodes in the area
- **Channels** — channel message list is scoped to the area

Click a pill to select that area. Click again (or click the active pill) to deselect. Only one area can be active at a time. The selection is saved in `localStorage` and persists across page reloads.

## Area Map tool

The Area Map is a visual debug and builder tool served at `/area-map.html` on your CoreScope instance.

### Viewing existing areas

1. Open `/area-map.html` in your browser.
2. Leave the server field empty (uses the current origin) and click **Load**.
3. Each configured area is drawn as a colored polygon on the map.
4. Colored dots show nodes that the server returns when that area is selected — this is what the filter actually returns, so you can verify the boundaries are correct.
5. Use the checkboxes in the sidebar to toggle individual areas on or off.
6. Enable **All nodes (grey)** to overlay every node with GPS — nodes outside all areas appear grey, making it easy to spot incorrectly excluded or included nodes.

### Drawing a new area

1. Fill in **Key** (e.g. `ANT`) and **Label** (e.g. `Antwerp`) in the sidebar.
2. Click **Draw** — the cursor turns to a crosshair.
3. Click on the map to add polygon vertices. The polygon updates after each click.
4. Use **↩ Undo** to remove the last point, **✕ Clear** to start over.
5. When satisfied, the JSON snippet in the output box is ready to copy:

```json
"ANT": {
  "label": "Antwerp",
  "polygon": [[51.28, 4.20], [51.28, 4.55], [51.10, 4.55], [51.10, 4.20]]
}
```

6. Paste this entry into the `areas` object in `config.json` and restart the server.

## API

```
GET /api/config/areas
```

Returns the list of configured area keys and labels (no polygon data). Used by the frontend to build the pill bar.

```
GET /api/config/areas/polygons
```

Returns full area definitions including polygon coordinates. Used by the Area Map tool.

Both endpoints require no authentication.
