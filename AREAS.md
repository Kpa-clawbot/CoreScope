# Areas

`config.json`'s `areas` map lets you draw named geographic regions — cities,
regions, whole countries, even continents — and have CoreScope automatically
count which nodes fall inside each one, roll counts up through nested areas
(a city inside a country inside a continent), and (optionally) classify
foreign vs. domestic traffic from the same boundaries.

There is no per-deployment code involved. Everything below is `config.json`
content — the areas feature itself (`AreaEntry`, `AreaForPoint`,
`AreaKeyForPoint`, `AreaKeysForPoint`, `HomeArea`, `computeScopeAdoptionByArea`
in `cmd/server/`) is generic and works the same regardless of what regions you
draw or which country you run CoreScope in.

## Defining an area

```json
"areas": {
  "DK": {
    "label": "Danmark (alle)",
    "regionScopes": ["dk"],
    "polygon": [[54.85, 8.65], [55.50, 8.10], [57.10, 8.20], ...]
  },
  "AAR": {
    "label": "Aarhus by",
    "regionScopes": ["dk-aarhus"],
    "polygon": [[56.35, 10.33], [56.31, 10.45], ...]
  },
  "EU": {
    "label": "Europa (alle)",
    "regionScopes": ["eu", "europe"],
    "latMin": 34.0, "latMax": 71.5, "lonMin": -25.0, "lonMax": 45.0
  }
}
```

Each entry has three parts:

- **`label`** — the human-readable name shown in the UI.
- **Geometry** — either:
  - `polygon`: a list of `[lat, lon]` points tracing a real boundary
    (coastline, border). Use this when you care about precision — the
    boundary between two adjacent countries/regions especially, since a
    simple box will bleed across it.
  - `latMin` / `latMax` / `lonMin` / `lonMax`: a plain bounding box. Good
    enough for a rough first pass, or for areas with no close neighbor to
    worry about overlapping (e.g. a whole continent).

  If `polygon` has at least 3 points, it's used; otherwise the code falls
  back to the box. An area can have one or the other, not both meaningfully
  at once.
- **`regionScopes`** (optional) — links this area to one or more hashRegions
  channel scopes (e.g. `["dk-aarhus"]`, each stored *without* the leading
  `#`). This powers the Scopes tab's "Scope Adoption by Area" section: which
  nodes physically in this area actually use (via their own `default_scope`,
  or by relaying it) *any* of the regions this area represents. Most areas
  only need one scope, but a broad umbrella area can link several — e.g.
  Europa linking both `"eu"` and `"europe"` if both names see real traffic —
  and a node matching any one of them counts as supporting the area. Leave
  it unset/empty if there's no matching hashRegion — the area still works
  for everything else (badges, node counts, the area filter), it just won't
  have anything to compare scope-adoption against.

## Hierarchy: draw it, don't declare it

**There is no `"parent"` field.** An area doesn't know it's "inside" another
area — that's worked out purely from geometry, every time a node needs to be
counted: for each area, does the node's `(lat, lon)` fall inside its
geometry? If yes, the node counts toward that area. A node in Aarhus falls
inside `AAR`'s polygon *and* a broader `JYL` (Jylland) polygon *and* `DK`
*and* `EU`, simultaneously — no code anywhere needs to know those areas are
related, and none of them need to enumerate their members.

Practically, this means:

- To make a country-level area (e.g. "Danmark") show the *whole country's*
  totals rather than just the leftover nodes no smaller area already
  claimed, its geometry must actually contain those smaller areas'
  geometry. Draw the country boundary generously enough to cover all its
  regions/cities and it just works.
- Adding a new area — a new city, region, or country — never requires
  touching any other area's config, or any code. Draw its boundary, add the
  entry, restart. If it geographically sits inside an existing broader
  area, it's automatically included in that area's totals from the next
  restart on.
- The same applies at any scale: adding e.g. "Finland" as a new country
  area automatically starts contributing to "Europa (alle)" the moment its
  polygon is added, with zero changes to the Europe entry.

Two different lookups use this geometry, for different purposes:

- **Single most-specific match** (`AreaForPoint` / `AreaKeyForPoint`) — used
  for per-node badges (Wardriving tab's GPS-share/session area tags): picks
  the *smallest* matching area, so a node in Aarhus is labeled "Aarhus by",
  not "Danmark".
- **All containing areas** (`AreaKeysForPoint`) — used for aggregate counts
  (Scope Adoption by Area): a node counts toward *every* area it
  geographically sits inside, so country/continent totals genuinely
  aggregate their sub-areas instead of only showing leftovers.

## `homeArea`: linking foreign/domestic classification to an area

```json
"homeArea": "DK"
```

`homeArea` names an entry in `areas` whose geometry becomes the effective
`geo_filter` — the boundary the Foreign Traffic tab, the Nodes page
All/Domestic/Foreign filter, and the live map's declutter logic all use to
decide "is this node ours or foreign".

Before `homeArea` existed, `geo_filter` was a second, independently-drawn
boundary — easy to let drift out of sync with whatever the "home" area
actually looked like (this happened in practice: a too-loose home-country
box quietly claimed a neighboring country's nodes as domestic, and fixing
the area's polygon didn't fix `geo_filter` until this field existed).

Set `homeArea` to the key of whichever area represents "home" for your
deployment. Leave it unset (or pointing at a key that doesn't exist in
`areas`) to keep using a standalone `geo_filter` value exactly as before —
this is fully backward compatible.

## Adding a new area

1. Get a boundary. A rough bounding box is a fine starting point
   (`latMin`/`latMax`/`lonMin`/`lonMax`); upgrade to a `polygon` later if it
   turns out to overlap a neighbor.
2. Add it under `areas` in `config.json`.
3. Restart CoreScope — config is only read at startup, there's no hot-reload.
4. Done. It's picked up everywhere automatically: the area filter dropdown,
   per-node badges, Scope Adoption by Area, and (if it's nested inside a
   broader area) that broader area's totals.

If you draw a `polygon`, verify it before deploying: fetch
`/api/nodes?limit=5000` and `/api/config/areas/polygons`, and run every
node you know belongs on each side of the new boundary through a
point-in-polygon check (ray-casting — see `AreaForPoint` in
`cmd/server/config.go` for the exact algorithm CoreScope uses) to catch
bleed across a shared border before it ships. A box is more forgiving of
imprecision than a polygon since there's usually nothing on the other side
of an unclaimed edge to misclassify — but two adjacent countries sharing a
long border will bleed into each other badly with simple boxes, which is
why Denmark/Sweden/Norway/Germany all ended up as polygons.
