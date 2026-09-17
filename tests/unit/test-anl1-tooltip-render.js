/**
 * test-anl1-tooltip-render.js
 *
 * Behavioral regression test for MAJOR ANL-1 from PR #1539 polish round 1.
 *
 * Background: PR #1539 swapped `tip.innerHTML = td.dataset.tip` for
 * `tip.textContent = td.dataset.tip` to neutralize mutation-XSS. That fixes
 * the XSS, but the `data-tip` payload was structured HTML emitted by
 * `hashTooltipHtml()` — so users now see literal `<div class="...">…</div>`
 * strings inside the tooltip and the `.hash-matrix-tooltip-hex/-status/-nodes`
 * CSS rules no longer fire.
 *
 * The correct fix (option (c) from review): stop carrying HTML through the
 * dataset round-trip. Carry the *fields* via separate `data-tip-*` attrs,
 * then on mouseover rebuild the tooltip body with `createElement` +
 * `textContent` per child. Preserves styling, never touches `innerHTML` on
 * node-controlled fields, defeats the mutation-XSS by construction.
 *
 * This file drives that contract:
 *   (1) `hashCellTd(...)` emits dataset entries for hex / status / each row,
 *       NOT a `data-tip` HTML string.
 *   (2) `buildMatrixTipChildren(tip, td)` exists, reads those dataset
 *       entries, and populates `tip` with a known-good DOM tree:
 *         <div class="hash-matrix-tooltip-hex">{hex}</div>
 *         <div class="hash-matrix-tooltip-status">{status}</div>?
 *         <div class="hash-matrix-tooltip-nodes">
 *           <div>{rowText}</div>...
 *         </div>?
 *   (3) An XSS payload (`<img src=x onerror=alert(1)>`) injected into a row
 *       text appears as that LITERAL STRING in the row's textContent — no
 *       child <img> element is created.
 *   (4) Source no longer contains `tip.innerHTML = td.dataset.tip`.
 */
'use strict';
const fs = require('fs');
const assert = require('assert');
const vm = require('vm');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

const SRC = fs.readFileSync('public/analytics.js', 'utf8');

// --------------------------------------------------------------------------
// Minimal DOM mock — enough to exercise createElement / appendChild /
// removeChild / textContent / dataset / firstChild walking. We deliberately
// do NOT parse HTML — assigning `.innerHTML` is a programming error here
// (would silently lose the assertion's force), so we throw on it.
// --------------------------------------------------------------------------
function makeEl(tagName) {
  const el = {
    tagName: tagName.toUpperCase(),
    className: '',
    children: [],
    style: {},
    _textContent: '',
    get textContent() {
      if (this.children.length === 0) return this._textContent;
      return this.children.map(c => c.textContent).join('');
    },
    set textContent(v) {
      this.children = [];
      this._textContent = String(v);
    },
    set innerHTML(_v) {
      throw new Error('innerHTML write is forbidden in this mock (ANL-1 contract violation)');
    },
    get innerHTML() { throw new Error('innerHTML read not implemented'); },
    get firstChild() { return this.children[0] || null; },
    appendChild(c) { this.children.push(c); c.parentNode = this; return c; },
    removeChild(c) {
      const i = this.children.indexOf(c);
      if (i === -1) throw new Error('removeChild: not found');
      this.children.splice(i, 1);
      return c;
    },
    replaceChildren() { this.children = []; this._textContent = ''; },
    querySelector(sel) {
      // very limited: '.classname'
      if (sel.startsWith('.')) {
        const cls = sel.slice(1);
        const walk = (n) => {
          for (const c of n.children) {
            if ((c.className || '').split(/\s+/).includes(cls)) return c;
            const r = walk(c); if (r) return r;
          }
          return null;
        };
        return walk(this);
      }
      throw new Error('querySelector: only .class supported, got ' + sel);
    },
    querySelectorAll(sel) {
      if (sel.startsWith('.')) {
        const cls = sel.slice(1);
        const out = [];
        const walk = (n) => {
          for (const c of n.children) {
            if ((c.className || '').split(/\s+/).includes(cls)) out.push(c);
            walk(c);
          }
        };
        walk(this);
        return out;
      }
      throw new Error('querySelectorAll: only .class supported, got ' + sel);
    },
  };
  return el;
}
const fakeDocument = {
  createElement: (t) => makeEl(t),
};

