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
};
vm.createContext(ctx);
vm.runInContext(badgeSrc + '\nobsIataBadge = obsIataBadge;', ctx);
const obsIataBadge = ctx.obsIataBadge;
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

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
process.exit(failed > 0 ? 1 : 0);
