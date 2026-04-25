# Multibyte Capability Map Overlay — Design Spec

**Issue:** [#903](https://github.com/Kpa-clawbot/CoreScope/issues/903)
**Date:** 2026-04-25

## Overview

Add a toggle to the map controls that overlays multibyte-capability status on repeater markers. When active, markers are colored by evidence: confirmed (solid green), suspected (light green dashed), or unknown (dimmed gray). Status is derived from existing server-side capability analysis and persisted to the database so no startup scan is needed.

---

## Data Layer

### Migration

Two new columns on the `nodes` table:

```sql
ALTER TABLE nodes ADD COLUMN multibyte_sup INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN multibyte_evidence TEXT;
```

**`multibyte_sup`** tri-state:

| Value | Meaning |
|---|---|
| `0` | Unknown — no evidence seen |
| `1` | Suspected — node prefix appeared as a hop in a multibyte-path packet |
| `2` | Confirmed — node sent a multibyte advert (hash_size ≥ 2) directly |

**`multibyte_evidence`**: informational string — `"advert"`, `"path"`, or `NULL`.

### Write-back rule

Status only moves forward, never backward:

```sql
UPDATE nodes
SET multibyte_sup = ?, multibyte_evidence = ?
WHERE public_key = ? AND multibyte_sup < ?
```

A confirmed node (`2`) is never overwritten by suspected (`1`) or unknown (`0`). Rows already at the target level are skipped entirely by the `< ?` guard, so write-back quickly becomes a no-op for stable networks.

---

## Server

### Write-back function

New function in `store.go`:

```go
func (s *Store) persistMultiByteCapability(entries []MultiByteCapEntry) error
```

Called at the end of the existing `computeMultiByteCapability()` analytics flow, after the in-memory result is cached. Executes one `UPDATE` per node that needs upgrading. Because this runs on the existing ~15 s analytics cache cycle and the eligible set shrinks over time, it stays cheap.

`computeMultiByteCapability()` already distinguishes:
- **Confirmed** (`evidence = "advert"`) — node's own advert had hash_size ≥ 2
- **Suspected** (`evidence = "path"`) — node prefix appeared as a hop in a multibyte-path packet (TRACE packets excluded to avoid false positives)

Both map to `multibyte_sup` values 2 and 1 respectively.

### Node enrichment

Two fields added to every node object in the `/api/nodes` response:
- `multibyte_sup` — integer 0/1/2 (read from DB column, zero value if column absent)
- `multibyte_evidence` — `"advert"` / `"path"` / `null`

No new API endpoint. The columns are already fetched as part of the existing `nodes` row query — pass them through in `EnrichNodeWithHashSize` or alongside it.

### No changes to:
- Ingestor or packet ingestion path
- Existing analytics endpoints
- Any existing node DB writes

---

## Frontend

### State

```js
filters = {
  ...,
  multibyteOverlay: localStorage.getItem('meshcore-map-multibyte') === 'true'
}
```

Persisted in `localStorage`, same pattern as `byteSize` and `hashLabels`.

### Toggle placement

Added under the existing **Byte Size** `<fieldset>` in the map controls panel:

```html
<fieldset class="mc-section">
  <legend class="mc-label">Byte Size</legend>
  <div class="filter-group" id="mcByteFilter">
    <!-- existing All / 1-byte / 2-byte / 3-byte buttons -->
  </div>
  <label for="mcMultibyte">
    <input type="checkbox" id="mcMultibyte"> Show multibyte capability
  </label>
</fieldset>
```

### Marker styling

Applied in the existing marker render path, only when `filters.multibyteOverlay === true`, and **only for repeater nodes** (same scope as the byte-size filter — companion, room, sensor, and observer nodes are unaffected). Based on `node.multibyte_sup`:

| `multibyte_sup` | Marker style |
|---|---|
| `2` confirmed | Solid bright green fill (`#22c55e`), green border (`#16a34a`) |
| `1` suspected | Light green fill (`#86efac`), dashed green border (`#22c55e`) |
| `0` unknown | Existing role-based fill color unchanged, opacity reduced to `0.45` |

When the toggle is **OFF**, all markers render exactly as today — no style changes.

### Tooltip / popup

When the overlay is active and a node is clicked, the popup shows the evidence label:
- `multibyte_sup = 2` → "Multibyte: confirmed (advert)"
- `multibyte_sup = 1` → "Multibyte: suspected (path)"
- `multibyte_sup = 0` → "Multibyte: not detected"

---

## CPU / Performance Constraints

- No startup scan. Status is read from the DB column, which persists across restarts.
- Write-back runs on the existing analytics cache cycle (~15 s), not on packet arrival.
- The no-downgrade guard (`multibyte_sup < ?`) ensures write-back becomes a no-op for nodes that have settled — cost decreases over time.
- No bulk reprocessing at any point.

---

## Out of Scope

- Ingestor changes — multibyte detection is entirely server-side.
- New API endpoints — all data flows through the existing `/api/nodes` response.
- Retroactive backfill on install — the overlay populates naturally as the analytics cycle runs. No migration backfill query needed.