// --------------------------------------------------------------------------
// Extract the production function under test.
// Contract: analytics.js must define a function whose body, given a `tip`
// element and a `td` element with `data-tip-*` dataset entries, populates
// `tip` with the spec'd DOM (per file header).
// --------------------------------------------------------------------------
function loadBuildMatrixTipChildren() {
  // Find the function definition. We accept either a `function` declaration
  // or a `const ... = function (...) { ... }` form. The body must be
  // self-contained (no closures over esc / hashTooltipHtml).
  const m = SRC.match(
    /function buildMatrixTipChildren\s*\(([^)]*)\)\s*\{([\s\S]*?)\n\s{2}\}/
  );
  if (!m) throw new Error(
    'production function buildMatrixTipChildren(tip, td) not found in public/analytics.js'
  );
  const params = m[1];
  const body = m[2];
  // Compile in a sandbox where `document` is our fake.
  const ctx = { document: fakeDocument, console };
  vm.createContext(ctx);
  const factorySrc = `(function (${params}) {\n${body}\n})`;
  return vm.runInContext(factorySrc, ctx);
}

// --------------------------------------------------------------------------
// Extract the hashCellTd template (post-fix) and assert it no longer emits a
// monolithic data-tip HTML string. It MUST emit data-tip-hex (and optionally
// data-tip-status / data-tip-lines / data-tip-breakdown) attributes built
// from a spec object, not pre-rendered HTML.
// --------------------------------------------------------------------------
function extractHashCellTd() {
  const m = SRC.match(
    /function hashCellTd\(([^)]*)\)\s*\{([\s\S]*?)\n\s{2}\}/
  );
  if (!m) throw new Error('hashCellTd function not found');
  return { params: m[1].split(',').map(s => s.trim()), body: m[2] };
}

console.log('\n=== ANL-1 behavioral tooltip render ===');

test('analytics.js no longer assigns td.dataset.tip to tip.innerHTML', () => {
  assert.ok(!/\btip\.innerHTML\s*=\s*td\.dataset\.tip\b/.test(SRC),
    'mutation-XSS regression: tip.innerHTML = td.dataset.tip is back');
});

