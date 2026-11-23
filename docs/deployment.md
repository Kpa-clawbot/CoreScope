# CoreScope Deployment Guide

Comprehensive guide to deploying and operating CoreScope. For a quick start, see [DEPLOY.md](../DEPLOY.md).

## Table of Contents

- [System Requirements](#system-requirements)
- [Deployment modes](#deployment-modes)
- [Docker Deployment](#docker-deployment)
- [Configuration Reference](#configuration-reference)
- [MQTT Setup](#mqtt-setup)
- [TLS / HTTPS](#tls--https)
- [Behind a CDN (Cloudflare, Fastly)](#behind-a-cdn-cloudflare-fastly)
- [Monitoring & Health Checks](#monitoring--health-checks)
- [Backup & Restore](#backup--restore)
- [Troubleshooting](#troubleshooting)

---

## System Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| RAM | 256 MB | 512 MB+ |
| Disk | 500 MB (image + DB) | 2 GB+ for long-term data |
| CPU | 1 core | 2+ cores |
| Architecture | `linux/amd64`, `linux/arm64` | — |
| Docker (run the published image) | 20.10+ | Latest stable |
| Docker (build the image yourself) | 23.0+, or 20.10+ with `DOCKER_BUILDKIT=1` | Latest stable |

The Dockerfile uses BuildKit features (`# syntax=` and `RUN --mount`). BuildKit is the default builder from Docker 23.0; on 20.10 to 22.x the legacy builder is used unless `DOCKER_BUILDKIT=1` is set, and it rejects `RUN --mount`.

CoreScope runs well on Raspberry Pi 4/5 (ARM64). The Go server uses ~300 MB RAM for 56K+ packets.

---

## Deployment modes

The UI is static files (`public/`) that call the server's API. The three modes
differ only in what serves those files. The server container is the same image
in all three, and it still serves the classic UI itself.

| Mode | The UI is served by | API | Server CORS setting | Logins |
|---|---|---|---|---|
| [All-in-one container](#all-in-one-container) | the server container | same origin | none | yes |
| [Split containers](#split-containers) | a web container (`Dockerfile.web`) that forwards the API | same origin | none | yes |
| [Static host or CDN](#static-host-or-cdn) | Cloudflare Pages, a CDN or any web server | another origin | `CORS_ALLOWED_ORIGINS` | no, links to the classic UI |

### All-in-one container

The default, and what the rest of this guide describes: one image runs the
server, the ingestor and, optionally, Mosquitto and Caddy. The server serves the
UI from `/app/public` and the API on one origin. See
[Docker Deployment](#docker-deployment).

### Split containers

`docker-compose.split.yml` runs the server image unchanged next to a web
container built from `Dockerfile.web`. The web container is Caddy: it serves the
UI, packaged by `scripts/static/build.sh` when the image is built, and forwards
`/api/*` and `/ws` to the server. The browser sees one origin, so the server
needs no CORS setting. Logins work as on the classic UI when account
configuration uses the public web origin, as described below.

```bash
CORESCOPE_IMAGE=ghcr.io/kpa-clawbot/corescope:edge \
  docker compose -f docker-compose.split.yml up -d --build
```

| Variable | Default | |
|---|---|---|
| `HTTP_PORT`, `HTTPS_PORT` | `80`, `443` | Published by the web container; 443 over TCP and UDP (HTTP/3) |
| `SITE_ADDRESS` | `:80` | A domain name turns on Caddy's automatic HTTPS and HTTP/3. Keep the default ports then: certificate issuance and the HTTP/3 advertisement both assume 80 and 443 |
| `CORESCOPE_IMAGE` | none, required | The server image of the same version as the checkout the web image is built from: `:edge` on `master`, `:vX.Y.Z` on a release tag |
| `DATA_DIR` | `./data` | The server's data directory |
| `GIT_COMMIT` | the build time | The UI's cache buster |
| `CHANNEL` | `static` | `beta`, `next` or `dev` shows a badge |
| `MQTT_BIND`, `MQTT_PORT` | `127.0.0.1`, `1883` | Where the server's built-in broker is published. It accepts anonymous clients, so set `MQTT_BIND=0.0.0.0` only to let gateways on a network you trust publish to it |
| `SPLIT_NET` | `172.30.83` | The compose network's /24, as its first three octets; the web container is `.200` in it. Change it only if Docker reports that the pool overlaps another network, and update `trustedProxies` to match |

- When `userManagement.enabled` is true, set `userManagement.publicBaseUrl` in
  the server's config to the public web origin, including its scheme and port
  (for example, `http://localhost:8080` for a web container published on port
  8080). Login and other state-changing requests compare the browser's `Origin`
  or `Referer` with this configured origin; forwarding the public `Host` header
  alone is not enough. This value also determines mail links. See
  [account configuration](user-guide/accounts.md#2-configure).
- The web container finds the server through `BACKEND` (`corescope:3000` in the
  compose file). Port 3000 stays inside the compose network, and the server
  still serves everything there, the classic UI included; publish it if you want
  to reach that directly.
- The web image is built from your checkout, so keep the checkout and
  `CORESCOPE_IMAGE` on the same version: a newer UI may call endpoints an older
  server lacks. To update only the UI, rebuild the web container:
  `docker compose -f docker-compose.split.yml up -d --build web`.
- Behind the web container, as behind the all-in-one image's Caddy, every
  request reaches the server from the proxy's address, so per-IP limits are off
  until `webSocket.trustedProxies` and `userManagement.trustedProxies` list it.
  The compose file fixes that address at `172.30.83.200`: list it alone. Never
  list the compose subnet. It also holds the gateway address, and a client that
  reaches a published port 3000 through Docker's proxy (from the host itself or
  over IPv6, for example) arrives from there, so its own `X-Forwarded-For`
  would be believed.

### Static host or CDN

`make web` (or `scripts/static/build.sh`) writes the anonymous build to
`dist/web` for Cloudflare Pages or any static host. It calls the server
cross-origin, so the server lists the UI's origin in `CORS_ALLOWED_ORIGINS`.
It sends no cookies; logins and admin pages link to the classic UI on the
server. Setup, previews, server settings and rollback:
[deploy/static/README.md](../deploy/static/README.md).

---

## Docker Deployment

### Quick Start (one command)

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

Open `http://localhost` — you'll see an empty dashboard ready to receive packets.

No `config.json` is required. The server starts with sensible defaults:
- HTTP on port 3000 (Caddy proxies port 80 → 3000 internally)
- Internal Mosquitto MQTT broker on port 1883
- Ingestor connects to `mqtt://localhost:1883` automatically
- SQLite database at `/app/data/meshcore.db`

### Full `docker run` Reference (recommended)

The bare `docker run` command is the primary deployment method. One image, documented parameters — run it however you want.

```bash
docker run -d --name corescope \
  --restart=unless-stopped \
  -p 80:80 -p 443:443 -p 1883:1883 \
  -e DISABLE_MOSQUITTO=false \
  -e DISABLE_CADDY=false \
  -v /your/data:/app/data \
  -v /your/Caddyfile:/etc/caddy/Caddyfile:ro \
  -v /your/caddy-data:/data/caddy \
  ghcr.io/kpa-clawbot/corescope:latest
```

#### Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `-p 80:80` | Yes | HTTP web UI |
| `-p 443:443` | No | HTTPS (only if using built-in Caddy with a domain) |
| `-p 1883:1883` | No | MQTT broker (expose if external gateways connect directly) |
| `-v /your/data:/app/data` | Yes | Persistent data: SQLite DB, config.json, theme.json |
| `-v /your/Caddyfile:/etc/caddy/Caddyfile:ro` | No | Custom Caddyfile for HTTPS |
| `-v /your/caddy-data:/data/caddy` | No | Caddy TLS certificate storage |
| `-e DISABLE_MOSQUITTO=true` | No | Skip the internal Mosquitto broker (use your own) |
| `-e DISABLE_CADDY=true` | No | Skip the built-in Caddy reverse proxy |
| `-e CORESCOPE_URL=https://analyzer.example.org` | No | Public URL for link previews — see [Link previews](#link-previews-og-tags) |
| `-e MQTT_BROKER=mqtt://host:1883` | No | Override MQTT broker URL |
| `-e RUN_AS_UID=1000` | No | Run the Go services as this uid instead of root — see [Running the Go services unprivileged](#running-the-go-services-unprivileged) |
| `-e RUN_AS_GID=1000` | No | Group for `RUN_AS_UID` (defaults to it) |

#### `/app/data/.env` convenience file

Instead of passing `-e` flags, you can drop a `.env` file in your data volume:

```bash
# /your/data/.env
DISABLE_MOSQUITTO=true
DISABLE_CADDY=true
MQTT_BROKER=mqtt://my-broker:1883
```

The entrypoint sources this file before starting services. This works with any launch method (`docker run`, compose, or manage.sh).

Two exceptions, both because the entrypoint runs this file as root from a
directory the Go services can write once they run unprivileged:

- `RUN_AS_UID` and `RUN_AS_GID` are never taken from `.env`. Set them with
  `-e` or in compose's `environment:`.
- With `RUN_AS_UID` set, `.env` is read only if it is owned by root and
  writable by no one else (`sudo chown root:root .env && sudo chmod 644 .env`).
  Otherwise the log says it was skipped, and why. "Owned by root" means uid 0
  inside the container. With rootless Docker or a user-namespace remap
  (`userns-remap`), host root is not that uid: look at the owner the container
  reports (`docker exec <container> stat -c '%u' /app/data/.env`) and give the host
  file whichever owner id maps to container uid 0.

### Docker Compose (legacy alternative)

Docker Compose files are maintained for backward compatibility but are no longer the recommended approach.

```bash
curl -sL https://raw.githubusercontent.com/Kpa-clawbot/CoreScope/master/docker-compose.example.yml \
  -o docker-compose.yml
docker compose up -d
```

#### Compose environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_PORT` | `80` | Host port for the web UI |
| `DATA_DIR` | `./data` | Host path for persistent data |
| `DISABLE_MOSQUITTO` | `false` | Set `true` to use an external MQTT broker |
| `DISABLE_CADDY` | `false` | Set `true` to skip the built-in Caddy proxy |
| `CORESCOPE_URL` | unset | Public URL for link previews — see [Link previews](#link-previews-og-tags) |

### manage.sh (legacy alternative)

The `manage.sh` wrapper script provides a setup wizard and convenience commands. It uses Docker Compose internally. See [DEPLOY.md](../DEPLOY.md) for usage. New deployments should prefer bare `docker run`.

### Image tags

| Tag | Use case |
|-----|----------|
| `v3.4.1` | Pinned release — recommended for production |
| `v3.4` | Latest patch in the v3.4.x series |
| `v3` | Latest minor+patch in v3.x |
| `latest` | Latest release tag |
| `edge` | Built from master on every push — unstable |

### Updating

```bash
docker compose pull
docker compose up -d
```

For `docker run` users:

```bash
docker pull ghcr.io/kpa-clawbot/corescope:latest
docker stop corescope && docker rm corescope
docker run -d --name corescope ... # same flags as before
```

Data is preserved in the volume — updates are non-destructive.

### Running the Go services unprivileged

`corescope-server` and `corescope-ingestor` can run as a normal user instead of
root, so that a code-execution bug in either one does not land as root in a
container that has your data directory and `config.json` mounted. `supervisord`
itself stays root, because mosquitto and caddy still need to bind their ports.

It is opt-in, with `RUN_AS_UID`:

| `RUN_AS_UID` | Services run as |
|--------------|-----------------|
| unset (the default) | root — as in earlier releases |
| set | `RUN_AS_UID` / `RUN_AS_GID` |

Nothing about an upgrade changes until you set it: the container does not pick a
uid for you, and does not change the owner of anything on your host. That is
deliberate rather than cautious. Every release before this one ran as root, so
the `meshcore.db` in your data directory — with its `-wal` and `-shm`, `users.db`
and the backups beside them — is root-owned. Switching user without also handing
those files over leaves the services unable to write the database, so the switch
has to be something you ask for.

**To turn it on**, pick the uid you want and restart. Setting `RUN_AS_UID` is
taken as permission to `chown` the data directory, and what an earlier root-run
container left in it, to that uid. This happens at every start, so a file
created there later as root (with `docker exec`, say) is handed over at the
next restart:

```bash
docker run -d --name corescope \
  -e RUN_AS_UID=1000 -e RUN_AS_GID=1000 \
  -v /your/data:/app/data \
  ... # same flags as before
  ghcr.io/kpa-clawbot/corescope:latest
```

Check which user it picked:

```bash
docker logs corescope 2>&1 | grep '\[entrypoint\] Go services run as'
# [entrypoint] Go services run as corescope (uid=1000 gid=1000)
```

If you would rather the container never `chown` anything, do the hand-over
yourself first. Then there is nothing left for it to change:

```bash
docker stop corescope
sudo chown -R 1000:1000 /your/data     # uid of your own account is usually 1000
docker start corescope                 # with RUN_AS_UID=1000 set as above
```

To go back, unset `RUN_AS_UID` and `chown -R root:root /your/data`.

Notes:

- `RUN_AS_GID` defaults to `RUN_AS_UID` when omitted. `RUN_AS_UID=0` is the
  same as leaving it unset.
- If the uid already belongs to another account inside the image (`caddy` is
  uid 100, `mosquitto` 101), that account is used as it is, keeping its own
  group, and the log says so. Pick a different uid if you need `RUN_AS_GID`
  honoured.
- With `RUN_AS_UID` set to a uid the container cannot use, startup fails rather
  than quietly falling back to root.
- What the hand-over leaves alone: anything mounted into the data directory on
  its own, such as `-v ./config.json:/app/data/config.json:ro`, which keeps the
  owner it has on your host (make sure the uid can read it), and `.env` (see
  above). Each mount it skips is named in the log.
- If the hand-over cannot change a file, for example on NFS with
  `root_squash`, the log says how many and which. Fix their owner on the host.
- `/app` stays root's. The services write only to the data directory and
  `/tmp`.
- A fresh install is root-owned and so starts as root, like any other. Set
  `RUN_AS_UID` from the first run to avoid a hand-over later.

The uid selection lives in `docker/select-service-user.sh`, with its branches
covered by `docker/test-select-service-user.sh`.

---

## Configuration Reference

CoreScope uses a layered configuration system (highest priority wins):

1. **Environment variables** — `MQTT_BROKER`, `DB_PATH`, etc.
2. **`/app/data/config.json`** — full config file (volume-mounted)
3. **Built-in defaults** — work out of the box with no config

### Environment variable overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER` | `mqtt://localhost:1883` | MQTT broker URL (overrides config file) |
| `MQTT_TOPIC` | `meshcore/#` | MQTT topic subscription pattern |
| `DB_PATH` | `data/meshcore.db` | SQLite database path |
| `DISABLE_MOSQUITTO` | `false` | Skip the internal Mosquitto broker |
| `DISABLE_CADDY` | `false` | Skip the built-in Caddy reverse proxy |
| `CORESCOPE_URL` | unset | Public URL for link previews — see [Link previews](#link-previews-og-tags) |

### Link previews (og tags)

Discord, Slack, Facebook and other link previews read `og:image` and
`twitter:image` from `index.html`, and need them absolute. The page holds a
`__CORESCOPE_URL__` placeholder, which the server fills in the same way it
fills in the `__BUST__` cache buster:

- **`CORESCOPE_URL` set** (`http(s)://host[:port]`, optionally with a path,
  e.g. `https://analyzer.example.org`): that URL, on every page, and no request
  header is consulted. Trailing slashes are dropped. `og:url` is sent too.
- **Unset, or invalid** (logged at startup): the origin each request reached:
  the `Host` header, with the scheme from TLS or `X-Forwarded-Proto`.
  `og:url` is left out. `X-Forwarded-Host` is never used.

`og:url` can only be the site root, because the part of a link after `#` never
reaches the server. Platforms that follow it, such as Facebook and Messenger,
may then open the home page when someone clicks the preview of a shared deep
link. Leave `CORESCOPE_URL` unset if that matters more to you than the tag.

On a public instance, setting `CORESCOPE_URL` is otherwise recommended. Unset,
a forged `Host` header is echoed into that response's image links. Only the
sender sees it as long as nothing in front caches the page (it is `no-store`).

Also set `CORESCOPE_URL` when:

- a proxy in front rewrites the `Host` header;
- TLS ends before the last proxy (the built-in Caddy behind nginx sees plain
  HTTP and says so in `X-Forwarded-Proto`, so previews would link `http://`);
- a proxy serves CoreScope under a path: include the path, or the image link
  misses the prefix;
- a CDN caches pages despite their `no-store` header, so that no visitor's
  request ends up in another's preview.

With compose, put it in the `.env` next to the compose file:
`CORESCOPE_URL=https://analyzer.example.org`.

The static build (`scripts/static/build.sh`, the web container) has no
request to take an origin from: it takes `CORESCOPE_URL` when the UI is
packaged (a build argument of `Dockerfile.web`), and without one leaves
`og:url` out and links the project's preview image on GitHub.

### config.json

For advanced configuration, create a `config.json` and mount it at `/app/data/config.json`:

```bash
docker run -d --name corescope \
  -p 80:80 \
  -v corescope-data:/app/data \
  -v ./config.json:/app/data/config.json:ro \
  ghcr.io/kpa-clawbot/corescope:latest
```

See `config.example.json` in the repository for all available options including:
- MQTT sources (multiple brokers)
- Channel encryption keys
- Branding and theming
- Health thresholds
- Region filters
- Retention policies
- Geo-filtering
- Map tile providers (OSM, Stamen, Carto, etc.)

### Map Tile Providers

Map tiles are configured under `map.tiles` in `config.json`. Several providers are built in; you switch whole provider groups on or off, set the key for Carto, and can add your own tile servers. Everything under `map.tiles` is sent to every visitor (see [Public config and privacy](#public-config-and-privacy)).

#### How the picker works for visitors

The Map, Live and Repeater Planner pages show the layer picker. Every other map (node detail, node reach, RX coverage, the Analytics route map and the home mini-map) has no picker but follows the same choice; because those maps never load MapLibre, a vector choice shows the theme's default raster map there. **Auto** is the default: it picks the light or dark map to match the current theme, using `lightDefault` / `darkDefault`. A visitor can instead pin a specific map. Pinned maps are listed in **Street**, **Topographic** and **Satellite** groups (gray styles follow the plain ones inside each group), and a separate **Overlays** group has checkboxes for layers drawn on top of the map (place labels, hillshade). A pin is saved per browser in `localStorage` (`mc-tile-pinned`) and survives a reload: every picker opens on it until the visitor picks **Auto** again, and the maps without a picker show it whenever it suits the current theme (satellite always does), otherwise the theme's map. Ticked overlays are saved the same way. Nothing is stored on the server.

#### Built-in providers

The group is the key under `map.tiles.providers`. "Default" is the state when the group is absent from your config.

| Group | Maps (ids) | Category | Theme | Key | Default | Terms |
|---|---|---|---|---|---|---|
| `carto` | `carto-light`, `carto-dark`, `carto-voyager`, `carto-voyager-dark`, `positron-dark` | street | light / dark | Yes, free (see [below](#carto-now-requires-an-api-key)) | On | Free key, 5M tiles/month |
| `osm` | `osm-standard`, `osm-dark` | street | light / dark | Optional (`provider` + `token`: `thunderforest`, `maptiler` or `mapbox`) | Off | Without a token this is the OSM community server: fair use only |
| `opentopomap` | `opentopomap` | topographic | light | No | Off | Fair use, zoom up to 17 |
| `stamen` | `stamen-toner-lite`, `stamen-toner-dark` | street | light / dark | Yes (`token`, Stadia Maps); not offered without one | Off | Stadia Maps terms |
| `usgs` | `usgs-topo`, `usgs-imagery` | topographic, satellite | light | No | Off | US coverage, zoom up to 16 |
| `esri` | `esri-darkgray-labels` | street | dark | No | Always on | Esri terms |
| `eox` | `eox-s2-2025`, `eox-s2-2016` (Sentinel-2 satellite), `eox-terrain-light`; overlays `eox-labels`, `eox-labels-dark` | satellite, topographic | any (satellite), light (terrain) | No | Off | 2016 imagery CC BY 4.0; 2025 imagery CC BY-NC-SA 4.0 (non-commercial) |
| `osmfr` | `osmfr-hot` (Humanitarian), `osmfr-cyclosm` (CyclOSM), `osmfr-gray` (OSM Gray), `osmfr-gray-dark` (OSM Gray Dark) | street, topographic | light / dark | No | Off | OSM France community servers, donation-funded fair use |
| `openfreemap` | `openfreemap-liberty`, `-bright`, `-positron` (light); `openfreemap-dark`, `-fiord` (dark) | street (vector) | light / dark | No | Off | Free, no limits |
| `versatiles` | `versatiles-colorful`, `-muted`, `-gray` (light); `versatiles-colorful-dark`, `-gray-dark` (dark) | street (vector) | light / dark | No | Off | Free |
| `hillshade` | overlay `hillshade` | overlay | any | No | Off | Elevation tiles from AWS Open Data by default |

**EOX licensing.** The 2016 Sentinel-2 imagery is CC BY 4.0. The later years (including 2025) are CC BY-NC-SA 4.0, which excludes commercial use. The group is off by default; a commercial deployment should leave it off or license the imagery from EOX. The EOX group also carries Terrain Light and the place-label overlays, so leaving it off leaves those out too.

**Be a good neighbour.** OSM France, OpenTopoMap and the OSM community server are volunteer-run. Keep your instance's traffic modest, or self-host (see [Self-hosting](#self-hosting-tiles)).

#### Turning groups on and off

Set `enabled` on a group:

```json
"map": {
  "tiles": {
    "providers": {
      "osm":         { "enabled": true },
      "openfreemap": { "enabled": true },
      "hillshade":   { "enabled": true }
    }
  }
}
```

- `eox`, `osmfr`, `openfreemap`, `versatiles`, `hillshade`, `osm`, `opentopomap`, `stamen` and `usgs` are off until you set `"enabled": true` (the boolean; a string does not count). `stamen` additionally needs a `token`. Upgrading therefore changes nothing in the picker until you turn a group on: check the group's terms in the table above first.
- `carto` is on unless `"enabled": false`; `esri` is always available.
- Disabling a group removes its maps from the picker. If a visitor had saved one, or `darkDefault` / `lightDefault` names one, it falls back to the default.

`darkDefault` and `lightDefault` pick which map **Auto** uses in the dark and light theme. They take any registered map id from the table above or a custom entry as `custom-<id>`. Defaults are `carto-dark` and `carto-light`; an unknown or disabled id is ignored.

#### Your own tile servers: `map.tiles.custom`

`map.tiles.custom` is a list of up to 20 entries. Each appears in the picker as `custom-<id>`. Custom entries are always enabled when valid.

| Field | Required | Values | Default |
|---|---|---|---|
| `id` | Yes | 1-32 characters of `a-z`, `0-9`, `-`, starting with a letter or digit. Unique across all custom entries | |
| `url` | Yes | See URL rules below. Raster: an XYZ template containing `{z}`, `{x}` and `{y}` (or `{-y}`). Vector: a MapLibre style JSON URL | |
| `label` | No | Text, at most 64 characters | the `id` |
| `type` | No | `light`, `dark` or `any`. `any` is listed under both themes and is never color-inverted (use for satellite) | `light` |
| `category` | No | `street`, `topo` or `satellite` | `street` |
| `format` | No | `raster` or `vector` | `raster` |
| `attribution` | No | Plain text, at most 200 characters (HTML is escaped, not rendered) | empty |
| `maxZoom` | No | Number, clamped to 0-24 | 19 |
| `maxNativeZoom` | No | Number, clamped to 0-24. Highest zoom the server really has; the browser stretches tiles above it up to `maxZoom` | unset |
| `subdomains` | No | 1-8 characters of `a-z`, `0-9`, used for `{s}` | `abc` |
| `overlay` | No | `true` adds a raster layer drawn over the base map (checkbox under **Overlays**) instead of a base map. Must be `format: "raster"` | `false` |

**URL rules.** `url` is at most 2048 characters and must be one of:

- `https://host/...` or `http://host/...` (an `http://` tile URL is blocked by browsers on an `https` page, and vector maps refuse it outright);
- a same-site path starting with a single `/`, for example `/tiles/{z}/{x}/{y}.png`.

Rejected: any other scheme (`javascript:`, `data:`, `file:`, `ftp:`), protocol-relative `//host/...`, `/\host`, credentials in the URL (`user:pass@host`), backslashes, whitespace or control characters, and any `{...}` other than `{z}` `{x}` `{y}` `{s}` `{r}` `{-y}`.

**What happens to a bad entry.** An invalid entry is skipped; the rest still load. For each skipped entry the browser console shows one warning such as `[tiles] custom tile "ours" skipped: url must be an https://, http:// or '/' URL ...` that names the entry's `id` and the reason (it never prints the URL, which may carry a key). If the `id` itself is invalid the warning names the list position instead, for example `map.tiles.custom[2] skipped: id must be ...`. Entries beyond the 20th are ignored with a warning, a duplicate `id` is skipped, and a vector entry with `overlay: true` is skipped. Open the browser console on the map page to check your config.

**Example 1: self-hosted raster XYZ server**

```json
"custom": [
  {
    "id": "ours",
    "label": "Our tile server",
    "type": "light",
    "category": "street",
    "format": "raster",
    "url": "https://tiles.example.org/{z}/{x}/{y}.png",
    "attribution": "(c) OpenStreetMap contributors",
    "maxZoom": 19
  }
]
```

**Example 2: self-hosted vector style**

The `url` is a MapLibre style JSON. It can come from a self-hosted OpenFreeMap, or from tileserver-gl, Martin or Protomaps serving a `style.json`. The tile and glyph URLs inside the style must also be `https://` (or `http://` only if the page itself is `http://`).

```json
"custom": [
  {
    "id": "ours-vector",
    "label": "Our vector map",
    "type": "light",
    "category": "street",
    "format": "vector",
    "url": "https://maps.example.org/styles/liberty",
    "attribution": "OpenMapTiles, OpenStreetMap contributors"
  },
  {
    "id": "ours-vector-dark",
    "label": "Our vector map (dark)",
    "type": "dark",
    "format": "vector",
    "url": "https://maps.example.org/styles/dark",
    "attribution": "OpenMapTiles, OpenStreetMap contributors"
  }
]
```

**Example 3: a raster overlay (contour lines)**

Use transparent PNG tiles. The overlay is off until a visitor ticks it under **Overlays**.

```json
"custom": [
  {
    "id": "contours",
    "label": "Contour lines",
    "overlay": true,
    "format": "raster",
    "url": "https://tiles.example.org/contours/{z}/{x}/{y}.png",
    "attribution": "Contours (c) Example Org",
    "maxZoom": 19,
    "maxNativeZoom": 16
  }
]
```

To make a custom map the default, name it in `darkDefault` / `lightDefault`, for example `"lightDefault": "custom-ours"`.

#### Vector maps

OpenFreeMap, VersaTiles and custom `vector` entries are drawn with MapLibre GL, which is vendored in `public/vendor/` (no third-party script host). It is about 380 KB gzipped and is downloaded only the first time a vector map is shown; visitors who stay on raster maps never fetch it. The scripts are loaded with Subresource Integrity, so a proxy or CDN that rewrites JavaScript (minifying, injecting) makes them fail the check: vector maps then fall back to the raster map with only a console warning. Serve `public/vendor/` unmodified. If WebGL is unavailable, the library fails to load within 15 seconds, or the GL context is lost, the map falls back to the shipped default raster map for the current theme (Carto Positron or Carto Dark).

#### Hillshade overlay

The `hillshade` overlay shades terrain in the visitor's browser from Terrarium-encoded elevation tiles (`elevation m = R*256 + G + B/256 - 32768`, 256x256 or 512x512 PNG). By default it reads AWS Open Data, the same bucket the repeater planner uses for `terrain.tileUrl`. To read from your own mirror, set a template URL (same URL rules as `custom`, and it must contain `{z}`, `{x}`, `{y}`; otherwise the default is used and a console warning is logged):

```json
"hillshade": { "enabled": true, "url": "https://tiles.example.org/terrarium/{z}/{x}/{y}.png" }
```

The tile host must allow cross-origin image reads (`Access-Control-Allow-Origin`); without it the browser cannot read pixels and the overlay stays blank. It is off for visitors until they tick it.

#### Self-hosting tiles

Self-hosting removes the third-party dependency and keeps visitors' requests on your infrastructure. Follow the upstream documentation rather than any steps here:

- **OpenFreeMap**: run your own copy of the planet tiles and styles; see <https://github.com/hyperknot/openfreemap> (self-hosting guide). Point a `vector` custom entry at your `styles/<name>` URL.
- **Protomaps (PMTiles)**: a single-file tile archive served from static storage or a small server; see <https://docs.protomaps.com>. Use its style JSON as a `vector` custom entry.
- **Any XYZ raster server** (tileserver-gl, Martin, a static tile tree on object storage): use a `raster` custom entry.
- **Elevation for hillshade**: mirror the Terrarium tiles (`s3://elevation-tiles-prod/terrarium`, public, no credentials needed) and set `hillshade.url`. The bucket is large; mirror only your region's zoom levels (hillshade uses up to zoom 15).
- **Elevation for the planner**: mirror the SRTM `skadi` tree (`s3://elevation-tiles-prod/skadi`) and set `terrain.tileUrl` to its `https` base URL (no query string). That setting is server-side and separate from `map.tiles`; see [docs/api-spec.md](api-spec.md).

#### Public config and privacy

- **Everything under `map.tiles` is public.** The server sends it to every visitor, including anonymous ones. Never put credentials in a URL, and restrict any provider key by domain in that provider's dashboard. This applies to the Carto key, the `osm` and `stamen` tokens, and custom URLs alike.
- **Visitors' browsers fetch tiles directly** from the chosen host, not through CoreScope. That host sees the visitor's IP address and the map area being viewed. Browsers send only your site's origin as the Referer, not the full page path. If this matters for your users, pick a provider you trust or self-host.
- **A `/path` URL resolves against the page's origin.** On a static/CDN UI deployment (see [Static host or CDN](#static-host-or-cdn)) that is the UI host, not the API server.

#### CARTO now requires an API key

CARTO's raster basemaps (`carto-dark`, `carto-light`, `carto-voyager`,
`carto-voyager-dark`, `positron-dark`) require an API key. Without one, tiles
are still returned with a **200 status** but every one of them is stamped
`API KEY REQUIRED` — so nothing errors, no healthcheck fires, and the map just
quietly looks broken.

Get a free key at <https://carto.com/basemaps/apikey> — no CARTO account, no
credit card, emailed back immediately, 5 million tile requests per calendar
month across raster and vector. Then set it as `key`:

```json
"map": {
  "tiles": {
    "providers": {
      "carto": { "enabled": true, "key": "YOUR_CARTO_KEY" }
    }
  }
}
```

Notes:

- The query parameter is `key`. **`api_key` is silently ignored** and still
  serves the watermarked tile.
- The key is sent to the browser, so treat it as public. CARTO asks for a domain
  when the key is issued, but whether that restriction is enforced on every
  request has not been verified here. Assume anyone can read the key from
  devtools and spend your quota. The 5M/month fair-use ceiling makes that
  low-impact for most deployments, but it is worth knowing.
- A wrong or expired key fails the same silent way as no key at all. Verify by
  **looking at a tile**, not by checking for a 200.
- CARTO has said it is considering freezing data updates to the raster
  basemaps. If you would rather not depend on them, set
  `"carto": { "enabled": false }` and use another provider — `esri` needs no
  key at all, and `osm`/`stamen` accept their own tokens.

---

## MQTT Setup

CoreScope receives MeshCore packets via MQTT. The container ships with an internal Mosquitto broker — no setup needed for basic use.

### Internal broker (default)

The built-in Mosquitto broker listens on port 1883 inside the container. Point your MeshCore gateways at it:

```bash
# Expose MQTT port for external gateways
docker run -d --name corescope \
  -p 80:80 -p 1883:1883 \
  -v corescope-data:/app/data \
  ghcr.io/kpa-clawbot/corescope:latest
```

### External broker

To use your own MQTT broker (Mosquitto, EMQX, HiveMQ, etc.):

1. Disable the internal broker:
   ```bash
   -e DISABLE_MOSQUITTO=true
   ```

2. Point the ingestor at your broker:
   ```bash
   -e MQTT_BROKER=mqtt://your-broker:1883
   ```

   Or via `config.json`:
   ```json
   {
     "mqttSources": [
       {
         "name": "my-broker",
         "broker": "mqtt://your-broker:1883",
         "username": "user",
         "password": "pass",
         "topics": ["meshcore/#"]
       }
     ]
   }
   ```

### Multiple brokers

CoreScope can connect to multiple MQTT brokers simultaneously:

```json
{
  "mqttSources": [
    {
      "name": "local",
      "broker": "mqtt://localhost:1883",
      "topics": ["meshcore/#"]
    },
    {
      "name": "remote",
      "broker": "mqtts://remote-broker:8883",
      "username": "reader",
      "password": "secret",
      "topics": ["meshcore/+/+/packets"]
    }
  ]
}
```

### MQTT topic format

MeshCore gateways typically publish to `meshcore/<gateway>/<region>/packets`. The default subscription `meshcore/#` catches all of them.

---

## TLS / HTTPS

### Option 1: External reverse proxy (recommended)

Run CoreScope behind nginx, Traefik, or Cloudflare Tunnel for TLS termination:

```nginx
# nginx example
server {
    listen 443 ssl;
    server_name corescope.example.com;

    ssl_certificate /etc/ssl/certs/corescope.pem;
    ssl_certificate_key /etc/ssl/private/corescope.key;

    location / {
        proxy_pass http://localhost:80;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

The `Upgrade` and `Connection` headers are required for WebSocket support.

The `X-Forwarded-For` line matters for the per-client limits on
`/api/elevation/profile` and `/api/planner/*`. Without it stock nginx passes
the visitor's own header through untouched, and the entry CoreScope trusts is
chosen by the visitor. `$proxy_add_x_forwarded_for` appends the real peer
address.

Those limits apply only when CoreScope can tell clients apart. A reverse proxy
on a loopback or private address that is not listed in
`webSocket.trustedProxies` (the shipped default) makes every visitor look like
one client, so the per-client limits are skipped and one warning is logged;
the server-wide terrain download and planner job budgets still apply. List the
proxy's address to turn them on.

A proxy listed in `webSocket.trustedProxies` MUST append to `X-Forwarded-For`
(never replace it with a client-supplied value). If you list a CDN's ranges,
anyone who can send requests from inside those ranges (for example CDN
workers) can choose the hop CoreScope uses. Traefik appends to
`X-Forwarded-For` by default for trusted entrypoints; check yours.

### Option 2: Built-in Caddy (auto-TLS)

The container includes Caddy for automatic Let's Encrypt certificates:

1. Create a Caddyfile:
   ```
   corescope.example.com {
     reverse_proxy localhost:3000
   }
   ```

2. Mount it and expose TLS ports:
   ```bash
   docker run -d --name corescope \
     -p 80:80 -p 443:443 \
     -v corescope-data:/app/data \
     -v caddy-certs:/data/caddy \
     -v ./Caddyfile:/etc/caddy/Caddyfile:ro \
     ghcr.io/kpa-clawbot/corescope:latest
   ```

Caddy handles certificate issuance and renewal automatically. `reverse_proxy`
appends the connecting address to `X-Forwarded-For` by default, so no extra
header configuration is needed; list the Caddy address in
`webSocket.trustedProxies`.

---

## API Documentation

CoreScope auto-generates an OpenAPI 3.0 specification from its route definitions. The spec is always in sync with the running server — no manual maintenance required.

### Endpoints

| URL | Description |
|-----|-------------|
| `/api/spec` | OpenAPI 3.0 JSON schema — machine-readable API definition |
| `/api/docs` | Interactive Swagger UI — browse and test all 40+ endpoints |

### Usage

**Browse the API interactively:**
```
http://your-instance/api/docs
```

**Fetch the spec programmatically:**
```bash
curl http://your-instance/api/spec | jq .
```

**For bot/integration developers:** The spec includes all request parameters, response schemas, and example values. Import it into Postman, Insomnia, or any OpenAPI-compatible tool.

### Public instance
The live instance at [analyzer.00id.net](https://analyzer.00id.net) has all API endpoints publicly accessible:
- Spec: [analyzer.00id.net/api/spec](https://analyzer.00id.net/api/spec)
- Docs: [analyzer.00id.net/api/docs](https://analyzer.00id.net/api/docs)

---

## Behind a CDN (Cloudflare, Fastly)

If you front CoreScope with a CDN — Cloudflare, Fastly, Akamai, or
similar — you **must** configure the CDN to bypass cache for `/api/*`.
The server emits `Cache-Control: no-store` on every API response
(see #1551), but Cloudflare's zone-level Cache Rules and legacy Page
Rules can override origin headers. When that happens, observers, packets,
stats and other API responses get cached at the edge for minutes to hours,
producing observer-flap, stale dashboards and inconsistent state across
viewers.

### 1. Verify whether your CDN is caching `/api/*`

From **outside** the CDN (a different network than your origin), run:

```sh
curl -sI 'https://<your-domain>/api/observers' | grep -iE 'cf-cache|age|cache-control'
```

Healthy output (cache is bypassed):

```
cache-control: no-store
cf-cache-status: BYPASS
age: 0
```

Unhealthy output (CDN is caching despite `no-store`):

```
cache-control: no-store
cf-cache-status: HIT
age: 4732
```

`HIT` or `age > 0` means an intermediary is serving cached JSON. Fix it
before relying on the dashboard.

You can also run the bundled helper, which exits non-zero with a precise
diagnostic when caching is detected:

```sh
scripts/check-cdn-bypass.sh https://<your-domain>
```

### 2. Cloudflare: add a Cache Rule (recommended)

Cloudflare Dashboard → your zone → **Caching → Cache Rules → Create rule**:

- **When incoming requests match:** Field = `URI Path`, Operator = `starts with`, Value = `/api/`
- **Then:** Cache eligibility → **Bypass cache**

Save and deploy. Re-run the curl above; you should now see
`cf-cache-status: BYPASS`.

Legacy Page Rules equivalent (if your zone has no Cache Rules):

- URL pattern: `*your-domain*/api/*`
- Setting: **Cache Level → Bypass**

### 3. Fastly / other CDNs

Apply the equivalent bypass-cache rule for the `/api/` path prefix.
The key invariant is: any response from `/api/*` must reach the
browser uncached (no shared-cache HIT, no positive `Age` header).

### 4. Re-verify

After applying the rule, run step 1's curl from outside the CDN again
and confirm `cf-cache-status: BYPASS` (or absence of HIT) and `age: 0`.

### 5. Watch the server log

The server logs a one-shot warning at the first request bearing a
CDN-specific header (`CF-Connecting-IP`, `CF-Ray`, `Fastly-Client-IP`,
or `True-Client-IP`):

Generic reverse-proxy headers (`X-Forwarded-For`, `X-Real-IP`) are
deliberately NOT used as the signal — every nginx/Caddy/Traefik/k8s
deploy sets them, so they'd produce false positives on every
reverse-proxied install.

```
[security] WARNING: detected request via CDN (CF-Ray header present).
Ensure /api/* is bypassed in your CDN config — see docs/deployment-behind-cdn.md.
Cached API responses cause observer-flap and incorrect dashboards.
```

This is informational — the request is not blocked. Treat it as a
prompt to run the verification curl above. The warning logs at most
once per process boot, regardless of how many CDN-fronted requests
arrive.

### Why this can't be fixed server-side

CDN cache policy is operator-controlled. The application emits the
most conservative cache header it can (`Cache-Control: no-store`),
but Cloudflare Cache Rules / Page Rules have higher precedence than
origin headers in many zone configurations. The only durable fix is
the operator-side bypass rule.

---

## Monitoring & Health Checks

### Docker health check

The container includes a built-in health check that hits `/api/stats`:

```bash
docker inspect --format='{{.State.Health.Status}}' corescope
```

Docker reports `healthy` or `unhealthy` automatically. The check runs every 30 seconds.

### Manual health check

```bash
curl -f http://localhost/api/stats
```

Returns JSON with packet counts, node counts, and version info:

```json
{
  "totalPackets": 56234,
  "totalNodes": 142,
  "totalObservers": 12,
  "packetsLastHour": 830,
  "packetsLast24h": 19644,
  "engine": "go",
  "version": "v3.4.1"
}
```

### Log monitoring

```bash
# All logs
docker compose logs -f

# Server only
docker compose logs -f | grep '\[server\]'

# Ingestor only
docker compose logs -f | grep '\[ingestor\]'
```

### Resource monitoring

```bash
docker stats corescope
```

---

## Backup & Restore

### Backup

Use **`GET /api/backup`** for the telemetry database. It uses SQLite's
[`VACUUM INTO`](https://www.sqlite.org/lang_vacuum.html) to produce a consistent
snapshot while the ingestor is running, including committed data in the WAL.
Do not copy the live `meshcore.db` file: a checkpoint followed by a file copy
also races new writes. Allow room for a full snapshot in the server's temporary
directory and at the download destination; large snapshots cost CPU and disk I/O.

When accounts are enabled, sign in as an administrator and open `/api/backup`
on the same site to download a snapshot. For command-line or scheduled backups,
configure a randomly generated `apiKey` in `config.json` (at least 16 characters).
Known weak/default keys are rejected even when they meet the length requirement.
Create a private header file, `~/.config/corescope/backup.header`, containing one line:
`X-API-Key: <your configured key>`. Set its permissions to `600`; keep the key out
of shell history, URLs, and scripts checked into Git.

On the backup host, install `curl` and the `sqlite3` CLI. Replace the example
HTTPS origin below with your instance. The download is published as a `.db`
only after it is complete, nonempty, and passes SQLite's consistency check:

```bash
(
  set -eu
  umask 077
  backup_dir="$HOME/corescope-backups"
  mkdir -p "$backup_dir"
  tmp=$(mktemp "$backup_dir/corescope-$(date -u +%Y%m%dT%H%M%SZ).XXXXXXXX")
  trap 'rm -f "$tmp"' EXIT
  curl -q --fail --silent --show-error \
    --connect-timeout 30 --max-time 1800 \
    --header "@$HOME/.config/corescope/backup.header" \
    --output "$tmp" https://corescope.example.org/api/backup
  test -s "$tmp"
  test "$(sqlite3 -readonly "$tmp" 'PRAGMA quick_check;')" = ok
  mv "$tmp" "$tmp.db"
)
```

The temporary-file template ends in `X` characters for both GNU and BSD/macOS
`mktemp`. The 30-minute limit is curl's client-side ceiling for generating and
downloading the snapshot. CoreScope separately sets a 60-second HTTP write
timeout, which the backup handler does not override; reverse proxies can impose
their own deadlines. Raising `--max-time` does not extend those server or proxy limits.
Timeouts fail the script and remove the incomplete download without replacing a
previous completed backup.

This is a point-in-time telemetry snapshot, not continuous replication or a full
instance backup. Keep protected copies off the CoreScope host, and also preserve:
- `config.json`, `theme.json`, deployment settings, and any configured keys/secrets.
- When accounts are enabled, **`users.db` separately**: use the
  [account backup and restore guide](user-guide/accounts.md#backups), which covers
  its own snapshot endpoint and scheduled backups. Do not copy that database live.

### Restore

1. Verify the downloaded file is nonempty and run
   `sqlite3 -readonly backup.db 'PRAGMA quick_check;'`; the result must be `ok`.
   Keep an untouched copy of this snapshot.
2. Stop **every server and ingestor using the target database**, including any
   separate instances. For the single-container example, use `docker stop corescope`;
   for `docker-compose.example.yml`, use
   `docker compose -f docker-compose.example.yml stop corescope`.
3. Locate the host directory or named volume mounted at `/app/data`. For the
   example Compose file, this is `DATA_DIR` (default `./data`). While all database
   users remain stopped, move the old `meshcore.db` **and any matching `-wal`,
   `-shm`, and `-journal` files together** into a separate recovery directory.
   Keep that set intact; never leave an old WAL beside the replacement database.
4. Copy the checked snapshot into that data mount as `meshcore.db`. Preserve the
   previous database's owner and permissions so the configured service user can
   read and write it. Leave configuration, keys, and `users.db` in place.
5. Start the stopped services (`docker start corescope`, or the matching Compose
   `start corescope` command). Check the logs, `/api/healthz`, and expected historical
   packets in the dashboard. Keep the recovery directory until these checks pass.

To roll back, stop all database users again, move the replacement database and
its sidecars aside, then restore the entire original file set before restarting.

### Automated backups

Run the checked-download block above from a private script using cron or a
systemd timer. Use absolute paths and the backup user's protected header file;
schedule one run at a time during quieter hours. Alert on nonzero exit status.
Prune old **completed `.db` snapshots only after a new backup succeeds**, according
to your retention policy, and periodically test a restore in an isolated instance.

---

## Troubleshooting

### Container starts but dashboard is empty

This is normal on first start with no MQTT sources configured. The dashboard shows data once packets arrive via MQTT. Either:
- Point a MeshCore gateway at the container's MQTT broker (port 1883)
- Configure an external MQTT source in `config.json`

### "no MQTT connections established" in logs

The ingestor couldn't connect to any MQTT broker. Check:
1. Is the internal Mosquitto running? (`DISABLE_MOSQUITTO` should be `false`)
2. Is the external broker reachable? Test with `mosquitto_sub -h broker -t meshcore/#`
3. Are credentials correct in `config.json`?

### WebSocket disconnects / real-time updates stop

If behind a reverse proxy, ensure WebSocket upgrade headers are forwarded:
```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
```

Also check proxy timeouts — set them to at least 300s for long-lived WebSocket connections.

### High memory usage

The in-memory packet store grows with retained packets. Configure retention limits in `config.json`:

```json
{
  "packetStore": {
    "retentionHours": 24,
    "maxMemoryMB": 512
  },
  "retention": {
    "nodeDays": 7,
    "packetDays": 30
  }
}
```

`packetStore.maxMemoryMB` bounds the store **and the caches that belong to it** — the decoded-packet cache and per-packet index entries, not just the stored rows. It is enforced in two places: the startup load stops at the budget, and the store evicts oldest-first when it exceeds it. Leaving it unset means no limit. Actual usage is on `/api/perf` as `packetStore.trackedMB`, next to `maxMB`.

### Database locked errors

SQLite doesn't support concurrent writers well. Ensure only one CoreScope instance accesses the database file. If running multiple containers, each needs its own database.

### Container unhealthy

Check logs: `docker compose logs --tail 50`. Common causes:
- Port 3000 already in use inside the container
- Database file permissions (must be writable by the container user)
- Corrupted database — restore from backup

### ARM / Raspberry Pi issues

- Use `linux/arm64` images (Pi 4 and 5). Pi 3 (armv7) is not supported.
- First pull may be slow — the multi-arch manifest selects the right image automatically.
- If memory is tight, set `packetStore.maxMemoryMB` to limit RAM usage.
