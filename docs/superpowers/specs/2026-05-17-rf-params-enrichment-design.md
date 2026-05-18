# RF Params Enrichment — Design Spec

**Date:** 2026-05-17
**Branch:** feat/rf-params-enrichment (to be created)

## Overview

Enrich node data with LoRa RF parameters (frequency, spreading factor, coding rate, bandwidth)
sourced from the official MeshCore map API. Parameters are shown in the nodes overview table
(single sortable "LoRa" column) and in the node detail panel. Only data ≤7 days old is
displayed; stale or absent entries are treated as null and shown as empty.

## Data Source

- **URL:** `https://map.meshcore.io/api/v1/nodes`
- **Response:** JSON array of ~45k nodes, ~33 MB uncompressed
- **Relevant fields per node:** `public_key`, `params` (`freq`, `sf`, `cr`, `bw`), `last_advert` (ISO timestamp)
- No per-node filter endpoint exists; the full list must be fetched each time

## Backend — RF Params Cache (`rf_params_cache.go`)

A new file holds a single exported cache struct, wired into `Server` at startup.

**Structs:**
```go
type RFParams struct {
    Freq       float64 `json:"freq"`
    SF         int     `json:"sf"`
    CR         int     `json:"cr"`
    BW         float64 `json:"bw"`
    LastAdvert string  `json:"last_advert"`
}

type RFParamsCache struct {
    mu    sync.RWMutex
    index map[string]*RFParams // keyed by public_key
}
```

**Behaviour:**
- Fetched immediately on `Server` startup, then refreshed every 6 hours via a background goroutine
- On fetch failure: log the error, keep the previous index intact (no wipe on error)
- Parse only `public_key`, `params`, and `last_advert` from each entry; ignore all other fields
- Store all entries regardless of age; staleness is evaluated at serve time

**Freshness check (at serve time):**
- Parse `last_advert` as UTC
- If `time.Now().UTC().Sub(lastAdvert) > 7 * 24 * time.Hour` → return `nil`
- If `params` is absent in source data → return `nil`

## API Change — `/api/nodes`

Each node object in the response gains one new optional field:

```json
"rf_params": {
  "freq": 869.618,
  "sf": 8,
  "cr": 8,
  "bw": 62.5,
  "last_advert": "2026-05-18T11:31:10.000Z"
}
```

- `null` when no match found, params absent in source, or `last_advert` > 7 days ago
- No other API changes; no new endpoints

## Frontend — Nodes Table

A single new sortable column **"LoRa"** added to the nodes overview table.

**Preset lookup table (client-side, hardcoded in `nodes.js`):**

| freq / sf / bw / cr | Label |
|---|---|
| 915.800 / SF10 / BW250 / CR5 | Australia |
| 916.575 / SF7 / BW62.5 / CR8 | Australia (Narrow) |
| 915.075 / SF9 / BW125 / CR5 | Australia (Mid) |
| 923.125 / SF8 / BW62.5 / CR8 | Australia: SA, WA |
| 923.125 / SF8 / BW62.5 / CR5 | Australia: QLD |
| 869.618 / SF8 / BW62.5 / CR8 | EU/UK (Narrow) / Switzerland |
| 869.525 / SF11 / BW250 / CR5 | EU/UK (Deprecated) |
| 869.432 / SF7 / BW62.5 / CR5 | Czech Republic (Narrow) |
| 433.650 / SF11 / BW250 / CR5 | EU 433MHz (Long Range) |
| 433.650 / SF8 / BW62.5 / CR8 | EU 433MHz (Narrow) |
| 917.375 / SF11 / BW250 / CR5 | New Zealand |
| 917.375 / SF7 / BW62.5 / CR5 | New Zealand (Narrow) |
| 433.375 / SF9 / BW62.5 / CR6 | Portugal 433 |
| 869.618 / SF7 / BW62.5 / CR5 | Netherlands (Narrow) |
| 869.618 / SF7 / BW62.5 / CR6 | Portugal 868 |
| 910.525 / SF7 / BW62.5 / CR5 | USA/Canada (Recommended) |
| 920.250 / SF8 / BW62.5 / CR5 | Vietnam (Narrow) |
| 920.250 / SF11 / BW250 / CR5 | Vietnam (Deprecated) |

Notes:
- Switzerland and EU/UK (Narrow) share identical params; they are merged into one label.
- Netherlands (Narrow) (869.618/SF7/BW62.5/CR5) is a custom addition not in the official app preset list.

**Preset key construction (avoid float equality bugs):**
Build a string key from the params: `` `${freq.toFixed(3)}/${sf}/${bw.toFixed(1)}/${cr}` ``
e.g. `"869.618/8/62.5/8"`. The hardcoded preset table uses the same format as keys.
Both `freq` and `bw` come from the API as simple decimals; `toFixed` normalises any
float representation drift.

**Cell rendering logic:**
- `rf_params === null` → empty cell
- params match a preset → show preset label (e.g. `EU/UK (Narrow) / Switzerland`)
- params present but no preset match → show raw summary: `{freq} SF{sf}` (e.g. `910.5 SF9`)

**Sort behaviour:**
- String sort on the rendered cell value
- Empty cells (null rf_params) always sort last, regardless of sort direction
- Uses the existing `TableSort` utility with a null-last comparator

## Frontend — Node Detail Panel

A new **"RF Parameters"** section appears below the existing info rows, only when `rf_params`
is non-null.

**Contents:**
- Preset name if recognized (omitted if no preset match)
- Four individual value rows: Freq, SF, CR, BW
- Muted footer line: `via MeshCore Map · last advert <relative age>` (e.g. "2 days ago")

## Testing

- Unit test for `RFParamsCache` freshness: >7 days → nil, ≤7 days → returned
- Unit test for fetch failure: cache retains previous data on error
- Unit test for `/api/nodes` enrichment: node with matching pubkey gets `rf_params` populated
- Frontend unit test for preset lookup: known combos return label, unknown returns raw summary,
  null returns empty string
- Frontend unit test for null-last sort: nodes without rf_params sort to bottom in both asc/desc

## Out of Scope

- Admin UI for managing presets (hardcoded table is sufficient for now)
- Persisting the cache to disk across restarts (cold start re-fetches)
- A dedicated `/api/rf-params` endpoint
- RF params on the map view
