/* Issue #1705 — WCAG AA contrast regression for `.subpath-selected .hop-prefix`.
 *
 * The class historically used `color: rgba(255,255,255,0.6)` over a
 * `.subpath-selected { background: var(--accent) }` surface. In dark theme
 * the composited pixel was ~1.87:1 — well under WCAG AA 4.5:1.
 *
 * This test:
 *  1. Parses public/style.css and extracts `--accent`, `--accent-strong`,
 *     `--text-on-accent` per theme (with the same one-level var() resolver
 *     as test-issue-1668-m2-contrast.js).
 *  2. Reads the declared `background` for `.subpath-selected` and the
 *     declared `color` for `.subpath-selected .hop-prefix`.
 *  3. Properly composites rgba() foregrounds against the resolved
 *     background pixel before computing luminance (the historical
 *     a11y-probe miss called out in the #1705 issue body).
 *  4. Asserts the composited fg-vs-bg contrast ratio is ≥4.5:1 in both
 *     `:root` (light) and `[data-theme="dark"]`.
 *
 * Red posture: with the legacy rule
 *     .subpath-selected .hop-prefix { color: rgba(255,255,255,0.6); }
 * over `--accent` (#4a9eff in either theme), this test fails on the
 * composited contrast assertion (≈1.87:1).
 *
 * Green posture: parent uses `--accent-strong` + `--text-on-accent`, and
 * the child inherits, giving ≥4.5:1 on the composited pixel in both themes.
 */
'use strict';
const fs = require('fs');
const assert = require('assert');

const CSS_PATH = 'public/style.css';
const css = fs.readFileSync(CSS_PATH, 'utf8');

// ── Token extraction (mirrors test-issue-1668-m2-contrast.js) ─────────────
// MF5 (review r1): throw loudly when the block regex does not match the
// scoped `selector { ... \n}` shape — silent {} previously let the test
// pass falsely if `style.css` were minified or its closing brace style
// changed. Callers must pass a human-readable `selectorLabel` for the
// error message.
function extractBlockTokens(blockRegex, selectorLabel) {
  const tokens = {};
  const flagged = new RegExp(blockRegex.source, blockRegex.flags.includes('g') ? blockRegex.flags : blockRegex.flags + 'g');
  let m;
  let matched = false;
  while ((m = flagged.exec(css)) !== null) {
    matched = true;
    const body = m[1];
    const re = /^\s*(--[a-z0-9-]+)\s*:\s*([^;]+);/gim;
    let mm;
    while ((mm = re.exec(body)) !== null) {
      tokens[mm[1]] = mm[2].trim();
    }
  }
  if (!matched) {
    throw new Error(
      `extractBlockTokens: no closing brace found for selector ${selectorLabel || blockRegex.source} ` +
      `(parser expects \`<selector> { ... \\n}\` shape; if style.css was minified or restructured, fix the parser).`
    );
  }
  return tokens;
}

const lightTokens = extractBlockTokens(/:root\s*\{([\s\S]*?)\n\}/, ':root');
const darkTokens  = extractBlockTokens(/\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/, '[data-theme="dark"]');

// MF5 defensive assertion — if the CSS structure regresses to a shape
// the parser can't read, fail at the top rather than silently emitting
// "all colors look fine" because every token resolved to undefined.
for (const tok of ['--accent', '--accent-strong', '--text-on-accent']) {
  assert.ok(
    lightTokens[tok],
    `extractBlockTokens regression: expected :root to declare ${tok} but parser found none. ` +
    `Either the CSS structure changed or extractBlockTokens needs updating.`
  );
}

function resolveToken(name, theme) {
  const map = theme === 'dark' ? { ...lightTokens, ...darkTokens } : lightTokens;
  let val = map[name];
  if (!val) return null;
  for (let i = 0; i < 5; i++) {
    const m = val.match(/^var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([^)]+))?\)\s*$/);
    if (!m) break;
    const next = map[m[1]];
    val = next || (m[2] ? m[2].trim() : null);
    if (!val) return null;
  }
  return val;
}

