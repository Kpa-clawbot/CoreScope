# Configuration

CoreScope is configured via `config.json` in the server's working directory. Copy `config.example.json` to get started.

## Core settings

| Field | Default | Description |
|-------|---------|-------------|
| `port` | `3000` | HTTP server port |
| `apiKey` | — | Secret key for admin API endpoints (POST/PUT routes) |
| `dbPath` | — | Path to the SQLite database file (optional, defaults to `meshcore.db`). On PostgreSQL it is not the database, but it still anchors the directory for the file queues the server and ingestor share (geo-prune requests, the multibyte snapshot) and the default location of `users.db`: keep that directory mounted. |

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
> On SQLite, deleted pages are marked free but not returned to the filesystem
> unless [incremental auto-vacuum](database.md) is enabled (PostgreSQL reclaims space through
> autovacuum). New databases created
> after v0.x.x have auto-vacuum enabled automatically. Existing databases require
> a one-time migration — see the [Database](database.md) guide.

## Database

| Field | Default | Description |
|-------|---------|-------------|
| `db.driver` | `sqlite` | `sqlite` (aliases `sqlite3`, empty) or `postgres` (aliases `postgresql`, `pg`). Case-insensitive; anything else is a startup error. |
| `db.dsn` | — | PostgreSQL connection URL, e.g. `postgres://user:pass@host:5432/dbname?sslmode=require`. Required when `db.driver` is `postgres`. |
| `db.statementTimeoutSec` | `120` | PostgreSQL only: `statement_timeout` for the server's sessions, in seconds. `0` disables it; a negative value is a startup error. |
| `db.vacuumOnStartup` | `false` | **SQLite only.** Run a one-time full `VACUUM` on startup to enable incremental auto-vacuum (blocks for minutes on large DBs) |
| `db.incrementalVacuumPages` | `1024` | **SQLite only.** Free pages returned to the OS after each retention reaper cycle |
| `db.analysisLimit` | — | **SQLite only.** Ignored on PostgreSQL |

The environment variables `DB_DRIVER` and `DB_DSN` set the same two keys. Precedence follows
`DB_PATH`: in the ingestor the environment overrides `config.json`; in the server `config.json`
wins and the environment only fills keys that are empty there. The server and the ingestor need
different PostgreSQL roles (the server must be read-only), so give each its own DSN; in the
Docker image use `DB_DSN_INGESTOR` and `DB_DSN_SERVER`. Keep DSNs, which contain a password,
out of git and out of the data directory. See the [PostgreSQL guide](../postgresql.md).

`retention.hopIndexDays` is also SQLite-only; on PostgreSQL it is ignored with one warning.
Retention settings otherwise work on both backends.

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
