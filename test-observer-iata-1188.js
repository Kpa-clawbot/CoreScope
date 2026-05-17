/**
 * Behavior test (#1188 / #1189 R1): public/packets.js helper `obsIataBadge`
 * must actually USE packet.observer_iata and emit the .badge-iata span.
 *
 * Earlier version was tautological — a grep over the source. A deliberately
 * broken `obsIataBadge` that returned a hardcoded string still passed every
 * assertion. This version EXTRACTS the function body, evaluates it in a
 * Node sandbox, and asserts the returned HTML for known inputs. Mutating
 * the implementation (e.g. ignoring `packet.observer_iata`, returning the
 * empty string, dropping `escapeHtml`) MUST flip this test red.
 *
 * Runs in Node.js — no browser, no jsdom required.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  \u2705 ' + msg); }
  else { failed++; console.error('  \u274c ' + msg); }
}

const src = fs.readFileSync(path.join(__dirname, 'public/packets.js'), 'utf8');

// ── Extract obsIataBadge source. Function spans a small, bounded block. ──
// We capture everything from the `function obsIataBadge` keyword up to and
// including the closing brace that terminates the *function body*. Use a
// non-greedy match then expand minimally — packets.js keeps the helper
// short and self-contained so this is robust.
function extractFn(name) {
  const re = new RegExp(
    'function\\s+' + name + '\\s*\\([^)]*\\)\\s*\\{[\\s\\S]*?\\n\\s{2}\\}',
    'm'
  );
  const m = src.match(re);
  if (!m) throw new Error('could not extract function ' + name);
  return m[0];
}

const badgeSrc = extractFn('obsIataBadge');
const groupedSrc = extractFn('groupedObserverIataBadgesHtml');

// Sandbox: provide the helpers the function depends on (escapeHtml,
// observerMap). escapeHtml mirrors the real one in public/app.js.
const ctx = {
  observerMap: new Map(),
  escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  },
  obsIataBadge: null,
  groupedObserverIataBadgesHtml: null,
};
vm.createContext(ctx);
vm.runInContext(badgeSrc + '\n' + groupedSrc + '\nobsIataBadge = obsIataBadge;\ngroupedObserverIataBadgesHtml = groupedObserverIataBadgesHtml;', ctx);
const obsIataBadge = ctx.obsIataBadge;
const groupedObserverIataBadgesHtml = ctx.groupedObserverIataBadgesHtml;
if (typeof obsIataBadge !== 'function') {
  console.error('  \u274c failed to load obsIataBadge into sandbox');
  process.exit(1);
}

// ── Behavior #1: packet.observer_iata SJC → <span class="badge-iata">SJC</span> ──
{
  const html = obsIataBadge({ observer_iata: 'SJC' });
  assert(typeof html === 'string', 'returns a string');
  assert(html.includes('class="badge-iata"'),
    'output contains class="badge-iata"');
  assert(html.includes('>SJC<'),
    'output contains the IATA value (SJC) as text content');
  assert(html === '<span class="badge-iata">SJC</span>',
    'output is exactly <span class="badge-iata">SJC</span>, got: ' + html);
}

// ── Behavior #2: no observer_iata and no observerMap entry → empty string ──
{
  const html = obsIataBadge({ observer_id: 'unknown-obs' });
  assert(html === '',
    'returns "" when packet has no observer_iata and observerMap lacks the id');
}

// ── Behavior #3: fallback to observerMap when packet.observer_iata absent ──
{
  ctx.observerMap.set('obs-fallback', { name: 'Foo', iata: 'OAK' });
  const html = obsIataBadge({ observer_id: 'obs-fallback' });
  assert(html === '<span class="badge-iata">OAK</span>',
    'falls back to observerMap.get(observer_id).iata when packet.observer_iata absent, got: ' + html);
}

// ── Behavior #4: packet.observer_iata WINS over observerMap (server-joined
//   field is authoritative; avoids per-row client lookup divergence) ──
{
  ctx.observerMap.set('obs-mismatch', { name: 'Bar', iata: 'WRONG' });
  const html = obsIataBadge({ observer_id: 'obs-mismatch', observer_iata: 'MRY' });
  assert(html === '<span class="badge-iata">MRY</span>',
    'packet.observer_iata wins over observerMap value (got: ' + html + ')');
}

// ── Behavior #5: null packet doesn't crash ──
{
  const html = obsIataBadge(null);
  assert(html === '', 'null packet returns ""');
}

// ── Behavior #6: HTML-escapes hostile IATA-like input ──
{
  const html = obsIataBadge({ observer_iata: '<script>' });
  assert(!html.includes('<script>'),
    'raw <script> not present in output (escapeHtml applied), got: ' + html);
  assert(html.includes('&lt;script&gt;'),
    'output contains the escaped form &lt;script&gt;');
}

// ── groupedObserverIataBadgesHtml (#1189 R1 UX): distinct-IATA set ──
// Mesh-operator finding: grouped row must distinguish same-region (redundant)
// from cross-region (interesting). Header pill now shows the DISTINCT IATA set.

// All same region: single badge, no +N
{
  const p = { observer_iata: 'SJC', _children: [
    { observer_iata: 'SJC' }, { observer_iata: 'SJC' }, { observer_iata: 'SJC' },
  ]};
  const html = groupedObserverIataBadgesHtml(p);
  assert(html === '<span class="badge-iata">SJC</span>',
    'all-same-region group renders ONE badge with no +N, got: ' + html);
}

// Two distinct regions: both badges visible
{
  const p = { observer_iata: 'SJC', _children: [
    { observer_iata: 'SFO' }, { observer_iata: 'SJC' },
  ]};
  const html = groupedObserverIataBadgesHtml(p);
  assert(html.includes('SJC') && html.includes('SFO'),
    'two-region group renders both IATAs, got: ' + html);
  assert(!/\+\d/.test(html),
    'no +N suffix when all distinct IATAs fit in 2 visible slots, got: ' + html);
}

// Three+ distinct regions: first 2 + +N of distinct-region count
{
  const p = { observer_iata: 'SJC', _children: [
    { observer_iata: 'SFO' }, { observer_iata: 'OAK' }, { observer_iata: 'MRY' },
  ]};
  const html = groupedObserverIataBadgesHtml(p);
  const badges = (html.match(/badge-iata/g) || []).length;
  assert(badges === 2, 'shows 2 visible badges when distinct-region count > 2, got: ' + badges);
  assert(/\+2$/.test(html.trim()),
    'trailing +2 reflects distinct-region overflow count, got: ' + html);
}

// Falls back to observerMap when packets lack observer_iata field
{
  ctx.observerMap.clear();
  ctx.observerMap.set('obs-a', { name: 'A', iata: 'SJC' });
  ctx.observerMap.set('obs-b', { name: 'B', iata: 'SFO' });
  const p = { observer_id: 'obs-a', _children: [{ observer_id: 'obs-b' }] };
  const html = groupedObserverIataBadgesHtml(p);
  assert(html.includes('SJC') && html.includes('SFO'),
    'falls back to observerMap for missing observer_iata, got: ' + html);
}

// No IATA anywhere → empty string
{
  ctx.observerMap.clear();
  const html = groupedObserverIataBadgesHtml({ observer_id: 'unknown', _children: [{ observer_id: 'unknown2' }] });
  assert(html === '',
    'no IATA known anywhere → empty string, got: ' + html);
}

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
process.exit(failed > 0 ? 1 : 0);