// ── Color parsing (returns [r,g,b,a] with a in [0,1]) ─────────────────────
function parseColor(s) {
  if (!s) return null;
  s = s.trim();
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) {
    return [
      parseInt(m[1][0] + m[1][0], 16),
      parseInt(m[1][1] + m[1][1], 16),
      parseInt(m[1][2] + m[1][2], 16),
      1,
    ];
  }
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) {
    return [parseInt(m[1].slice(0,2),16), parseInt(m[1].slice(2,4),16), parseInt(m[1].slice(4,6),16), 1];
  }
  m = s.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([0-9.]+))?\s*\)$/i);
  if (m) return [+m[1], +m[2], +m[3], m[4] != null ? parseFloat(m[4]) : 1];
  return null;
}

// ── Alpha compositing (sRGB straight-alpha over an opaque background) ─────
// The historical audit probe miss: rgba(255,255,255,0.6) over #4a9eff is
// NOT "white vs #4a9eff" — it's the COMPOSITED pixel vs #4a9eff for the
// purposes of WCAG. We compose then compare composed-fg to bg.
function composite(fg, bg) {
  const a = fg[3];
  return [
    Math.round(fg[0] * a + bg[0] * (1 - a)),
    Math.round(fg[1] * a + bg[1] * (1 - a)),
    Math.round(fg[2] * a + bg[2] * (1 - a)),
    1,
  ];
}

