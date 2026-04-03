# Customizer Rework Spec

## Overview

The current customizer (`public/customize.js`) suffers from fundamental state management issues documented in [issue #284](https://github.com/Kpa-clawbot/CoreScope/issues/284). State is scattered across 7 localStorage keys, CSS updates bypass the data layer, and there's no single source of truth for the effective configuration.

This spec defines a clean rework based on event-driven state management with a single data flow path. The goal: predictable state, minimal storage footprint, portable config format, and zero ambiguity about which values are active and why.

## Design Decisions

These are agreed and final. Do not reinterpret or deviate.

1. **Three state layers:** server defaults (immutable after fetch), user overrides (delta in localStorage), effective config (computed via merge, never stored directly).
2. **Single data flow:** user action → debounce (~300ms) → write delta to localStorage → read back from localStorage → merge with server defaults → apply CSS variables. No shortcuts, no optimistic CSS updates.
3. **One localStorage key:** `cs-theme-overrides` — replaces the current 7 scattered keys (`meshcore-user-theme`, `meshcore-timestamp-mode`, `meshcore-timestamp-timezone`, `meshcore-timestamp-format`, `meshcore-timestamp-custom-format`, `meshcore-heatmap-opacity`, `meshcore-live-heatmap-opacity`).
4. **Universal format:** same shape as the server's `ThemeResponse` plus additional keys. Works identically for user export, admin `theme.json`, and user import.
5. **User overrides always win** in merge — `merge(serverDefaults, userOverrides)` = effective config.
6. **Override indicator:** shown in customizer panel ONLY when override value differs from current server default.
7. **Silent pruning:** if an override value matches the server default, remove it from the delta. Prune on page load (after fetching server config) and during each merge cycle. Keeps delta minimal.
8. **Per-field reset:** remove a single key from the delta → re-merge → re-apply CSS.
9. **Full reset:** `localStorage.removeItem('cs-theme-overrides')` → re-merge (effective = server defaults) → re-apply CSS.
10. **Export = dump delta object as JSON download. Import = validate shape, write to localStorage, trigger re-merge.**
11. **No CSS magic:** CSS variables ONLY update after the localStorage round-trip completes. No optimistic updates.

## Data Model

### Delta Object Format

The user override delta is a sparse object — it only contains fields the user has explicitly changed that differ from server defaults. The shape mirrors the server's `ThemeResponse` (from `/api/config/theme`) plus additional client-only sections:

```json
{
  "branding": {
    "siteName": "string — site name override",
    "tagline": "string — tagline override",
    "logoUrl": "string — custom logo URL",
    "faviconUrl": "string — custom favicon URL"
  },
  "theme": {
    "accent": "string — CSS color, light mode accent",
    "accentHover": "string — CSS color, light mode accent hover",
    "navBg": "string — CSS color, nav background",
    "navBg2": "string — CSS color, nav secondary background",
    "navText": "string — CSS color, nav text",
    "navTextMuted": "string — CSS color, nav muted text",
    "background": "string — CSS color, page background",
    "text": "string — CSS color, body text",
    "textMuted": "string — CSS color, muted text",
    "border": "string — CSS color, borders",
    "surface1": "string — CSS color, surface level 1",
    "surface2": "string — CSS color, surface level 2",
    "cardBg": "string — CSS color, card backgrounds",
    "contentBg": "string — CSS color, content area background",
    "detailBg": "string — CSS color, detail pane background",
    "inputBg": "string — CSS color, input backgrounds",
    "rowStripe": "string — CSS color, alternating row stripe",
    "rowHover": "string — CSS color, row hover highlight",
    "selectedBg": "string — CSS color, selected row background",
    "statusGreen": "string — CSS color, healthy status",
    "statusYellow": "string — CSS color, degraded status",
    "statusRed": "string — CSS color, critical status",
    "font": "string — CSS font-family for body text",
    "mono": "string — CSS font-family for monospace"
  },
  "themeDark": {
    "/* same keys as theme — dark mode overrides */"
  },
  "nodeColors": {
    "repeater": "string — CSS color",
    "companion": "string — CSS color",
    "room": "string — CSS color",
    "sensor": "string — CSS color",
    "observer": "string — CSS color"
  },
  "typeColors": {
    "ADVERT": "string — CSS color",
    "GRP_TXT": "string — CSS color",
    "TXT_MSG": "string — CSS color",
    "ACK": "string — CSS color",
    "REQUEST": "string — CSS color",
    "RESPONSE": "string — CSS color",
    "TRACE": "string — CSS color",
    "PATH": "string — CSS color",
    "ANON_REQ": "string — CSS color"
  },
  "home": {
    "heroTitle": "string",
    "heroSubtitle": "string",
    "steps": "[array of {emoji, title, description}]",
    "checklist": "[array of strings]",
    "footerLinks": "[array of {label, url}]"
  },
  "timestamps": {
    "defaultMode": "string — 'ago' | 'absolute'",
    "timezone": "string — 'local' | 'utc'",
    "formatPreset": "string — 'iso' | 'iso-seconds' | 'locale'",
    "customFormat": "string — custom strftime-style format"
  },
  "heatmapOpacity": "number — 0.0 to 1.0",
  "liveHeatmapOpacity": "number — 0.0 to 1.0"
}
```

**Rules:**
- All sections and keys are optional. An empty object `{}` means "no overrides."
- Only keys that differ from server defaults are stored (enforced by pruning).
- The `timestamps`, `heatmapOpacity`, and `liveHeatmapOpacity` keys are client-only extensions — not part of the server's `ThemeResponse`, but included in the universal format for portability.

### localStorage Key

**Key:** `cs-theme-overrides`
**Value:** JSON string of the delta object above.
**Absent key** = no overrides = effective config equals server defaults.

## Data Flow Diagrams

### Page Load

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────────┐
│ Fetch        │     │ Read localStorage │     │ Migration check │
│ /api/config/ │     │ cs-theme-overrides│     │ (one-time)      │
│ theme        │     └────────┬─────────┘     └────────┬────────┘
└──────┬──────┘              │                         │
       │                     │    ┌────────────────────┘
       ▼                     ▼    ▼
  serverDefaults      userOverrides (possibly migrated)
       │                     │
       ▼                     ▼
  ┌──────────────────────────────┐
  │ pruneOverrides(server, user) │ ← remove keys matching defaults
  └──────────────┬───────────────┘
                 │
                 ▼ (pruned delta written back)
  ┌──────────────────────────────────────┐
  │ computeEffective(server, prunedUser) │
  └──────────────┬───────────────────────┘
                 │
                 ▼
  ┌──────────────────────┐
  │ applyCSS(effective)  │ ← sets CSS vars on :root
  └──────────────────────┘
```

### User Change (e.g., picks new accent color)

```
  User action (input/click)
       │
       ▼
  debounce(300ms)
       │
       ▼
  setOverride('theme', 'accent', '#ff0000')
       │
       ├─► readOverrides()          ← read current delta from localStorage
       │       │
       │       ▼
       ├─► update delta object      ← set delta.theme.accent = '#ff0000'
       │       │
       │       ▼
       ├─► pruneOverrides(server, delta)  ← if value matches server default, remove it
       │       │
       │       ▼
       ├─► writeOverrides(delta)    ← serialize & write to localStorage
       │       │
       │       ▼
       ├─► readOverrides()          ← read BACK from localStorage (round-trip)
       │       │
       │       ▼
       ├─► computeEffective(server, delta)
       │       │
       │       ▼
       └─► applyCSS(effective)      ← CSS vars updated on :root
```

### Per-Field Reset

```
  User clicks reset icon on a field
       │
       ▼
  clearOverride('theme', 'accent')
       │
       ├─► readOverrides()
       ├─► delete delta.theme.accent
       ├─► if delta.theme is empty, delete delta.theme
       ├─► writeOverrides(delta)
       ├─► readOverrides()          ← round-trip
       ├─► computeEffective(server, delta)
       └─► applyCSS(effective)
```

### Full Reset

```
  User clicks "Reset All"
       │
       ▼
  localStorage.removeItem('cs-theme-overrides')
       │
       ▼
  computeEffective(server, {})    ← no overrides = server defaults
       │
       ▼
  applyCSS(effective)
```

### Export

```
  User clicks "Export"
       │
       ▼
  readOverrides()
       │
       ▼
  JSON.stringify(delta, null, 2)
       │
       ▼
  trigger download as .json file
```

### Import

```
  User selects .json file
       │
       ▼
  parse JSON
       │
       ▼
  validateShape(parsed)           ← check structure, reject unknown top-level keys
       │
       ├─► invalid → show error, abort
       │
       ▼ valid
  writeOverrides(parsed)
       │
       ▼
  readOverrides()                 ← round-trip
       │
       ▼
  pruneOverrides(server, delta)   ← prune matches
       │
       ▼
  writeOverrides(prunedDelta)
       │
       ▼
  computeEffective(server, prunedDelta)
       │
       ▼
  applyCSS(effective)
```

## Function Signatures

### `readOverrides() → object`

Reads `cs-theme-overrides` from localStorage, parses as JSON. Returns empty object `{}` on missing key, parse error, or non-object value. Never throws.

### `writeOverrides(delta: object) → void`

Serializes `delta` to JSON and writes to `cs-theme-overrides` in localStorage. If `delta` is empty (`{}`), removes the key entirely.

### `computeEffective(serverConfig: object, userOverrides: object) → object`

Deep merges `userOverrides` onto `serverConfig`. For each section (e.g., `theme`, `nodeColors`), if `userOverrides` has the section, its keys override the corresponding `serverConfig` keys. Top-level non-object keys (e.g., `heatmapOpacity`) are directly overridden.

Returns a new object — neither input is mutated.

**Merge rules:**
- Object sections: shallow merge per section (`Object.assign({}, server.theme, user.theme)`)
- Array sections (e.g., `home.steps`): full replacement (user array wins entirely, no element-level merge)
- Scalar sections (e.g., `heatmapOpacity`): direct replacement

### `applyCSS(effectiveConfig: object) → void`

Maps effective config values to CSS custom properties on `:root` using the existing `THEME_CSS_MAP` mapping. Also applies:
- Node colors as `--node-{role}` variables
- Type colors as `--type-{name}` variables
- Font families as `--font-body` and `--font-mono`
- Dark mode values via the existing media query / class mechanism

Updates the `<style>` element (create if absent, reuse if present). Dispatches a `theme-changed` CustomEvent on `window` after applying.

### `setOverride(section: string, key: string, value: any) → void`

Sets a single override. For nested sections (e.g., `section='theme'`, `key='accent'`), sets `delta[section][key] = value`. For top-level scalars (e.g., `section=null`, `key='heatmapOpacity'`), sets `delta[key] = value`.

Follows the full data flow: read → update → prune → write → read-back → merge → apply CSS. Debounced at ~300ms (the debounce wraps the write-through-to-CSS portion).

### `clearOverride(section: string, key: string) → void`

Removes a single key from the delta. If the section becomes empty after removal, removes the section too. Triggers the full data flow (no debounce — resets should feel instant).

### `pruneOverrides(serverConfig: object, userOverrides: object) → object`

Compares each key in `userOverrides` against `serverConfig`. If values are deeply equal, removes the key from the result. Returns a new pruned delta object. Removes empty sections.

**Comparison rules:**
- Strings: strict equality
- Numbers: strict equality
- Arrays: JSON.stringify comparison (order-sensitive)
- Objects: recursive key-by-key comparison

### `migrateOldKeys() → object | null`

One-time migration. Checks for any of the 7 legacy localStorage keys. If found:
1. Reads all legacy values
2. Maps them into the new delta format
3. Writes the merged delta to `cs-theme-overrides`
4. Removes all 7 legacy keys
5. Returns the migrated delta

Returns `null` if no legacy keys found.

### `validateShape(obj: any) → { valid: boolean, errors: string[] }`

Validates that an imported object conforms to the expected shape:
- Must be a plain object
- Top-level keys must be from the known set: `branding`, `theme`, `themeDark`, `nodeColors`, `typeColors`, `home`, `timestamps`, `heatmapOpacity`, `liveHeatmapOpacity`
- Section values must be objects (where expected) or correct scalar types
- Does NOT validate individual color values (any string is accepted)

Unknown top-level keys cause a warning but don't fail validation (forward compatibility).

## Migration Plan

On first page load, before the normal init flow:

1. Check if `cs-theme-overrides` already exists → if yes, skip migration.
2. Check if ANY of the 7 legacy keys exist in localStorage.
3. If legacy keys found, build a delta object:

| Legacy Key | Maps To |
|---|---|
| `meshcore-user-theme` | Parse as JSON, spread into appropriate sections (`theme`, `nodeColors`, `typeColors`, `branding`, `home`) |
| `meshcore-timestamp-mode` | `timestamps.defaultMode` |
| `meshcore-timestamp-timezone` | `timestamps.timezone` |
| `meshcore-timestamp-format` | `timestamps.formatPreset` |
| `meshcore-timestamp-custom-format` | `timestamps.customFormat` |
| `meshcore-heatmap-opacity` | `heatmapOpacity` (parse as float) |
| `meshcore-live-heatmap-opacity` | `liveHeatmapOpacity` (parse as float) |

4. Write the assembled delta to `cs-theme-overrides`.
5. Delete all 7 legacy keys.
6. Prune the delta against server defaults (some migrated values may match).
7. Continue with normal init.

**Edge cases:**
- If `meshcore-user-theme` contains invalid JSON, skip it (log a warning to console).
- If a legacy value is empty string or null, skip that field.
- Migration runs exactly once — the presence of `cs-theme-overrides` (even as `{}`) prevents re-migration.

## Override Indicator UX

In the customizer panel, each field that has an active override (value differs from server default) shows a visual indicator:

- **Indicator:** A small dot or icon (e.g., `●` or a reset arrow `↺`) adjacent to the field label.
- **Color:** Use the accent color to draw attention without being noisy.
- **Behavior:** Clicking the indicator resets that single field (calls `clearOverride`).
- **Tooltip:** "Reset to server default" or "This value differs from the server default."
- **Absence:** Fields matching the server default show no indicator — clean and minimal.

**Section-level indicator:** If any field in a section (e.g., "Theme Colors") is overridden, the tab/section header shows a count badge (e.g., "Theme Colors (3)").

**"Reset All" button:** Always visible at bottom of panel. Confirms before executing (`localStorage.removeItem` + re-merge).

## Server Compatibility

The delta format is intentionally shaped to be a valid subset of the server's `theme.json` admin config file. This means:

- **User export → admin import:** An admin can take a user's exported JSON and drop it into `theme.json` as server defaults. The `timestamps`, `heatmapOpacity`, and `liveHeatmapOpacity` keys are ignored by the current server (it doesn't read them from `theme.json`), but they don't cause errors.
- **Admin config → user import:** A `theme.json` file can be imported as user overrides. Unknown server-only keys are ignored by the client.
- **Round-trip safe:** Export → import produces identical delta (assuming no server default changes between operations).

The server's `ThemeResponse` struct currently returns: `branding`, `theme`, `themeDark`, `nodeColors`, `typeColors`, `home`. The client-only extensions (`timestamps`, `heatmapOpacity`, `liveHeatmapOpacity`) are additive — they extend the format without conflicting.

## Testing Requirements

### Unit Tests (Node.js, no browser required)

1. **`readOverrides`**
   - Returns `{}` when key is absent
   - Returns `{}` when key contains invalid JSON
   - Returns `{}` when key contains a non-object (string, array, number)
   - Returns parsed object when key contains valid JSON object

2. **`writeOverrides`**
   - Writes serialized JSON to localStorage
   - Removes key when delta is empty `{}`
   - Round-trips correctly (write → read = identical object)

3. **`computeEffective`**
   - Returns server defaults when overrides is `{}`
   - Overrides a single key in a section
   - Overrides multiple keys across sections
   - Does not mutate either input
   - Handles missing sections in overrides gracefully
   - Array values (e.g., `home.steps`) are fully replaced, not merged
   - Top-level scalars (`heatmapOpacity`) are directly replaced

4. **`pruneOverrides`**
   - Removes keys that match server defaults
   - Keeps keys that differ from server defaults
   - Removes empty sections after pruning
   - Returns `{}` when all overrides match defaults
   - Handles nested object comparison correctly
   - Handles array comparison correctly (order-sensitive)

5. **`setOverride` / `clearOverride`**
   - Setting a value stores it in the delta
   - Setting a value that matches server default prunes it
   - Clearing a key removes it from delta
   - Clearing the last key in a section removes the section
   - Full data flow executes (CSS vars updated)

6. **`migrateOldKeys`**
   - Migrates all 7 keys correctly
   - Handles partial migration (only some keys present)
   - Handles invalid JSON in `meshcore-user-theme`
   - Removes all legacy keys after migration
   - Skips migration if `cs-theme-overrides` already exists
   - Returns null when no legacy keys found

7. **`validateShape`**
   - Accepts valid delta objects
   - Accepts empty object
   - Rejects non-objects (string, array, null)
   - Warns on unknown top-level keys (doesn't reject)
   - Validates section types (object vs scalar)

### Browser/E2E Tests (Playwright)

1. **Customizer opens and shows current values** — fields reflect effective config.
2. **Changing a color updates CSS variable** — after debounce, `:root` has new value.
3. **Override indicator appears** when value differs from server default.
4. **Per-field reset** removes override, reverts to server default, indicator disappears.
5. **Full reset** clears all overrides, all fields show server defaults.
6. **Export** downloads a JSON file with current delta.
7. **Import** applies overrides from uploaded JSON file.
8. **Migration** — set legacy keys, reload, verify they're migrated and removed.

## What's NOT In Scope

- **Server-side timestamp config** (`allowCustomFormat` gate) — remains server-only, not exposed in the customizer delta. Future work.
- **Admin import endpoint** — no server API for uploading `theme.json` via the UI. Admins edit the file directly. Future work.
- **Map config overrides** (`mapDefaults.center`, `mapDefaults.zoom`) — separate concern, not part of theme. Future work.
- **Geo-filter config** — server-only. Not in scope.
- **Per-page layout preferences** (column widths, sort orders) — separate from theming. Future work.
- **Multi-theme presets** (e.g., "Solarized", "Dracula") — could be built on top of import/export, but not in this rework.
- **Real-time sync across tabs** — `storage` event listening for cross-tab sync is a nice-to-have but not required for v1. The data flow supports it naturally if added later.
