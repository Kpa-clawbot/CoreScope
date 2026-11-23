# Location Privacy (fuzzy location)

Location privacy is an opt-in setting that stops this CoreScope instance from serving the exact
position of any node. Every position the web UI and the API show is an approximate point within a
fixed radius of the real one. It is off by default.

It limits what users of **this instance** can learn. It does not make positions secret: adverts are
public radio traffic. Read [What it protects, and what it does not](#what-it-protects-and-what-it-does-not)
before relying on it.

## Quick start

1. Generate a secret of at least 32 bytes:

   ```bash
   openssl rand -base64 48
   ```

2. Enable the feature in `config.json`:

   ```json
   "locationPrivacy": {
     "enabled": true,
     "radiusM": 1000
   }
   ```

3. Provide the secret, preferably through the environment so it stays out of `config.json`:

   ```bash
   export CORESCOPE_LOCATION_SECRET='<the generated secret>'
   ```

   When `CORESCOPE_LOCATION_SECRET` is set, it overrides `locationPrivacy.secret`. You can instead
   put the value in `"secret"` inside the block.

4. Restart the server.

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `false` | Turns the feature on. |
| `radiusM` | `1000` | Maximum distance in metres between a served position and the real one. Valid range 100 to 50000; `0` or omitted means 1000. |
| `secret` | empty | At least 32 bytes. Overridden by `CORESCOPE_LOCATION_SECRET` when that is set. Never written to API output or logs. |

The server reads this block; the ingestor does not need it. See also the `locationPrivacy` block in
`config.example.json`.

## What changes for users

- **Node positions.** Every node position served (map markers, node detail, observers, exports,
  packets) is within `radiusM` of the real one. The point is stable: it is derived from a grid and
  the secret, not re-randomised per request, and it stays put while the node moves only a little.
- **Co-located nodes share a point.** The offset comes from the grid cell only, not from the node.
  Nodes in the same cell (several repeaters on one mast, or nodes close together) are shown at the
  same approximate point, so their markers can stack.
- **Distances and analysis.** The repeater planner, line of sight, distances, reach and neighbour
  distances are computed from the approximate positions. Treat their results as indicative.
- **RX coverage and RF noise.** Cells built from companion GPS fixes are coarser. Each fix is
  replaced by the centre of a cell at least 2 x `radiusM` on a side, and the hexes drawn from those
  are no smaller than `radiusM` (circumradius).
- **Advert packets.** For an advert that carries a location, the server serves the packet without
  its raw bytes (`raw_hex` is null), with `decoded_json` lacking the `lat`, `lon`, `signature` and
  `raw` keys, and with `location_hidden: true`. The packet hash is unchanged. Such a packet carries
  `frame_bytes` (the frame length) and `frame_head` (hex of the header and path bytes only), so
  size filters and hash-size badges keep working. An advert frame that is truncated, but holds
  signature bytes, is hidden too. This applies to HTTP responses and the WebSocket live feed.
- **Per-companion views.** `?rx=<companion>` views of RX coverage, RF noise and coverage gaps are
  served only to the companion's linked owner (with user management) or an admin; anyone else gets
  a 403. Aggregate views stay public. Cell centres can be discovered by querying the public map, so
  a parked companion whose GPS jitters across a cell edge would otherwise show in two adjacent
  cells and be pinned to that edge.
- **Geo filter and areas.** `geo_filter`, area membership (`?area=`) and the admin geo-filter prune
  still classify nodes by their exact position. Nodes are never shown at that position.
- **UI.** The map legend and attribution, node detail, coverage pages and exports say that
  positions are approximate, and the packet detail says when raw bytes are hidden.
  `GET /api/config/client` reports `locationPrivacy: {enabled, radiusM}` for this; the key is
  absent when the feature is off.

## What it protects, and what it does not

Protects against: people using this CoreScope instance, through the web UI or the API including the
live feed, learning the exact position of a node or where a companion was.

Does not protect against:

- Anyone with a LoRa radio. Adverts carry the position in clear and are broadcast.
- Anyone with access to the MQTT broker the observers publish to.
- Other analyzers fed by the same data.
- Copies of the data scraped before the setting was enabled.
- Admins and API-key holders: the backup download, dropped packets and the admin geo-prune keep
  exact data.
- The database itself. It stays exact. The server stays read-only (the ingestor is unchanged), and
  turning the setting off restores exact output.

## Operations

- **Keep the secret private and stable.** Use the same secret across restarts and across replicas.
  Changing it moves every approximate position, and comparing the old and the new positions of a
  node narrows down where it really is. Rotate only if the secret has leaked.
- **Startup refusal.** The server exits at startup when location privacy is enabled and the secret
  (from the config or `CORESCOPE_LOCATION_SECRET`) is missing or shorter than 32 bytes, or when
  `radiusM` is outside 100 to 50000.
- **Env var counts as intent.** If `CORESCOPE_LOCATION_SECRET` is set, the server starts only when
  location privacy is enabled or the block says `"enabled": false` explicitly. A missing block, a
  misspelled key or a missing `enabled` stops startup, so a supplied secret never leaves the
  instance serving exact positions.
- **Unparseable config.** If `config.json` cannot be parsed, the server refuses to start when
  `CORESCOPE_LOCATION_SECRET` is set, or when the raw file has a `locationPrivacy` block with
  `"enabled": true`. A disabled install with a broken config (for example a copy of
  `config.example.json`, whose block is disabled, with a typo) starts as before, as does a missing
  or unreadable file without the env secret.
- **Replicas.** Each replica keeps its own small in-memory state that stops a node flickering
  between cells near a cell edge. A node sitting exactly on a cell edge when a replica starts may
  therefore be shown at a slightly different approximate point by different replicas. Both points
  are still within the radius.

## Residual risks

- Someone who can inject adverts (a radio near an observer, or an MQTT feed) can learn where the
  grid lines lie by placing their own nodes and watching where the served points change.
- Restarts, replicas and eviction from the in-memory state can re-anchor a node that sits on a cell
  edge. Both points are within the radius, and comparing them says a little about where the node is.
- Rotating the secret lets old and new points be compared (see Operations).
- The origin of the companion cell grid can be recovered by bisecting the public coverage map. That
  limits a fix to its cell and nothing finer, which is the guarantee.

## FAQ

**Why not just round the coordinates?**
Rounding to a fixed grid gives a public, predictable cell, and nodes near an edge flip between
cells as they move or as GPS jitters. Here the grid origin and a per-node offset come from the
secret, so the served point is not the cell centre or any public function of the position, and the
distance to the real position is bounded by `radiusM`.

**Why do co-located nodes show at one point?**
If each node had its own independent offset, the points of several nodes on one site could be
averaged to get close to the real position. With the offset taken from the cell, they all show the
same point, and there is nothing to average.

**Why is the planner less accurate?**
It works from the served positions, which are up to `radiusM` off. Short legs and fine distances
change the most. Lower `radiusM` for better accuracy and less privacy.

**Why are the coverage cells coarse?**
A companion fix is given a minimum uncertainty (a cell at least 2 x `radiusM` wide) rather than a
maximum error, so that repeated requests cannot place a fix better than its cell.

**How do I turn it off?**
Set `"enabled": false` (or remove the block) and restart. Exact positions are served again, since
the database was never changed.