function relLum([r,g,b]) {
  const f = (c) => {
    c /= 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126*f(r) + 0.7152*f(g) + 0.0722*f(b);
}

function contrast(fg, bg) {
  const L1 = relLum(fg), L2 = relLum(bg);
  const [hi, lo] = L1 >= L2 ? [L1, L2] : [L2, L1];
  return (hi + 0.05) / (lo + 0.05);
}

// `.subpath-selected { background: <X>; color: <Y>; ... }`
// `.subpath-selected .hop-prefix { color: <Z>; ... }`
// MF7 (review r1): collect ALL matches for the property across every rule
// whose selector matches `selectorRegex` and fail loudly if more than one
// declaration is found, so a higher-specificity / later-in-cascade
// override doesn't silently win over (or under) the one we're auditing.
// The simplified parser here cannot model real CSS specificity; throwing
// is the correct discipline — a human resolves which one is the
// intended ground truth.
function extractDecl(selectorRegex, prop, selectorLabel) {
  const re = new RegExp(selectorRegex.source + '\\s*\\{([^}]*)\\}', 'g');
  const matches = [];
  let m;
  while ((m = re.exec(css)) !== null) {
    const body = m[1];
    const propRe = new RegExp('(?:^|[;\\s])' + prop + '\\s*:\\s*([^;]+?)\\s*(?:!important)?\\s*;', 'i');
    const mm = body.match(propRe);
    if (mm) matches.push({ rule: m[0].slice(0, 80).replace(/\s+/g, ' '), value: mm[1].trim() });
  }
  if (matches.length === 0) return null;
  if (matches.length > 1) {
    // Different values across rules → ambiguous cascade. Identical values
    // are still suspicious but not load-bearing for this audit.
    const distinct = new Set(matches.map(x => x.value));
    if (distinct.size > 1) {
      throw new Error(
        `extractDecl: multiple distinct \`${prop}\` declarations matched selector ${selectorLabel || selectorRegex.source} — ` +
        `parser cannot resolve CSS specificity. Matches:\n` +
        matches.map(x => `    ${x.value}  (in: ${x.rule}...)`).join('\n') +
        `\nResolve by collapsing rules in CSS or by adding a Playwright getComputedStyle assertion.`
      );
    }
    // Same value across N rules — warn but proceed.
    console.warn(`  ⚠ extractDecl: ${matches.length} rules declare \`${prop}\` = ${matches[0].value} for ${selectorLabel || selectorRegex.source} (identical, OK).`);
  }
  return matches[matches.length - 1].value;
}

// The parent selector — `\\.subpath-selected` matches both
// `.subpath-selected { ... }` and the qualified child rule, so anchor by
// boundary on the closing of the class token.
const parentBg     = extractDecl(/(?:^|[\s>+~,])\.subpath-selected(?=\s*\{)/, 'background', '.subpath-selected');
const parentColor  = extractDecl(/(?:^|[\s>+~,])\.subpath-selected(?=\s*\{)/, 'color', '.subpath-selected');
const childColor   = extractDecl(/\.subpath-selected\s+\.hop-prefix(?=\s*\{)/, 'color', '.subpath-selected .hop-prefix');

assert.ok(parentBg, '`.subpath-selected { background: ... }` not found in CSS');
assert.ok(childColor, '`.subpath-selected .hop-prefix { color: ... }` not found in CSS');

console.log(`\n#1705 .subpath-selected hop-prefix contrast audit\n${'─'.repeat(60)}`);
console.log(`parent background : ${parentBg}`);
console.log(`parent color      : ${parentColor || '(unset)'}`);
console.log(`child color       : ${childColor}`);

// `var(--token, fallback)` — resolve via theme map; if missing, use fallback.
function resolveCssValue(raw, theme) {
  raw = raw.trim();
  const m = raw.match(/^var\(\s*(--[a-z0-9-]+)\s*(?:,\s*(.+))?\)$/i);
  if (m) {
    const tokVal = resolveToken(m[1], theme);
    if (tokVal) return tokVal;
    return m[2] ? resolveCssValue(m[2], theme) : null;
  }
  // strip trailing `!important` if any
  return raw.replace(/\s*!important\s*$/i, '');
}

const THEMES = ['light', 'dark'];
let failures = 0;
for (const theme of THEMES) {
  const bgRaw   = resolveCssValue(parentBg, theme);
  const bg      = parseColor(bgRaw);
  assert.ok(bg, `[${theme}] could not parse parent background "${bgRaw}"`);

  // Child color may be `inherit`, in which case fall back to the parent's
  // declared color. MF6 (review r1): do NOT default to white — that
  // historically hid a regression where the parent's color drifted from
  // #fff to something else and the test still saw white-on-blue and
  // passed. Walk the declared cascade: child → parent → throw if neither
  // declares a color. If the user wants browser-cascade semantics,
  // they can use the Playwright variant (test-issue-1705-subpath-contrast-e2e.js).
  let childRaw = resolveCssValue(childColor, theme);
  if (/^inherit$/i.test(childRaw)) {
    if (!parentColor) {
      throw new Error(
        `[${theme}] child .hop-prefix color is \`inherit\` but no parent .subpath-selected color is declared in CSS. ` +
        `Parser cannot walk past one level — either declare a color on .subpath-selected, ` +
        `or replace this parser test with the Playwright variant which uses real getComputedStyle.`
      );
    }
    childRaw = resolveCssValue(parentColor, theme);
    if (!childRaw) {
      throw new Error(
        `[${theme}] could not resolve parent color "${parentColor}" while expanding child \`inherit\` in ${theme} theme.`
      );
    }
  }
  const fgDeclared = parseColor(childRaw);
  assert.ok(fgDeclared, `[${theme}] could not parse child color "${childRaw}"`);

  // Alpha-composite the declared foreground over the resolved background
  // pixel before computing contrast. This is the correctness fix the
  // probe missed.
  const fgComposed = fgDeclared[3] < 1 ? composite(fgDeclared, bg) : fgDeclared;
  const ratio      = contrast(fgComposed, bg);

  const hex = (c) => `#${c.slice(0,3).map(v=>v.toString(16).padStart(2,'0')).join('')}`;
  const ok = ratio >= 4.5;
  console.log(
    `${ok ? '✓' : '✗'} ${theme.padEnd(5)} hop-prefix composed=${hex(fgComposed)} on bg=${hex(bg)}  ratio=${ratio.toFixed(2)}:1  (need ≥4.5)`
  );
  if (!ok) failures++;
  assert.ok(
    ok,
    `#1705 regression: .subpath-selected .hop-prefix contrast in ${theme} theme is ${ratio.toFixed(2)}:1, below WCAG AA 4.5:1 (composed ${hex(fgComposed)} on ${hex(bg)})`
  );
}

if (failures === 0) {
  console.log(`\n✅ PASS — .subpath-selected .hop-prefix ≥4.5:1 in both themes`);
}
