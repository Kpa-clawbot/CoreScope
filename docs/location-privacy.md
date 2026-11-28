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
| `radiusM` | `1000` | Maximum distance in metres between a served position and the real one. Each node has its own grid with cells 0.4 x `radiusM` on a side; the grid of one node says nothing about another. Several nodes at one site can be averaged, so raise it for more cover there (see [What it protects](#what-it-protects-and-what-it-does-not)). Valid range 100 to 50000; `0` or omitted means 1000. |
| `secret` | empty | At least 32 bytes. Overridden by `CORESCOPE_LOCATION_SECRET` when that is set. Never written to API output or logs. |

The server reads this block; the ingestor does not need it. See also the `locationPrivacy` block in
`config.example.json`.

## What changes for users

- **Node positions.** Every node position served (map markers, node detail, observers, exports,
  packets) is within `radiusM` of the real one. The point is stable: it is derived from the secret,
  the node's public key and a grid, not re-randomised per request, and it stays put while the node
  moves only a little.
- **Each node has its own point.** The grid and the offset are derived from the node's public key,
  so every node is moved independently. Nodes at the same spot (several repeaters on one mast) are
  shown at separate points, up to 2 x `radiusM` apart, and two markers close together on
  the map are not evidence that the nodes are close.
- **Nodes without a full public key.** A node whose public key is not 64 hex digits is served
  without a position.
- **Distances and analysis.** The repeater planner, line of sight, distances, reach and neighbour
  distances are computed from the approximate positions. Treat their results as indicative.
  The ducting overlay's forecast cells are placed around the approximate positions too.
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
  positions are approximate, and the packet detail says when raw bytes are hidden. A dashed
  circle of radius `radiusM` around the served point shows where the node can be: always on the
  node detail map, and on the main and live maps around the node under the pointer and the
  selected node (open popup or detail panel). The circle is drawn around the served point, so the
  real position is anywhere inside it, not at its centre.
  `GET /api/config/client` reports `locationPrivacy: {enabled, radiusM}` for this; the key is
  absent when the feature is off.

## What it protects, and what it does not

Protects against: people using this CoreScope instance, through the web UI or the API including the
live feed, learning the exact position of a node or where a companion was.

How much it hides depends on what the viewer already knows:

- **From the served point alone,** a node is somewhere within `radiusM` of it.
- **Knowing where another node is does not help.** Each node has its own grid (cells 0.4 x
  `radiusM` on a side) and its own offset per cell, both derived from the secret and that node's
  public key. The served point of one node says nothing about the grid or the offset of any other.
  Someone who knows the real position of one node (their own node, a public mast), or who places
  nodes of their own near a target, learns nothing about the target.
- **Several nodes known to be at one site can be averaged.** Nodes at the same spot are shown at
  independent points. Someone who knows that N nodes really are at one site can average their
  served points and get closer than a single point allows. At the default `radiusM` of 1000:

  | Nodes at one site | Median error | 90th percentile |
  |---|---|---|
  | 1 | 404 m | 617 m |
  | 2 | 268 m | 454 m |
  | 4 | 184 m | 325 m |
  | 10 | 114 m | 208 m |

  The errors scale with `radiusM`. The served points do not show which nodes are at one site; the
  viewer has to know that from elsewhere (node names, for example).

So if you run several nodes on one mast and want more cover for that site, raise `radiusM`: at 2500,
four nodes at one site are located no better than one node is at 1000 (median error about 460 m).

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
  node narrows down where it really is. Rotate only if the secret has leaked, never on a schedule.
- **Keep advert signature validation on.** The ingestor checks advert signatures by default
  (`validateSignatures`, a top-level key of the ingestor config). Each node's grid is tied to its
  public key, so this is what stops someone else from using it. With validation off, anyone who can
  get adverts heard could send adverts under a real node's key from positions of their choice,
  watch where the served point changes, and find that node's grid. Position estimates built from
  companion reports, which are not signed, use a separate grid of their own, so a report someone
  submits for a key never reveals anything about that node's own grid.
- **Do not re-key a node in place.** A new keypair at the same site gives the node a new,
  independent point. Anyone who can tell that the old and the new key are the same node can
  compare the two points, as with several nodes at one site.
- **Startup refusal.** The server exits at startup when location privacy is enabled and the secret
  (from the config or `CORESCOPE_LOCATION_SECRET`) is missing or shorter than 32 bytes, or when
  `radiusM` is outside 100 to 50000.
- **Env var counts as intent.** If `CORESCOPE_LOCATION_SECRET` is set, the server starts only when
  location privacy is enabled or the block says `"enabled": false` explicitly. A missing block, a
  misspelled key or a missing `enabled` stops startup, so a supplied secret never leaves the
  instance serving exact positions.
- **Unparseable config.** If `config.json` cannot be parsed, the server refuses to start when
  `CORESCOPE_LOCATION_SECRET` is set, or when the raw file may turn location privacy on: a
  `locationPrivacy` block that does not read as off on its own (a quoted `"true"`, a second
  `enabled` or a second block saying `true`, or a block that is itself broken), or one the server
  cannot find its way to because a broken string earlier in the file hides it. Any other mention of
  `locationPrivacy` refuses too, unless it is plain text inside a string value such as a comment:
  a key that lost its colon or closing quote, or a misspelt key such as `locationPrivacyX`. A file
  whose every block reads as off (for example a copy of `config.example.json`, whose block is disabled, with a
  typo elsewhere) starts as before, as does a file without the block, or a missing or unreadable
  file without the env secret. To get past a refusal, fix the file, or set
  `"enabled": false` in the block.
- **Replicas.** Each replica keeps its own small in-memory state that stops a node flickering
  between cells near a cell edge. A node sitting exactly on a cell edge when a replica starts may
  therefore be shown at a slightly different approximate point by different replicas. Both points
  are still within the radius.

## Residual risks

- Someone who can inject adverts (a radio near an observer, or an MQTT feed) can map the grid of
  the nodes they inject by moving them and watching where the served points change. That is the
  grid of their own keys only, and it says nothing about any other node. This holds while the
  ingestor validates advert signatures (see Operations).
- The served points of several nodes known to be at one site, or of one site under an old and a
  new key, can be averaged (see
  [What it protects](#what-it-protects-and-what-it-does-not)).
- Restarts, replicas and eviction from the in-memory state can re-anchor a node that sits on a cell
  edge. Both points are within the radius, and comparing them says a little about where the node is.
- Rotating the secret lets old and new points be compared (see Operations).
- The origin of the companion cell grid can be recovered by bisecting the public coverage map. That
  limits a fix to its cell and nothing finer, which is the guarantee.

## FAQ

**Why not just round the coordinates?**
Rounding to a fixed grid gives a public, predictable cell, and nodes near an edge flip between
cells as they move or as GPS jitters. Here the grid origin and the offset of each cell come from the
secret and the node's public key, so the served point is not the cell centre or any public function
of the position, the grid is different for every node, and the distance to the real position is
bounded by `radiusM`.

**Why does each node get its own grid?**
With one grid for all nodes, every node in a cell would be shown at the same point. Anyone who can
get adverts heard could then place about six nodes of their own around a target's served point;
the one that came back with the identical point would give away the target's cell, 400 m across at
the default `radiusM`. About 25 adverts would map the whole grid. With a grid per public key, a
node someone else places says nothing about yours. The cost is that nodes at one site are shown at
separate points that can be averaged (see
[What it protects](#what-it-protects-and-what-it-does-not)); raise `radiusM` if that matters for
your site.

**Why is the planner less accurate?**
It works from the served positions, which are up to `radiusM` off. Short legs and fine distances
change the most. Lower `radiusM` for better accuracy and less privacy.

**Why are the coverage cells coarse?**
A companion fix is given a minimum uncertainty (a cell at least 2 x `radiusM` wide) rather than a
maximum error, so that repeated requests cannot place a fix better than its cell.

**How do I turn it off?**
Set `"enabled": false` (or remove the block) and restart. Exact positions are served again, since
the database was never changed.
