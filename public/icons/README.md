# Phosphor Icon Sprite

This directory holds the vendored Phosphor Icons sprite used by the CoreScope
frontend. We do **not** ship the Phosphor webfont (~150 KB) or fetch icons from
a CDN at runtime — every icon used by the UI is bundled here.

## File layout

- `phosphor-sprite.svg` — single SVG sprite, one `<symbol id="ph-NAME">` per icon
  (regular weight, `viewBox="0 0 256 256"` to match Phosphor's native grid).

## Markup pattern

```html
<svg class="ph-icon" aria-hidden="true" focusable="false">
  <use href="/icons/phosphor-sprite.svg#ph-magnifying-glass"></use>
</svg>
```

CSS helper (defined in `public/style.css`):

```css
.ph-icon { width: 1em; height: 1em; vertical-align: -0.125em; fill: currentColor; }
```

Icons inherit color via `currentColor` and size via the surrounding font-size,
so they re-theme automatically with light/dark mode and CSS variables.

## Adding a new icon

1. Pull the regular-weight SVG from
   <https://cdn.jsdelivr.net/npm/@phosphor-icons/core@2.1.1/assets/regular/NAME.svg>
   (or `assets/fill/NAME.svg` for the rare filled-circle / star-fill cases).
2. Append a `<symbol id="ph-NAME" viewBox="0 0 256 256">…</symbol>` to
   `phosphor-sprite.svg`. Strip the outer `<svg>` wrapper and any `fill=` attrs
   on the inner `<path>` (we want `currentColor` from the parent).
3. Reference it with `<use href="/icons/phosphor-sprite.svg#ph-NAME"></use>`.

## Weight policy

**Regular weight only**, with two filled exceptions allowed for status dots and
star-favorite (`circle-fill`, `star-fill`, `square-fill`). Bold/duotone are
reserved for a future design pass — do not introduce them ad hoc.

## Lint plan (M6)

A `make lint-no-emoji` target will grep `public/**` for codepoints in
`U+1F300–U+1FAFF`, `U+2600–U+27BF`, `U+2700–U+27BF` and the misc-symbols set
(`◆●■▲★☆○✓✗⚠✉`) outside an allowlist (channel-name strings, log/error text,
test fixtures). Until that lands, run the audit script in
`scripts/audit-emoji.py` (added in #1648).
