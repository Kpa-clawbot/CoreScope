/**
 * Issue #1644 — Behavioral regression tests for the observer-comparison
 * redesign. Pure-Node, no Playwright; runs in <1s.
 *
 * Three behavioral guarantees:
 *  1. `.btn-secondary` exists as a TOP-LEVEL themed rule in style.css
 *     (not just scoped inside the channel modal). It uses theme tokens
 *     for background, border and color — never browser defaults (white/
 *     #ccc), never invented top-level color literals.
 *  2. `.btn-secondary[disabled]`/`:disabled` is visually distinct
 *     (opacity rule present).
 *  3. observers.js snapshots+restores the compare-selection checkbox
 *     state across renders via a documented Set-based helper.
 *     A pure helper `window.preserveCompareSelection(prevSet, tbody)`
 *     re-checks any rows whose id appears in prevSet, and the renderer
 *     calls it post-`innerHTML=` rewrite.
 *
 * These are the assertions the redesign must keep green. Aesthetic
 * verification (looks Tufte-grade) is screenshot-based, not asserted here.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const CSS = fs.readFileSync(path.join(__dirname, 'public/style.css'), 'utf8');
const OBS_JS = fs.readFileSync(path.join(__dirname, 'public/observers.js'), 'utf8');
const COMPARE_JS = fs.readFileSync(path.join(__dirname, 'public/compare.js'), 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  \u2705 ' + name); }
  catch (e) { failed++; console.error('  \u274c ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

console.log('\n#1644 redesign — behavioral assertions\n');

// ── 1) Themed .btn-secondary at top level ────────────────────────────
test('.btn-secondary defined as top-level CSS rule (not only .ch-modal-btn-secondary)', () => {
  // Match a top-level `.btn-secondary` or `.btn-secondary,` selector — NOT
  // `.ch-modal-btn-secondary` which is an entirely different prefix.
  const re = /(^|[\s,{}])\.btn-secondary(\s*[,{:.\s])/m;
  assert(re.test(CSS), '.btn-secondary rule missing from public/style.css');
});

test('.btn-secondary uses theme tokens (background var(--*) and color var(--*))', () => {
  // Find the rule block that declares .btn-secondary at top level and
  // verify it composes var(--…) tokens rather than hex/named browser
  // defaults. We extract any block whose selector list contains
  // `.btn-secondary` (with no preceding alphanum so we don't pick up
  // `.ch-modal-btn-secondary`).
  const blocks = CSS.match(/(?:^|\n)([^{}\n]*\.btn-secondary[^{}\n]*)\{([^}]*)\}/g) || [];
  // Filter out the .ch-modal-btn-secondary mention (different rule)
  const own = blocks.filter(b => /(^|[\s,])\.btn-secondary(\s*[,{:.\s])/.test(b));
  assert(own.length > 0, 'no own-rule block found for .btn-secondary');
  const joined = own.join('\n');
  assert(/background\s*:\s*[^;]*var\(--/.test(joined),
    '.btn-secondary background must reference a CSS variable');
  assert(/color\s*:\s*[^;]*var\(--/.test(joined),
    '.btn-secondary color must reference a CSS variable');
  // Tufte: no decorative gradients/shadows on a secondary button
  assert(!/linear-gradient|box-shadow:\s*[^n]/.test(joined.replace(/box-shadow:\s*none/g, '')),
    '.btn-secondary must not introduce chartjunk (gradient/shadow)');
});

test('.btn-secondary disabled state is visually distinct (opacity rule)', () => {
  assert(/\.btn-secondary[^{]*(?:\[disabled\]|:disabled)[^{]*\{[^}]*opacity\s*:/.test(CSS),
    '.btn-secondary[disabled] / :disabled needs an opacity declaration');
});

// ── 2) Compare card surfaces de-junked ───────────────────────────────
// The redesign replaces the three card-boxes with a single proportional
// strip + diff bar (Tufte: shared-axis small-multiples). If any
// compare-card variants survive, they must use theme tokens, not raw
// rgba() literals. The historic rule with `rgba(34, 197, 94, 0.1)` etc.
// is the regression we're guarding against.
test('compare-card surfaces (if present) use theme tokens — no raw rgba literals', () => {
  const cardRules = CSS.match(/\.compare-card-(?:a|b|both)[^{]*\{[^}]*\}/g) || [];
  cardRules.forEach(rule => {
    assert(!/rgba\(\s*\d+\s*,\s*\d+\s*,\s*\d+/.test(rule),
      'compare-card rule has raw rgba() literal — must use theme tokens: ' + rule.slice(0, 100));
  });
});

test('compare-strip exists in CSS as the headline data-display element', () => {
  assert(/\.compare-strip\b/.test(CSS),
    'expected .compare-strip rule (the small-multiples redesign of the comparison summary)');
});

// ── 3) Checkbox-state preservation helper ────────────────────────────
test('observers.js exposes window.preserveCompareSelection helper', () => {
  assert(/window\.preserveCompareSelection\s*=/.test(OBS_JS),
    'expected window.preserveCompareSelection helper to be defined');
});

test('preserveCompareSelection re-checks rows whose id was previously selected', () => {
  // Minimal DOM shim: tbody.querySelectorAll('input[data-compare-select]').
  // Each checkbox has .value and .checked (mutable).
  function mkBox(id) {
    return {
      value: id,
      checked: false,
      _attrs: { 'data-compare-select': '' },
      hasAttribute(k) { return k in this._attrs; },
    };
  }
  const boxes = [mkBox('a'), mkBox('b'), mkBox('c')];
  const tbody = {
    querySelectorAll(sel) {
      assert(/data-compare-select/.test(sel), 'expected data-compare-select selector');
      return boxes;
    },
  };
  // Load observers.js in a vm with stub globals
  const sandbox = {
    window: {},
    document: { addEventListener() {}, querySelector() { return null; } },
    registerPage() {},
    debouncedOnWS() {},
    offWS() {},
    api() { return Promise.resolve({ observers: [] }); },
    CLIENT_TTL: {},
    setInterval() {}, clearInterval() {},
    RegionFilter: { init() {}, onChange() {}, offChange() {}, getSelected() { return null; } },
    SlideOver: null,
    location: { hash: '' },
    Date: Date, Math: Math, Number: Number, Set: Set, Map: Map, Array: Array, Object: Object,
    encodeURIComponent, console,
  };
  vm.createContext(sandbox);
  vm.runInContext(OBS_JS, sandbox);
  const fn = sandbox.window.preserveCompareSelection;
  assert(typeof fn === 'function', 'preserveCompareSelection missing');

  const prev = new Set(['a', 'c']);
  fn(prev, tbody);
  assert(boxes[0].checked === true, 'a should be re-checked');
  assert(boxes[1].checked === false, 'b should NOT be checked');
  assert(boxes[2].checked === true, 'c should be re-checked');
});

test('observers render() calls preserveCompareSelection after innerHTML rewrite', () => {
  // Strip block + line comments first so we're not satisfied by the
  // JSDoc/`// See window.preserveCompareSelection above.` references —
  // we want a REAL invocation, in the code path.
  const code = OBS_JS
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1');
  assert(/preserveCompareSelection\s*\(/.test(code),
    'observers.js must INVOKE preserveCompareSelection in code (not just mention in a comment)');
  // And there must be a snapshot Set of previously-selected ids built
  // BEFORE the tbody is rewritten.
  assert(/:checked/.test(code) && /input\[data-compare-select\]/.test(code),
    'observers.js render must snapshot existing :checked compare-select boxes before innerHTML rewrite');
});

// ── 4) a11y — tablist + clickable strip semantics (#1644 round-1) ────
test('compare tab buttons declare aria-selected (synced with .active)', () => {
  // Tab markup is built as a string in compare.js. Each `<button class="tab-btn"
  // ... role="tab">` MUST also emit aria-selected so screen readers know which
  // tab is current. We grep for `aria-selected=` co-located with `role="tab"`.
  // A passing implementation will emit something like:
  //   aria-selected="' + (currentView === 'both' ? 'true' : 'false') + '"
  const tabButtonBlocks = COMPARE_JS.match(/'<button class="tab-btn[^']*'[\s\S]{0,400}?role="tab"[^']*'/g) || [];
  assert(tabButtonBlocks.length >= 3, 'expected >=3 tab-btn strings with role="tab"');
  tabButtonBlocks.forEach((blk, i) => {
    assert(/aria-selected\s*=/.test(blk),
      'tab-btn block #' + i + ' missing aria-selected: ' + blk.slice(0, 120));
  });
});

test('compare tab buttons declare aria-controls="compareDetail"', () => {
  const tabButtonBlocks = COMPARE_JS.match(/'<button class="tab-btn[^']*'[\s\S]{0,400}?role="tab"[^']*'/g) || [];
  tabButtonBlocks.forEach((blk, i) => {
    assert(/aria-controls=\\?"compareDetail\\?"/.test(blk),
      'tab-btn block #' + i + ' missing aria-controls="compareDetail"');
  });
});

test('clickable compare-strip segments expose role="button" + tabindex', () => {
  // The strip rows carry data-view and are clicked via closest('[data-view]').
  // Without role=button and tabindex, keyboard/AT users can't activate them.
  // Find the three strip block strings (compare-strip-side, -mid, -side-b).
  const stripBlocks = COMPARE_JS.match(/'<div class="compare-strip-(?:side|mid)[^']*data-view[^']*'/g) || [];
  assert(stripBlocks.length >= 3, 'expected >=3 data-view strip blocks, got ' + stripBlocks.length);
  stripBlocks.forEach((blk, i) => {
    assert(/role=\\?"button\\?"/.test(blk),
      'strip block #' + i + ' must have role="button": ' + blk.slice(0, 120));
    assert(/tabindex=\\?"0\\?"/.test(blk),
      'strip block #' + i + ' must have tabindex="0": ' + blk.slice(0, 120));
  });
});

test('compare.js binds keydown for Enter/Space activation of strip segments', () => {
  // We need a keydown handler that activates a [data-view] segment.
  assert(/addEventListener\(\s*['"]keydown['"]/.test(COMPARE_JS),
    'compare.js must bind a keydown handler for keyboard activation of strip segments');
});

test('.compare-strip-side and .compare-strip-mid declare cursor:pointer in CSS', () => {
  // Tufte review noted only .compare-card had cursor:pointer; the strip
  // segments are now the clickable surface and need the same affordance.
  const sideRules = CSS.match(/\.compare-strip-side\b[^{]*\{[^}]*\}/g) || [];
  const midRules  = CSS.match(/\.compare-strip-mid\b[^{]*\{[^}]*\}/g) || [];
  const all = sideRules.concat(midRules).join('\n');
  assert(/cursor\s*:\s*pointer/.test(all),
    '.compare-strip-side / .compare-strip-mid need cursor:pointer to signal clickability');
});

console.log('\n' + passed + ' passed, ' + failed + ' failed\n');
process.exit(failed === 0 ? 0 : 1);
