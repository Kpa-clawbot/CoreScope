# Customizer Rework Spec

## Overview

The current customizer (`public/customize.js`) suffers from fundamental state management issues documented in [issue #284](https://github.com/Kpa-clawbot/CoreScope/issues/284). State is scattered across 7 localStorage keys, CSS updates bypass the data layer, and there's no single source of truth for the effective configuration.

This spec defines a clean rework based on event-driven state management with a single data flow path. The goal: predictable state, minimal storage footprint, portable config format, and zero ambiguity about which values are active and why.

## Design Decisions

These are agreed and final. Do not reinterpret or deviate.

1. **Three state layers:** server defaults (immutable after fetch), user overrides (delta in localStorage), effective config (computed via merge, never stored directly).
2. **Single data flow:** user action → debounce (~300ms) → write delta to localStorage → read back from localStorage → merge with server defaults → apply CSS variables. No shortcuts, no optimistic CSS updates (see Decision #12 for the one exception).
3. **One localStorage key:** `cs-theme-overrides` — replaces the current 7 scattered keys (`meshcore-user-theme`, `meshcore-timestamp-mode`, `meshcore-timestamp-timezone`, `meshcore-timestamp-format`, `meshcore-timestamp-custom-format`, `meshcore-heatmap-opacity`, `meshcore-live-heatmap-opacity`).
4. **Universal format:** same shape as the server's `ThemeResponse` plus additional keys. Works identically for user export, admin `theme.json`, and user import.
5. **User overrides always win** in merge — `merge(serverDefaults, userOverrides)` = effective config.
6. **Override indicator:** shown in customizer panel ONLY when override value differs from current server default.
7. **No silent pruning:** overrides stay in localStorage until the user explicitly resets them (per-field reset or full reset). The delta may contain values that happen to match current server defaults — that's fine. User intent is preserved; nothing silently disappears.
8. **Per-field reset:** remove a single key from the delta → re-merge → re-apply CSS.
9. **Full reset:** `localStorage.removeItem('cs-theme-overrides')` → re-merge (effective = server defaults) → re-apply CSS.
10. **Export = dump delta object as JSON download. Import = validate shape, write to localStorage, trigger re-merge.**
11. **No CSS magic:** CSS variables ONLY update after the localStorage round-trip completes. No optimistic updates (see Decision #12 for the one exception).
12. **Color picker optimistic CSS exception:** For continuous inputs (color pickers, sliders), CSS is updated optimistically during `input` events for visual responsiveness. The localStorage write only happens on `change` event (mouseup/blur). On `change`, the full pipeline runs: write → read → merge → apply (which will match the optimistic state). If the user refreshes mid-drag before `change` fires, the change is lost — this is acceptable. This is the ONLY exception to the localStorage-first rule.

## Dark/Light Mode

The customizer treats light and dark mode as separate override sections:

- **`theme`** stores light mode color overrides.
- **`themeDark`** stores dark mode color overrides.
- When the user changes a color in the customizer, it writes to whichever section matches their current mode: `theme` if light, `themeDark` if dark.
- The dark/light mode toggle preference (`meshcore-theme` localStorage key) is **separate** from the delta object. It is a view preference, not a customization — it is not stored in `cs-theme-overrides`.
- The customizer UI shows color fields for the currently active mode only. Switching modes re-renders the color fields with values from the matching section.

## Presets

The existing preset themes are preserved and flow through the standard pipeline:

**Available presets:** Default, Ocean, Forest, Sunset, Monochrome.

**How presets work:**
- Clicking a preset writes its values to localStorage via the same pipeline as any other change: preset data → `writeOverrides()` → read back → merge → apply CSS.
- Presets are NOT special — they are pre-built delta objects applied through the standard flow.
- Each preset contains both `theme` (light) and `themeDark` (dark) sections, plus any other overrides the preset defines (e.g., `nodeColors`).
- **"Reset to Default"** = clear all overrides (equivalent to full reset: `localStorage.removeItem('cs-theme-overrides')` → re-merge → apply).

**Preset data format:** Same shape as the delta object. Example:

```json
{
  "theme": {
    "accent": "#0077b6",
    "navBg": "#03045e",
    "background": "#f0f7fa"
  },
  "themeDark": {
    "accent": "#48cae4",
    "navBg": "#03045e",
    "background": "#0a1929"
  }
}
```

Applying a preset **replaces** the entire delta (it's a `writeOverrides(presetData)`, not a merge onto existing overrides). The user can then further customize individual fields on top.

## Data Model

### Delta Object Format

The user override delta is a sparse object — it only contains fields the user has explicitly changed. The shape mirrors the server's `ThemeResponse` (from `/api/config/theme`) plus additional client-only sections:

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
- The `timestamps`, `heatmapOpacity`, and `liveHeatmapOpacity` keys are client-only extensions — not part of the server's `ThemeResponse`, but included in the universal format for portability.

### localStorage Key

**Key:** `cs-theme-overrides`
**Value:** JSON string of the delta object above.
**Absent key** = no overrides = effective config equals server defaults.

### Dark/Light Mode Preference

**Key:** `meshcore-theme`
**Value:** `"dark"` or `"light"` (or absent = follow system preference).
**This key is NOT part of the delta object.** It controls which mode is active, not which colors are used. The delta stores overrides for both modes independently in `theme` and `themeDark`.

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
  ┌──────────────────────────────────────┐
  │ computeEffective(server, userOverrides) │
  └──────────────┬───────────────────────┘
                 │
                 ▼
  ┌──────────────────────────────────────┐
  │ window.SITE_CONFIG = effective       │ ← atomic assignment
  └──────────────┬───────────────────────┘
                 │
                 ▼
  ┌──────────────────────┐
  │ applyCSS(effective)  │ ← sets CSS vars on :root for current mode
  └──────────────────────┘
                 │
                 ▼
  ┌──────────────────────────────┐
  │ dispatch 'theme-changed'     │ ← bare signal, no payload
  └──────────────────────────────┘
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
       ├─► writeOverrides(delta)    ← serialize & write to localStorage
       │       │
       │       ▼
       ├─► readOverrides()          ← read BACK from localStorage (round-trip)
       │       │
       │       ▼
       ├─► computeEffective(server, delta)
       │       │
       │       ▼
       ├─► window.SITE_CONFIG = effective  ← atomic assignment
       │       │
       │       ▼
       └─► applyCSS(effective)      ← CSS vars updated on :root
              │
              ▼
         dispatch 'theme-changed'
```

**Color picker / slider exception:** During continuous `input` events (drag), CSS is updated optimistically (directly setting `--var` on `:root`) without the localStorage round-trip. The full pipeline above only runs on the `change` event (mouseup/blur).

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
       ├─► window.SITE_CONFIG = effective
       └─► applyCSS(effective)
              │
              ▼
         dispatch 'theme-changed'
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
  window.SITE_CONFIG = effective
       │
       ▼
  applyCSS(effective)
       │
       ▼
  dispatch 'theme-changed'
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
  validateShape(parsed)           ← check structure, validate values
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
  computeEffective(server, delta)
       │
       ▼
  window.SITE_CONFIG = effective
       │
       ▼
  applyCSS(effective)
       │
       ▼
  dispatch 'theme-changed'
```

## Function Signatures

### `readOverrides() → object`

Reads `cs-theme-overrides` from localStorage, parses as JSON. Returns empty object `{}` on missing key, parse error, or non-object value. Never throws.

### `writeOverrides(delta: object) → void`

Serializes `delta` to JSON and writes to `cs-theme-overrides` in localStorage. If `delta` is empty (`{}`), removes the key entirely.

**Validation on write:**
- Color values must match: `#hex` (3, 4, 6, or 8 digit), `rgb()`, `rgba()`, `hsl()`, `hsla()`, or CSS named colors. Invalid color values are rejected (not written) with `console.warn`.
- Numeric values (`heatmapOpacity`, `liveHeatmapOpacity`) must be finite numbers in the range 0–1. Invalid values are rejected with `console.warn`.
- Timestamp enum values are validated against known options (`defaultMode`: `'ago'`/`'absolute'`; `timezone`: `'local'`/`'utc'`; `formatPreset`: `'iso'`/`'iso-seconds'`/`'locale'`). Invalid values are rejected with `console.warn`.

**Quota error handling:**
- Wrap `localStorage.setItem` in try/catch.
- On `QuotaExceededError`: show a visible warning to the user ("Storage full — changes may not be saved"), log to console.
- Do NOT silently swallow the error.

### `computeEffective(serverConfig: object, userOverrides: object) → object`

Deep merges `userOverrides` onto `serverConfig`. For each section (e.g., `theme`, `nodeColors`), if `userOverrides` has the section, its keys override the corresponding `serverConfig` keys. Top-level non-object keys (e.g., `heatmapOpacity`) are directly overridden.

Returns a new object — neither input is mutated.

**Merge rules:**
- Object sections: shallow merge per section (`Object.assign({}, server.theme, user.theme)`)
- Array sections (e.g., `home.steps`): full replacement (user array wins entirely, no element-level merge)
- Scalar sections (e.g., `heatmapOpacity`): direct replacement

After computing the effective config, writes it to `window.SITE_CONFIG` atomically (single assignment, not piecemeal mutations).

### `applyCSS(effectiveConfig: object) → void`

Maps effective config values to CSS custom properties on `:root`. Behavior:

1. Reads the current mode (light/dark) from the `meshcore-theme` localStorage key, falling back to system preference (`prefers-color-scheme`).
2. Applies the matching section's values: `theme` for light mode, `themeDark` for dark mode.
3. Also applies mode-independent values: node colors as `--node-{role}`, type colors as `--type-{name}`, font families as `--font-body` and `--font-mono`.
4. Does NOT generate dual CSS rule blocks — only the current mode's values are applied to `:root`.
5. On dark/light mode toggle, `applyCSS` is called again to re-apply the correct section.

Updates the `<style>` element (create if absent, reuse if present). Dispatches a `theme-changed` CustomEvent on `window` after applying.

### `theme-changed` Event

- `theme-changed` is a bare `CustomEvent` with no payload (matches current behavior).
- After each merge cycle, the effective config is written to `window.SITE_CONFIG` atomically (single assignment).
- `window.SITE_CONFIG` is the canonical readable source for effective config throughout the app. All existing listeners that read from `SITE_CONFIG` continue to work without changes.

### `setOverride(section: string, key: string, value: any) → void`

Sets a single override. For nested sections (e.g., `section='theme'`, `key='accent'`), sets `delta[section][key] = value`. For top-level scalars (e.g., `section=null`, `key='heatmapOpacity'`), sets `delta[key] = value`.

Follows the full data flow: read → update → write → read-back → merge → apply CSS → dispatch `theme-changed`. Debounced at ~300ms (the debounce wraps the write-through-to-CSS portion).

### `clearOverride(section: string, key: string) → void`

Removes a single key from the delta. If the section becomes empty after removal, removes the section too. Triggers the full data flow (no debounce — resets should feel instant).

### `migrateOldKeys() → object | null`

One-time migration. Checks for any of the 7 legacy localStorage keys. If found:
1. Reads all legacy values
2. Maps them into the new delta format (see Migration Plan)
3. Writes the merged delta to `cs-theme-overrides`
4. Removes all 7 legacy keys
5. Returns the migrated delta

Returns `null` if no legacy keys found.

### `validateShape(obj: any) → { valid: boolean, errors: string[] }`

Validates that an imported object conforms to the expected shape:
- Must be a plain object
- Top-level keys must be from the known set: `branding`, `theme`, `themeDark`, `nodeColors`, `typeColors`, `home`, `timestamps`, `heatmapOpacity`, `liveHeatmapOpacity`
- Section values must be objects (where expected) or correct scalar types
- Color values are validated: must match `#hex` (3, 4, 6, or 8 digit), `rgb()`, `rgba()`, `hsl()`, `hsla()`, or CSS named colors
- Numeric values (`heatmapOpacity`, `liveHeatmapOpacity`) must be finite numbers in range 0–1
- Timestamp enum values validated against known options

Unknown top-level keys cause a warning but don't fail validation (forward compatibility).

## Migration Plan

On first page load, before the normal init flow:

1. Check if `cs-theme-overrides` already exists → if yes, skip migration.
2. Check if ANY of the 7 legacy keys exist in localStorage.
3. If legacy keys found, build a delta object using the exact mapping below:

### Field-by-Field Migration Mapping

```
meshcore-user-theme (JSON) → parse, map directly:
  .branding  → delta.branding
  .theme     → delta.theme
  .themeDark → delta.themeDark
  .nodeColors → delta.nodeColors
  .typeColors → delta.typeColors
  .home      → delta.home
  (any other keys are dropped)

meshcore-timestamp-mode           → delta.timestamps.defaultMode
meshcore-timestamp-timezone       → delta.timestamps.timezone
meshcore-timestamp-format         → delta.timestamps.formatPreset
meshcore-timestamp-custom-format  → delta.timestamps.customFormat
meshcore-heatmap-opacity          → delta.heatmapOpacity (parseFloat)
meshcore-live-heatmap-opacity     → delta.liveHeatmapOpacity (parseFloat)
```

4. Write the assembled delta to `cs-theme-overrides`.
5. Delete all 7 legacy keys.
6. Continue with normal init.

**Edge cases:**
- If `meshcore-user-theme` contains invalid JSON, skip it (log a warning to console).
- If a legacy value is empty string or null, skip that field.
- Migration runs exactly once — the presence of `cs-theme-overrides` (even as `{}`) prevents re-migration.

## `allowCustomFormat` — User Preferences Trump

The server-side `allowCustomFormat` gate is not enforced client-side. If a user imports a delta with a custom format, it's applied regardless. The server controls what formats are available in the UI (whether the custom format input field is shown), but does not block stored preferences.

## Override Indicator UX

In the customizer panel, each field that has an active override (value differs from server default) shows a visual indicator:

- **Indicator:** A small dot or icon (e.g., `●` or a reset arrow `↺`) adjacent to the field label.
- **Color:** Use the accent color to draw attention without being noisy.
- **Behavior:** Clicking the indicator resets that single field (calls `clearOverride`).
- **Tooltip:** "Reset to server default" or "This value differs from the server default."
- **Absence:** Fields matching the server default show no indicator — clean and minimal.

**Section-level indicator:** If any field in a section (e.g., "Theme Colors") is overridden, the tab/section header shows a count badge (e.g., "Theme Colors (3)").

**"Reset All" button:** Always visible at bottom of panel. Confirms before executing (`localStorage.removeItem` + re-merge).

## UX Requirements

### Browser-Local Banner

The customizer panel must display a persistent, always-visible notice:

> **"These settings are saved in your browser only and don't affect other users."**

This is NOT a tooltip, NOT a dismissible popup — it must be always visible in the panel header or footer area. Users must understand at a glance that their changes are local.

### Auto-Save Indicator

Show a persistent status in the customizer panel footer, Google Docs style — subtle but always present:

- **Default state:** "All changes saved" (muted text)
- **During debounce:** "Saving..." (muted text)
- **On quota error:** "⚠️ Storage full — changes may not be saved" (red text, persistent until resolved)

The indicator reflects the actual state of the localStorage write, not just the UI action.

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
   - Rejects invalid color values with console.warn
   - Rejects out-of-range numeric values with console.warn
   - Rejects invalid timestamp enum values with console.warn
   - Handles QuotaExceededError gracefully (warns user, does not throw)

3. **`computeEffective`**
   - Returns server defaults when overrides is `{}`
   - Overrides a single key in a section
   - Overrides multiple keys across sections
   - Does not mutate either input
   - Handles missing sections in overrides gracefully
   - Array values (e.g., `home.steps`) are fully replaced, not merged
   - Top-level scalars (`heatmapOpacity`) are directly replaced

4. **`setOverride` / `clearOverride`**
   - Setting a value stores it in the delta
   - Clearing a key removes it from delta
   - Clearing the last key in a section removes the section
   - Full data flow executes (CSS vars updated)

5. **`migrateOldKeys`**
   - Migrates all 7 keys correctly using exact field mapping
   - Handles partial migration (only some keys present)
   - Handles invalid JSON in `meshcore-user-theme`
   - Removes all legacy keys after migration
   - Skips migration if `cs-theme-overrides` already exists
   - Returns null when no legacy keys found
   - Drops unknown keys from `meshcore-user-theme`

6. **`validateShape`**
   - Accepts valid delta objects
   - Accepts empty object
   - Rejects non-objects (string, array, null)
   - Warns on unknown top-level keys (doesn't reject)
   - Validates section types (object vs scalar)
   - Rejects invalid color values
   - Rejects out-of-range opacity values
   - Rejects invalid timestamp enum values

### Browser/E2E Tests (Playwright)

1. **Customizer opens and shows current values** — fields reflect effective config.
2. **Changing a color updates CSS variable** — after debounce, `:root` has new value.
3. **Override indicator appears** when value differs from server default.
4. **Per-field reset** removes override, reverts to server default, indicator disappears.
5. **Full reset** clears all overrides, all fields show server defaults.
6. **Export** downloads a JSON file with current delta.
7. **Import** applies overrides from uploaded JSON file.
8. **Migration** — set legacy keys, reload, verify they're migrated and removed.
9. **Preset application** — clicking a preset applies its colors, fields update.
10. **Dark/light mode toggle** — switching mode re-applies correct section's CSS vars.
11. **Browser-local banner** — verify persistent notice is visible in customizer panel.
12. **Auto-save indicator** — verify status text updates during and after changes.

## What's NOT In Scope

- **Undo/redo stack** — could be added as P2. For v1, per-field reset to server default is the only revert mechanism.
- **Cross-tab synchronization** — two tabs editing simultaneously may clobber each other's changes. Acceptable for v1.
- **Server-side timestamp config** (`allowCustomFormat` gate) — remains server-only, not exposed in the customizer delta. The server controls UI availability but does not block stored preferences (see `allowCustomFormat` section above).
- **Admin import endpoint** — no server API for uploading `theme.json` via the UI. Admins edit the file directly. Future work.
- **Map config overrides** (`mapDefaults.center`, `mapDefaults.zoom`) — separate concern, not part of theme. Future work.
- **Geo-filter config** — server-only. Not in scope.
- **Per-page layout preferences** (column widths, sort orders) — separate from theming. Future work.
