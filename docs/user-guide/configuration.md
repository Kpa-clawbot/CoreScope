# Configuration

CoreScope is configured via `config.json` in the server's working directory. Copy `config.example.json` to get started.

## Core settings

| Field | Default | Description |
|-------|---------|-------------|
| `port` | `3000` | HTTP server port |
| `apiKey` | — | Secret key for admin API endpoints (POST/PUT routes) |
| `dbPath` | — | Path to SQLite database file (optional, defaults to `meshcore.db`) |

## MQTT

```json
"mqtt": {
  "broker": "mqtt://localhost:1883",
  "topic": "meshcore/+/+/packets"
}
```

The ingestor connects to this MQTT broker and subscribes to the topic pattern.

### Multiple MQTT sources

Use `mqttSources` for multiple brokers:

```json
"mqttSources": [
  {
    "name": "local",
    "broker": "mqtt://localhost:1883",
    "topics": ["meshcore/#"]
  },
  {
    "name": "remote",
    "broker": "mqtts://mqtt.example.com:8883",
    "username": "user",
    "password": "pass",
    "topics": ["meshcore/SJC/#"]
  }
]
```

## Branding

| Field | Description |
|-------|-------------|
| `branding.siteName` | Site title shown in the nav bar |
| `branding.tagline` | Subtitle on the home page |
| `branding.logoUrl` | URL to a custom logo image |
| `branding.faviconUrl` | URL to a custom favicon |

## Theme

Colors used throughout the UI. All values are hex color codes.

| Field | Description |
|-------|-------------|
| `theme.accent` | Primary accent color (links, buttons) |
| `theme.navBg` | Navigation bar background |
| `theme.navBg2` | Secondary nav background |
| `theme.statusGreen` | Healthy status color |
| `theme.statusYellow` | Degraded status color |
| `theme.statusRed` | Silent/error status color |

See [Customization](customization.md) for the full list — the theme customizer exposes every color.

## Node colors

Default marker colors by role:

```json
"nodeColors": {
  "repeater": "#dc2626",
  "companion": "#2563eb",
  "room": "#16a34a",
  "sensor": "#d97706",
  "observer": "#8b5cf6"
}
```

## Health thresholds

How long (in hours) before a node is marked degraded or silent:

| Field | Default | Description |
|-------|---------|-------------|
| `healthThresholds.infraDegradedHours` | `24` | Repeaters/rooms → degraded after this many hours |
| `healthThresholds.infraSilentHours` | `72` | Repeaters/rooms → silent after this many hours |
| `healthThresholds.nodeDegradedHours` | `1` | Companions/others → degraded |
| `healthThresholds.nodeSilentHours` | `24` | Companions/others → silent |

## Retention

| Field | Default | Description |
|-------|---------|-------------|
| `retention.nodeDays` | `7` | Nodes not seen in N days move to inactive |
| `retention.packetDays` | `30` | Packets older than N days are deleted daily |

> **Note:** Lowering retention does **not** immediately shrink the database file.
> SQLite marks deleted pages as free but does not return them to the filesystem
> unless [incremental auto-vacuum](database.md) is enabled. New databases created
> after v0.x.x have auto-vacuum enabled automatically. Existing databases require
> a one-time migration — see the [Database](database.md) guide.

## Database

| Field | Default | Description |
|-------|---------|-------------|
| `db.vacuumOnStartup` | `false` | Run a one-time full `VACUUM` on startup to enable incremental auto-vacuum (blocks for minutes on large DBs) |
| `db.incrementalVacuumPages` | `1024` | Free pages returned to the OS after each retention reaper cycle |

