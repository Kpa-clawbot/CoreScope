/* Unit test for buildLegendHtml() — the helper extracted from live.js
 * legend rendering IIFE per PR #1804 r1 item 7 (adv3).
 *
 * Loads the helper via vm with a stubbed window so the function is
 * directly testable without spinning Playwright + chromium.
 */
'use strict';
const fs = require('fs');
const vm = require('vm');

const labelsSrc = fs.readFileSync('public/payload-labels.js', 'utf8');
const liveSrc   = fs.readFileSync('public/live.js', 'utf8');

const ctx = {
  window: {},
  console,
  document: { addEventListener: () => {}, getElementById: () => null, querySelector: () => null, querySelectorAll: () => [] },
  navigator: { userAgent: 'node' },
  location: { hash: '', pathname: '/' },
  localStorage: { getItem: () => null, setItem: () => {}, removeItem: () => {} },
  setTimeout, clearTimeout, setInterval, clearInterval,
  fetch: () => Promise.resolve({ ok: true, json: () => ({}) }),
};
ctx.self = ctx; ctx.globalThis = ctx;
vm.createContext(ctx);

// Load canonical labels first.
vm.runInContext(labelsSrc, ctx);

// live.js is a huge file with many module-scope side effects (router,
// websocket, etc). We only need buildLegendHtml. Extract it by locating
// `function buildLegendHtml(...)` and brace-counting to the matching '}'.
function extractHelper(src) {
  const startMarker = 'function buildLegendHtml';
  const idx = src.indexOf(startMarker);
  if (idx === -1) return null;
  // find the '{' opening the body
  let i = src.indexOf('{', idx);
  if (i === -1) return null;
  let depth = 0;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { return src.slice(idx, i + 1); } }
  }
  return null;
}
const helperSrc = extractHelper(liveSrc);
if (!helperSrc) {
  // Helper hasn't been extracted yet — that's the RED state.
  console.log('  ✗ buildLegendHtml helper not found in public/live.js — extract it (PR #1804 r1 item 7)');
  console.log('=== 0 passed, 1 failed ===');
  process.exit(1);
}
vm.runInContext('var TYPE_COLORS = { ADVERT: "#22c55e", GRP_TXT: "#3b82f6", GRP_DATA: "#8b5cf6", TXT_MSG: "#f59e0b", ACK: "#6b7280", REQ: "#a855f7", RESPONSE: "#06b6d4", TRACE: "#ec4899", PATH: "#14b8a6", ANON_REQ: "#f43f5e", MULTIPART: "#0d9488", CONTROL: "#b45309", RAW_CUSTOM: "#c026d3" };', ctx);
vm.runInContext(helperSrc, ctx);
const buildLegendHtml = ctx.buildLegendHtml;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  \u2713 ' + name); }
  catch (e) { fail++; console.log('  \u2717 ' + name + ' — ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

const html = buildLegendHtml(ctx.window.PayloadLabels);

test('returns a non-empty string', () => {
  assert(typeof html === 'string' && html.length > 0, 'got ' + typeof html);
});

test('emits one <li> per enum in PayloadLabels.ORDER', () => {
  const liCount = (html.match(/<li\b/g) || []).length;
  const want = ctx.window.PayloadLabels.ORDER.length;
  assert(liCount === want, 'li count: want ' + want + ', got ' + liCount);
});

test('every row carries data-enum="<ENUM>" attribute', () => {
  for (const k of ctx.window.PayloadLabels.ORDER) {
    assert(html.indexOf('data-enum="' + k + '"') !== -1,
      'data-enum="' + k + '" missing');
  }
});

test('every row uses the em-dash separator (uniform typography, item 1)', () => {
  for (const k of ctx.window.PayloadLabels.ORDER) {
    const entry = ctx.window.PayloadLabels[k];
    const wantSnippet = entry.short + ' \u2014 ' + entry.long;
    assert(html.indexOf(wantSnippet) !== -1,
      k + ': expected snippet "' + wantSnippet + '" not found in legend html');
  }
});

test('no row uses " / " as the SHORT/LONG separator (uniform typography)', () => {
  // Slashes are fine inside long descriptions (e.g. "path discovery /
  // return-path advertisement"). Forbid only the legacy `SHORT / Other —`
  // structure that used to render ACK differently.
  for (const k of ctx.window.PayloadLabels.ORDER) {
    const entry = ctx.window.PayloadLabels[k];
    const badSnippet = entry.short + ' / ';
    assert(html.indexOf(badSnippet) === -1,
      k + ': legacy slash-after-short separator survived ("' + badSnippet + '")');
  }
});

console.log('=== ' + pass + ' passed, ' + fail + ' failed ===');
process.exit(fail === 0 ? 0 : 1);
