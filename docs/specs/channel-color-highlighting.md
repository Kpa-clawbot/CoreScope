# Channel Color Highlighting Spec

**Status:** Proposed  
**Issue:** [#271](https://github.com/Kpa-clawbot/CoreScope/issues/271)  
**Author:** Stinkmeaner (AI)  
**Date:** 2026-04-05

## Problem

When monitoring multiple active hash channels simultaneously on the Live tab, all `GRP_TXT` traffic renders identically — same color, same styling. Users tracking specific channels (e.g. `#wardriving`) cannot visually distinguish their traffic from other channel activity without reading each row's channel field.

## Solution

Allow users to assign custom highlight colors to specific hash channels. Colors propagate across the Live feed, map animations, and timeline. Unassigned channels retain the default `GRP_TXT` styling.

### Data Model

**Storage:** Single `localStorage` key `live-channel-colors`

```json
{
  "#wardriving": "#ef4444",
  "#meshnet": "#3b82f6"
}
```

- Keyed by resolved channel name (e.g. `#wardriving`) or raw hash prefix if unresolved
- Included in customizer theme export/import for portability
- Maximum ~16 assignments (no hard limit, but UI should discourage excess — see Edge Cases)

### Channel Matching

- Match on the packet's `channel` or `group` field
- Handle both resolved channel names and raw hash prefixes
- Only applies to `GRP_TXT` packet types — other types retain their existing `TYPE_COLORS` styling

### Visual Treatment

**Feed rows (primary):**
- 4px colored left border
- Subtle background tint: channel color at 8–10% opacity
- Text color unchanged — contrast must remain accessible

**Map animations:**
- Packet arcs use the assigned channel color instead of default `TYPE_COLORS.GRP_TXT`
- Node markers retain role-based coloring (channel color does NOT override node markers)

**Timeline sparkline:**
- Dots/bars colored per channel assignment
- Unassigned channels use default color

**Auto-legend:**
- Generated from active assignments
- Displayed near the feed header
- Color swatch + channel name, compact horizontal layout

### Configuration UI

**Quick assign (primary workflow):**
- Right-click (long-press on mobile) a channel name in the Live feed
- Color picker popover with ~12 preset swatches + custom hex input
- "Clear" button to remove assignment

**Customizer panel (management):**
- New "Channel Colors" section under existing "Packet Type Colors"
- Lists all assigned channels with color swatches
- Add/edit/remove individual assignments
- "Clear All" button
- Synced with theme export/import

### Priority Rules

| Context | Color source |
|---------|-------------|
| Feed row background/border | Channel color (if assigned), else default |
| Feed row text | Always default (no override) |
| Map packet arcs | Channel color (if assigned), else `TYPE_COLORS.GRP_TXT` |
| Map node markers | Always role color (no override) |
| Timeline dots | Channel color (if assigned), else default |

## Edge Cases

- **10+ colors:** At ~10 simultaneous assignments, colors become hard to distinguish. The UI should show a soft warning ("Many colors assigned — consider clearing unused ones") but not block the user.
- **Color conflicts with role/type colors:** Channel color takes priority for feed row highlighting only. Role colors remain authoritative for node markers.
- **Removal:** Clearing a channel color reverts to default styling immediately — no page refresh needed.
- **Non-GRP_TXT packets:** Channel color never applied. These packets have no channel association.
- **Customizer rework (#288):** If the customizer rework lands first, the Channel Colors section should follow the new single-delta-object pattern (`cs-theme-overrides`). If it hasn't landed, use the standalone `live-channel-colors` key and migrate later.
- **Dark/light mode:** Channel colors are mode-independent (same color in both modes). The 8–10% opacity tint ensures readability in both themes.

## Milestones

### M1: Core model + feed row highlighting
- `localStorage` read/write for `live-channel-colors`
- Feed row rendering: left border + background tint
- Unit tests for storage CRUD and color application logic

### M2: Quick-assign UI
- Right-click / long-press context menu on channel names
- Color picker popover with presets + custom hex
- Clear button
- Playwright E2E test for assign/clear workflow

### M3: Map animation integration
- Packet arc color lookup from channel assignments
- Falls back to `TYPE_COLORS.GRP_TXT` when unassigned
- Visual verification via browser screenshot

### M4: Customizer section + export/import
- "Channel Colors" management panel in customizer
- Include channel colors in theme export JSON
- Import restores channel colors
- Unit tests for export/import round-trip

### M5: Timeline coloring + auto-legend
- Timeline sparkline uses channel colors
- Auto-legend renders near feed header
- Playwright E2E for legend visibility

## Testing

| Level | What | How |
|-------|------|-----|
| Unit | Storage CRUD, color lookup, merge with defaults | `test-frontend-helpers.js` via `vm.createContext` |
| Unit | Export/import round-trip with channel colors | Same |
| E2E | Quick-assign popover, color applied to feed rows | Playwright against localhost |
| E2E | Customizer channel colors section | Playwright |
| E2E | Legend appears when ≥1 channel colored | Playwright |
| Visual | Map arcs colored, dark/light mode readability | Browser screenshot |

## Expert Review Notes

### Tufte (Visualization)
- **Left border + tint is sound.** The 4px border is data-ink (encodes channel identity). The tint at 8–10% opacity provides grouping without overwhelming the data. This is information encoding, not decoration.
- **Risk at scale:** Beyond ~8 colors, perceptual distinguishability drops sharply. The spec correctly warns but doesn't enforce. Consider using a curated palette of maximally-distinct colors (like ColorBrewer qualitative sets) as the preset swatches rather than a free-form picker.
- **Auto-legend is correct:** Direct labeling on every row would be redundant (channel name already in the row). A compact legend near the feed is the right balance — it teaches the encoding once.
- **No chartjunk introduced.** The visual treatment adds information (channel identity) without decorative excess.

### Torvalds (Code Quality)
- **localStorage is fine** for user preferences with <1KB payloads. No need for IndexedDB or server-side storage.
- **5 milestones is appropriate.** Each is independently shippable and testable. No milestone depends on speculation about future milestones.
- **Watch the customizer coupling.** If #288 lands, the `live-channel-colors` key should merge into `cs-theme-overrides`. Design the read/write functions to abstract the storage key so migration is a one-line change, not a rewrite.
- **Keep the color picker simple.** Don't build a custom color picker — use `<input type="color">` with preset swatch buttons. The browser's native picker is fine.

### Doshi (Product Strategy)
- **This is N (Neutral).** It's a genuine usability improvement for multi-channel monitoring, but it doesn't change CoreScope's trajectory. It won't attract new users or unlock new use cases — it makes existing power users slightly more efficient.
- **Opportunity cost is low.** Each milestone is small (~1-2 hours of work). The total investment is modest.
- **5 milestones is fine** given each is small. Shipping M1+M2 alone delivers 80% of the value. M3–M5 are polish. Consider M1+M2 as the MVP gate — if nobody uses channel colors after M2, stop there.
- **Pre-mortem:** This fails if users rarely monitor 2+ channels simultaneously, making the problem theoretical. Validate that multi-channel monitoring is a real workflow before M3.