See [Database](database.md) for details on SQLite auto-vacuum, WAL, and manual maintenance.
See [#919](https://github.com/Kpa-clawbot/CoreScope/issues/919) for background.

## Channel decryption

| Field | Description |
|-------|-------------|
| `channelKeys` | Object of `"label": "hex-key"` pairs for decrypting channel messages |
| `hashChannels` | Array of channel names (e.g., `"#LongFast"`) to match by hash |

See [Channels](channels.md) for details.

## Map defaults

```json
"mapDefaults": {
  "center": [37.45, -122.0],
  "zoom": 9
}
```

Initial map center and zoom level.

## Map tiles

Which maps visitors can pick (street, gray, topographic, satellite, vector), the light and dark defaults, the Carto key, the hillshade overlay and your own tile servers are all set under `map.tiles`:

```json
"map": {
  "tiles": {
    "darkDefault": "carto-dark",
    "lightDefault": "carto-light",
    "providers": {
      "carto": { "key": "YOUR_CARTO_KEY" },
      "opentopomap": { "enabled": true },
      "openfreemap": { "enabled": true }
    },
    "custom": []
  }
}
```

Everything under `map.tiles` is sent to every visitor's browser, so only put keys there that are meant to be public (domain-restricted). Of the built-in groups only `carto` and `esri` are on unless you say otherwise; every other group (including the newer `eox`, `osmfr`, `openfreemap`, `versatiles` and `hillshade`) needs `"enabled": true`. The full list of maps, their licences and examples for custom and self-hosted tile servers are in [Map Tile Providers](../deployment.md#map-tile-providers).

## Ducting

```json
"ducting": {
  "enabled": true,
  "model": "gfs",
  "refreshMinutes": 360
}
```

Turns on the tropospheric ducting overlay on the Map, Live and Repeater Planner pages. It is off by default and, when on, the server downloads a weather forecast from Open-Meteo. Running your own Open-Meteo and pointing `baseUrl` at it is recommended but not required. The public API works for a non-commercial site, and the call budget keeps it within the free limits. A self-hosted instance has no call limits, so the budget is switched off. Self-hosting is required only for commercial use, since the free public service is for non-commercial use only. By default it forecasts the bounding box of your nodes plus `marginDeg` (5 degrees), up to `maxPoints` (450) cells. Only nodes heard (`last_seen`) in the last 14 days count, and with 5 or more of them the box ignores nodes more than 500 km (or 4 times the median distance, if that is larger) from the nodes' median position, so a few far-away nodes cannot stretch it (at most 10% of the nodes, or one node in a mesh of fewer than 20, are left out, the farthest first: the rest of a larger far-away group stays in the box as a possible second area); each refresh logs the box as `[ducting] grid: box S..N, W..E (k nodes, t trimmed), step X deg, C cells`. The grid keeps 0.5 degree cells only if the box plus margin fits in `maxPoints` cells (nodes spread over at most about half a degree each way with the defaults); otherwise the cells grow to 1 degree, or coarser. On a public instance, set `area`: the grid then stays where you put it, whatever positions nodes advertise. A proxy in front of the public Open-Meteo API must keep the host name `api.open-meteo.com` in `baseUrl`, or it counts as self-hosted and the call budget is switched off. On high terrain, where the forecast reaches less than 1.5 km above the ground, the map uses the mean gradient over the depth available down to 1 km; ground above roughly 2,000 m (Mexico City, for example) has less than that and gets no colour. In `GET /api/ducting`, `n` = 0 means no data. Against the public API the first fetch waits a random 1 to 5 minutes after start; Open-Meteo's free limits count per IP address, so instances on one host share them. Every key (`model`, `area`, `marginDeg`, `gridStepDeg`, `maxPoints`, `refreshMinutes`, `forecastHours`, `baseUrl`), the call budget (which applies to the public API only; a self-hosted `baseUrl` has no call budget) and self-hosting are described in [Tropospheric ducting overlay](../deployment.md#tropospheric-ducting-overlay).

## Regions

```json
"regions": {
  "SJC": "San Jose, US",
  "SFO": "San Francisco, US"
}
```

Named regions for the region filter dropdown. The `defaultRegion` field sets which region is selected by default.

## Cache TTL

All values in seconds. Controls how long the server caches API responses:

```json
"cacheTTL": {
  "stats": 10,
  "nodeList": 90,
  "nodeDetail": 300,
  "analyticsRF": 1800
}
```

Lower values = fresher data but more server load.

## Packet store

| Field | Default | Description |
|-------|---------|-------------|
| `packetStore.maxMemoryMB` | `1024` | Maximum RAM for in-memory packet store |
| `packetStore.estimatedPacketBytes` | `450` | Estimated bytes per packet (for memory budgeting) |
| `packetStore.retentionHours` | `0` | Only load packets younger than N hours on startup and keep them in memory. **Set this on any instance with a large DB.** `0` = unlimited (loads full DB history — causes OOM on cold start when the DB has hundreds of thousands of paths). Recommended: same as `retention.packetDays × 24` (e.g. `168` for 7 days). |

> **Warning:** Leaving `retentionHours` at `0` on a large database will cause the server to OOM-kill itself on every cold start. The full packet history is loaded into the subpath index at startup; a DB with ~280K paths produces ~13M index entries before the process is killed.

## Timestamps

| Field | Default | Description |
|-------|---------|-------------|
| `timestamps.defaultMode` | `"ago"` | Display mode: `"ago"` (relative) or `"absolute"` |
| `timestamps.timezone` | `"local"` | `"local"` or `"utc"` |
| `timestamps.formatPreset` | `"iso"` | Date format preset |

## Live map

| Field | Default | Description |
|-------|---------|-------------|
| `liveMap.propagationBufferMs` | `5000` | How long to buffer observations before animating |

## HTTPS

```json
"https": {
  "cert": "/path/to/cert.pem",
  "key": "/path/to/key.pem"
}
```

Provide cert and key paths to enable HTTPS.

## Geographic filtering

```json
"geo_filter": {
  "polygon": [[51.55, 3.80], [51.55, 5.90], [50.65, 5.90], [50.65, 3.80]],
  "bufferKm": 20
}
```

Restricts ingestion and API responses to nodes within the polygon plus a buffer margin. Remove the block to disable filtering. Nodes with no GPS fix always pass through.

Can also be configured live via the **🗺️ GeoFilter** tab in the Customizer (requires `apiKey`).

See [Geographic Filtering](geofilter.md) for the full guide.

## Location privacy

Opt-in: serve approximate node positions instead of exact ones (`locationPrivacy` block, secret via `CORESCOPE_LOCATION_SECRET`). See [Location Privacy](../location-privacy.md).

## Areas

```json
"areas": {
  "BAY": {
    "label": "Bay Area",
    "polygon": [[37.90, -122.55], [37.90, -121.75], [37.25, -121.75], [37.25, -122.55]]
  },
  "SJC": {
    "label": "San Jose",
    "latMin": 37.20, "latMax": 37.45, "lonMin": -122.05, "lonMax": -121.75
  }
}
```

GPS-based display filter. When configured, a pill bar appears in the dashboard letting users scope packets, nodes, and analytics to nodes physically located within a named area. Attribution is based on the transmitting node's own GPS coordinates — not the observer's location.

Each entry supports a `polygon` (array of `[lat, lon]` pairs) or a bounding box (`latMin`/`latMax`/`lonMin`/`lonMax`). Remove the block to disable the area filter UI.

See [Area Filter](area-filter.md) for the full guide including the visual builder tool.

## Home page

The `home` section customizes the onboarding experience. See `config.example.json` for the full structure including `steps`, `checklist`, and `footerLinks`.
