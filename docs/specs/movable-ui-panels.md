# Movable UI Panels — Draggable Panel Positioning

**Status:** Proposed  
**Related:** #279 (original request), PR #606 (collapsible panels — immediate fix)  
**Date:** 2026-04-05

---

## Problem

The live map page overlays several UI panels on the map viewport: legend, live feed, node detail, and filters. On smaller screens or dense deployments, these panels obscure map content. Users have no control over where panels sit — they're CSS-fixed in corners, and when they collide with each other or with map data, the only option is to close them entirely. Closing a panel means losing access to the data it shows.

PR #606 addresses the immediate pain with collapsible panels and responsive breakpoints. This spec covers the next step: letting users reposition panels to wherever serves their workflow best.

## Solution

Panels become draggable within the map viewport. Users grab a handle, drag to a new position, release. Position persists in `localStorage` per panel ID. That's it.

### What each panel gets

| Affordance | Behavior |
|---|---|
| **Drag handle** | A subtle grip indicator (6-dot grid or `⋮⋮`) in the panel header. Cursor changes to `grab`/`grabbing`. The handle is the ONLY drag target — the panel body remains interactive (scrollable, clickable). |
| **Snap-to-edge** | When released within 20px of a viewport edge, the panel snaps flush to that edge. Prevents panels floating 3px from the side looking broken. |
| **Position persistence** | `localStorage` key per panel: `panel-pos-{id}` → `{ x, y }` as viewport percentages (not pixels — survives resize). |
| **Z-index on focus** | Clicking or dragging a panel brings it to front. Simple incrementing counter, reset on page load. |
| **Reset button** | Single button (in settings or as a map control) resets ALL panels to default positions. Clears all `panel-pos-*` keys. |

### What we do NOT build

