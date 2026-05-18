# RF Params Enrichment — Design Spec

**Date:** 2026-05-17
**Branch:** feat/rf-params-enrichment (to be created)

## Overview

Enrich node data with LoRa RF parameters (frequency, spreading factor, coding rate, bandwidth)
sourced from the official MeshCore map API. Parameters are shown in the nodes overview table
(single sortable "LoRa" column) and in the node detail panel. Only data ≤7 days old is
displayed; stale or absent entries are treated as null and shown as empty.

## Data Sources

**Node params:** `https://map.meshcore.io/api/v1/nodes`
- JSON array of ~45k nodes, ~33 MB uncompressed
- Relevant fields per node: `public_key`, `params` (`freq`, `sf`, `cr`, `bw`), `last_advert` (ISO timestamp)
- No per-node filter endpoint exists; the full list must be fetched each time

**Preset definitions:** `https://api.meshcore.nz/api/v1/config`
- Returns `config.suggested_radio_settings.entries[]` — the official community preset list
- Each entry has: `title`, `frequency`, `spreading_factor`, `bandwidth`, `coding_rate` (all strings)
- Fetched once at startup; no periodic refresh needed (presets change rarely)
- Netherlands (Narrow) is a local supplement not present in this API (see below)

## Backend — RF Params Cache (`rf_params_cache.go`)

A new file holds two structs and their background refresh logic, wired into `Server` at startup.

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

**Node params cache behaviour:**
- Fetched immediately on `Server` startup, then refreshed every 6 hours via a background goroutine
- On fetch failure: log the error, keep the previous index intact (no wipe on error)
- Parse only `public_key`, `params`, and `last_advert` from each entry; ignore all other fields
- Store all entries regardless of age; staleness is evaluated at serve time

**Freshness check (at serve time):**
- Parse `last_advert` as UTC
- If `time.Now().UTC().Sub(lastAdvert) > 7 * 24 * time.Hour` → return `nil`
- If `params` is absent in source data → return `nil`

**Preset cache behaviour:**
- Fetched once at startup from `https://api.meshcore.nz/api/v1/config`
- Parsed into a `map[string]string` keyed by the canonical param key (see key format below)
- Netherlands (Narrow) (`869.618/7/62.5/5` → `"Netherlands (Narrow)"`) is injected after fetch
  as a local override; it will not be overwritten if the official API ever adds a conflicting entry
  because the local entry is inserted last
- Switzerland entry (`869.618/8/62.5/8`) from the config API is deduplicated with
  EU/UK (Narrow) at build time → merged label `"EU/UK (Narrow) / Switzerland"`
- Preset map is served to the frontend via a new lightweight endpoint (see below)

## API Changes

**`/api/nodes` — enriched node objects:**

Each node gains one new optional field:

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

**`/api/rf-presets` — new endpoint:**

Returns the preset map so the frontend can resolve labels without hardcoding:

```json
{
  "869.618/8/62.5/8": "EU/UK (Narrow) / Switzerland",
  "869.618/7/62.5/5": "Netherlands (Narrow)",
  "910.525/7/62.5/5": "USA/Canada (Recommended)",
  ...
}
```

Key format: `"{freq}/{sf}/{bw}/{cr}"` using the same string values as in the config API
(3 decimal places for freq, 1 for bw). Frontend uses the same `toFixed` normalisation when
building a key from live `rf_params` floats.

This endpoint is fetched once by the frontend on page load (small payload, ~1 KB).

## Frontend — Nodes Table

A single new sortable column **"LoRa"** added to the nodes overview table.

**Preset key construction (avoids float equality bugs):**
`` `${freq.toFixed(3)}/${sf}/${bw.toFixed(1)}/${cr}` `` e.g. `"869.618/8/62.5/8"`.
Matches the key format served by `/api/rf-presets`.

**Cell rendering logic:**
- `rf_params === null` → empty cell
- key found in preset map → show preset label (e.g. `EU/UK (Narrow) / Switzerland`)
- key not found → show raw summary: `{freq} SF{sf}` (e.g. `910.5 SF9`)

**Sort behaviour:**
- String sort on the rendered cell value
- Empty cells (null rf_params) always sort last, regardless of sort direction
- Uses the existing `TableSort` utility with a null-last comparator

## Frontend — Node Detail Panel

A new **"RF Parameters"** section appears below the existing info rows, only when `rf_params`
is non-null.

**Contents:**
- Preset name if key found in preset map (omitted if no match)
- Four individual value rows: Freq, SF, BW, CR
- Muted footer line: `via MeshCore Map · last advert <relative age>` (e.g. "2 days ago")

## Testing

- Unit test for `RFParamsCache` freshness: >7 days → nil, ≤7 days → returned
- Unit test for fetch failure: node cache retains previous data on error
- Unit test for `/api/nodes` enrichment: node with matching pubkey gets `rf_params` populated
- Unit test for `/api/rf-presets`: Netherlands (Narrow) present, Switzerland merged into EU/UK label
- Frontend unit test for preset lookup: known key returns label, unknown returns raw summary,
  null returns empty string
- Frontend unit test for null-last sort: nodes without rf_params sort to bottom in both asc/desc

## Out of Scope

- Periodic refresh of the preset list (presets change rarely; startup fetch is sufficient)
- Persisting either cache to disk across restarts (cold start re-fetches both)
- RF params on the map view