test('hashCellTd no longer carries pre-rendered HTML in a single data-tip attr', () => {
  const { body } = extractHashCellTd();
  // The broken contract: tipHtml string interpolated into data-tip=".."
  // and a `tipHtml.replace(/"/g,'&quot;')` call. Either being present means
  // we still ship structured HTML through the attribute round-trip.
  assert.ok(!/data-tip\s*=\s*"\$\{tipHtml/.test(body),
    'hashCellTd still writes pre-rendered HTML to data-tip="${tipHtml...}"');
  assert.ok(!/tipHtml\.replace\(\/"/g.test(body) && !/tipHtml\.replace\(\/"\/g/.test(body),
    'hashCellTd still references tipHtml.replace — HTML-string contract not removed');
  // Positive: must emit per-field dataset attributes built from a spec.
  assert.ok(/data-tip-hex/.test(body),
    'hashCellTd missing data-tip-hex attribute (spec-driven contract)');
});

test('buildMatrixTipChildren populates structured DOM from dataset fields', () => {
  const fn = loadBuildMatrixTipChildren();
  const tip = makeEl('div');
  const td = {
    dataset: {
      tipHex: '0xAB',
      tipStatus: '2 nodes — COLLISION',
      tipLines: 'alice\u001fbob',
    },
  };
  fn(tip, td);
  // a) three children: hex, status, nodes wrap
  assert.strictEqual(tip.children.length, 3,
    `expected 3 children (hex/status/nodes), got ${tip.children.length}`);
  const hexEl = tip.children[0];
  const statusEl = tip.children[1];
  const nodesEl = tip.children[2];
  assert.ok((hexEl.className || '').includes('hash-matrix-tooltip-hex'),
    'first child missing .hash-matrix-tooltip-hex class: ' + hexEl.className);
  assert.strictEqual(hexEl.textContent, '0xAB');
  assert.ok((statusEl.className || '').includes('hash-matrix-tooltip-status'),
    'second child missing .hash-matrix-tooltip-status class: ' + statusEl.className);
  assert.strictEqual(statusEl.textContent, '2 nodes — COLLISION');
  assert.ok((nodesEl.className || '').includes('hash-matrix-tooltip-nodes'),
    'third child missing .hash-matrix-tooltip-nodes class: ' + nodesEl.className);
  // b) two per-row children inside nodes wrap, textContent = each name
  assert.strictEqual(nodesEl.children.length, 2,
    `expected 2 row children, got ${nodesEl.children.length}`);
  assert.strictEqual(nodesEl.children[0].textContent, 'alice');
  assert.strictEqual(nodesEl.children[1].textContent, 'bob');
});

test('buildMatrixTipChildren omits status and nodes wrap when fields absent', () => {
  const fn = loadBuildMatrixTipChildren();
  const tip = makeEl('div');
  fn(tip, { dataset: { tipHex: '0x00' } });
  assert.strictEqual(tip.children.length, 1,
    `expected only hex child when status/lines absent, got ${tip.children.length}`);
  assert.strictEqual(tip.children[0].textContent, '0x00');
});

test('buildMatrixTipChildren replaces previous tooltip content on re-entry', () => {
  const fn = loadBuildMatrixTipChildren();
  const tip = makeEl('div');
  fn(tip, { dataset: { tipHex: '0x11', tipStatus: 'first' } });
  fn(tip, { dataset: { tipHex: '0x22', tipStatus: 'second' } });
  assert.strictEqual(tip.children[0].textContent, '0x22',
    'previous tooltip content was not replaced on second mouseover');
  assert.strictEqual(tip.children[1].textContent, 'second');
});

test('XSS payload in node-controlled row stays as text, never becomes HTML', () => {
  const fn = loadBuildMatrixTipChildren();
  const tip = makeEl('div');
  const XSS = '<img src=x onerror=alert(1)>';
  fn(tip, {
    dataset: {
      tipHex: '0xAB',
      tipStatus: '1 node',
      tipLines: XSS, // single row, malicious node name
    },
  });
  const nodesEl = tip.children[2];
  assert.ok(nodesEl, 'nodes wrap missing');
  assert.strictEqual(nodesEl.children.length, 1,
    `expected 1 row child, got ${nodesEl.children.length}`);
  const row = nodesEl.children[0];
  // The literal payload survives as a STRING (textContent). The mock's
  // innerHTML setter throws — so any child element created from parsing
  // the string would have crashed the call by now. Defense in depth: also
  // assert no child element with tagName IMG exists anywhere in tip.
  assert.strictEqual(row.textContent, XSS,
    'row textContent does not equal the raw payload string: ' + row.textContent);
  const walk = (n) => {
    if (n.tagName === 'IMG') throw new Error('IMG element materialized — XSS leak');
    for (const c of (n.children || [])) walk(c);
  };
  walk(tip);
});

test('User no longer sees literal "<div" inside the tooltip text', () => {
  // This is the rendered-bug check: with the round-1 fix the tooltip's
  // textContent (what the user sees) contained literal "<div class=...>"
  // strings because the data-tip payload was an HTML string. After the
  // proper fix, the textContent of the tooltip element is just the
  // concatenated field strings, never any HTML markup.
  const fn = loadBuildMatrixTipChildren();
  const tip = makeEl('div');
  fn(tip, {
    dataset: {
      tipHex: '0xAB',
      tipStatus: 'COLLISION',
      tipLines: 'alice\u001fbob',
    },
  });
  const visible = tip.textContent;
  assert.ok(!/<div\b/i.test(visible),
    'literal <div tag string visible in tooltip text — user sees raw HTML: ' + visible);
  assert.ok(!/<\/div>/i.test(visible),
    'literal </div> string visible in tooltip text: ' + visible);
  // Sanity: real field values DO appear.
  assert.ok(visible.includes('0xAB') && visible.includes('COLLISION')
    && visible.includes('alice') && visible.includes('bob'),
    'expected field values missing from rendered text: ' + visible);
});

console.log('\n' + '═'.repeat(48));
console.log(`  ANL-1 tooltip render: ${passed} passed, ${failed} failed`);
console.log('═'.repeat(48));
if (failed > 0) process.exit(1);