- **Resizable panels.** Drag-to-resize adds complexity for marginal benefit. Panels have natural content-driven sizes.
- **Docking/tiling/splitting.** This is not a window manager. No snap-to-other-panel, no split view, no tiling grid.
- **Panel minimization to a taskbar.** Collapsible (PR #606) is sufficient.
- **Drag on mobile.** Touch-drag conflicts with map pan. Mobile keeps collapsible behavior from PR #606. Draggable is desktop-only (`pointer: fine` media query).

## Design Considerations

### Drag handle affordance

The handle must be visible enough that users discover it, but not so prominent that it becomes visual noise. A 6-dot grip icon (`⋮⋮`) in the panel title bar, styled at 60% opacity, rising to 100% on hover. The cursor change (`grab` → `grabbing`) provides the primary affordance.

### Snap-to-edge

Panels snap to the nearest edge when released within a 20px threshold. Snap positions: top-left, top-right, bottom-left, bottom-right, or any edge midpoint. This prevents the "floating at 47px from the left" awkwardness without constraining users to a rigid grid.

### Position persistence

Positions stored as viewport percentages: `{ xPct: 0.02, yPct: 0.15 }`. On window resize, panels stay proportionally positioned. If a resize would push a panel off-screen, clamp it to the nearest visible edge.

### Responsive breakpoints

Below the medium breakpoint (defined in PR #606), panels revert to their fixed/collapsible positions. The draggable behavior is a progressive enhancement for viewports wide enough to have meaningful repositioning space. Persisted positions are preserved in `localStorage` but not applied until the viewport is wide enough again.

### Z-index management

A module-level counter starting at 1000. Each panel interaction (click, drag start) sets that panel's z-index to `++counter`. On page load, counter resets to 1000. No panel can exceed z-index 9999 (modal/overlay territory) — if counter approaches that, compact all panel z-indices down.

### Accessibility

- Panels are focusable (`tabindex="0"` on the drag handle).
- Arrow keys reposition the focused panel by 10px per press (Shift+Arrow = 50px).
- `Escape` while dragging cancels and returns to the previous position.
- `Home` key resets the focused panel to its default position.
- Screen readers: `aria-label="Drag handle for {panel name}. Use arrow keys to reposition."` and `role="slider"` with `aria-valuenow` reflecting position.

## Implementation

### Milestones

**M1: Core drag mechanics** (~2 days)
- `DragManager` class: registers panels, handles pointer events, updates positions
- Snap-to-edge logic
- Z-index management
- No persistence yet — positions reset on reload

**M2: Persistence + reset** (~1 day)
- `localStorage` read/write for panel positions
- Reset-to-defaults button
- Viewport-percentage storage with resize clamping

**M3: Responsive + accessibility** (~1 day)
- Disable drag below medium breakpoint
- Keyboard repositioning (arrow keys)
- ARIA attributes
- Screen reader announcements on position change

**M4: Polish + testing** (~1 day)
- Playwright E2E tests: drag, snap, persist, reset, keyboard
- Performance validation: drag must not trigger layout thrash (use `transform: translate()`, not `top/left`)
- Edge case handling (see below)

### Technical approach

- **No library.** Pointer events (`pointerdown`, `pointermove`, `pointerup`) with `setPointerCapture`. ~150 lines of vanilla JS.
- **CSS transforms for positioning.** `transform: translate(Xpx, Ypx)` avoids layout reflow during drag. Only write to `style.transform`, never `top`/`left`.
- **Debounce persistence.** Write to `localStorage` on `pointerup`, not during drag.
- **Single file:** `public/drag-manager.js` — imported by `live.js`, no other dependencies.

## Edge Cases

| Case | Handling |
|---|---|
| Panel dragged partially off-screen | Clamp to viewport bounds on `pointerup` |
| Window resized while panel is near edge | Re-clamp on `resize` (debounced 200ms) |
| Two panels overlap after drag | Allowed — z-index determines which is on top. Users can move them. |
| `localStorage` full or unavailable | Graceful fallback to default positions. No error shown. |
| Panel content changes size after drag | Panel stays at dragged position; content reflows within. If panel grows past viewport edge, clamp. |
| User has old `localStorage` keys from a removed panel | Ignore unknown keys on load. Clean up stale keys on reset. |
| RTL layouts | Snap logic uses physical viewport edges, not logical start/end. Drag is inherently physical. |

## Expert Reviews

### Tufte (Information Design)

- **Draggability is justified** only if it serves data access — and here it does. Panels obscuring map data is a data-visibility problem, not a UI-decoration problem. Letting users clear their sightlines to the data is correct.
- **The drag handle must be minimal.** Six dots at 60% opacity is acceptable. Anything more prominent (colored bars, icons, labels) becomes chartjunk — UI chrome competing with data for attention.
- **Resist feature creep.** Resizable panels, docking zones, panel-to-panel snapping — all increase interface complexity without increasing data throughput. The spec correctly excludes these.
- **Snap-to-edge is good.** It prevents the visual noise of arbitrarily placed rectangles. Panels aligned to edges create clean negative space for the map data.

### Torvalds (Engineering Pragmatism)

- **This is borderline over-engineering.** The real question: do users actually need free-form drag, or would a simpler "pick a corner" toggle (TL/TR/BL/BR) cover 95% of use cases with 20% of the code?
- **The 4-corner toggle would be ~40 lines.** The full drag system is ~150+ lines plus persistence, snap logic, accessibility, resize handling, z-index management, and edge cases. That's a lot of surface area for "I want the legend on the right instead of the left."
- **Recommendation:** Ship the 4-corner toggle first (M0). If users actually request free-form drag after that, build it. YAGNI applies here.
- **If you do build drag:** the spec is sound. Pointer events + transforms + localStorage is the right stack. No library is correct. But test it on Firefox — pointer capture has quirks.

### Doshi (Product/Business)

- **This is an N (Nice-to-have), not an L (Leverage).** It improves UX for power users who spend hours on the live map, but it doesn't unlock new capabilities or new users.
- **Opportunity cost:** 5 developer-days on draggable panels is 5 days not spent on features that expand what CoreScope can do (new analytics, alerting, multi-site support).
- **The collapsible panels (PR #606) likely resolve the P1 pain.** Track whether users still complain about panel placement after #606 ships. If complaints drop to zero, this spec can stay on the shelf.
- **If built:** ship M1+M2 only (3 days). M3 accessibility can come later if adoption warrants it. M4 testing is non-negotiable.

### Feedback incorporated

Based on the reviews, the spec adds a **Milestone 0** recommendation:

**M0: Corner-position toggle** (~0.5 days)  
Before building full drag, ship a simpler panel-position toggle: each panel's header gets a small button that cycles through TL → TR → BR → BL placement. Positions persist in `localStorage`. If this satisfies user needs, M1–M4 become unnecessary.

**Decision gate:** Ship M0 with PR #606 or shortly after. Monitor feedback for 2 weeks. If users request free-form repositioning, proceed to M1. If corner toggle is sufficient, close this spec as "resolved by M0."
