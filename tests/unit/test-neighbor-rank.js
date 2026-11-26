'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Unit tests for public/neighbor-rank.js pure helpers and node-reach's Rank card (#2100).
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

function load(file, extra) {
  const sandbox = Object.assign({
    window: {}, URLSearchParams, encodeURIComponent,
    escapeHtml: function (s) {
      return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    },
  }, extra || {});
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public', file), 'utf8'), sandbox);
  return sandbox;
}

const NR = load('neighbor-rank.js').window.NeighborRank;
let n = 0;
function t(name, fn) { fn(); n++; console.log('  ok ' + name); }

t('buildQuery omits empty q/role and pages by offset', () => {
  assert.strictEqual(NR.buildQuery({}), 'offset=0&limit=50');
  assert.strictEqual(NR.buildQuery({ q: ' hill top ', role: 'repeater', page: 3 }), 'q=hill%20top&role=repeater&offset=100&limit=50');
});

t('buildQuery sanitizes bad role, page and over-long q', () => {
  assert.strictEqual(NR.buildQuery({ role: 'bogus', page: -4 }), 'offset=0&limit=50');
  assert.strictEqual(NR.buildQuery({ page: 'abc' }), 'offset=0&limit=50');
  const q = decodeURIComponent(NR.buildQuery({ q: 'x'.repeat(200) }).split('&')[0].slice(2));
  assert.strictEqual(q.length, 64);
  // The cap counts characters, as the server does, and never cuts one in two.
  const qe = decodeURIComponent(NR.buildQuery({ q: 'é'.repeat(70) }).split('&')[0].slice(2));
  assert.strictEqual(qe, 'é'.repeat(64));
  const emoji = '\u{1F4E1}'; // 2 UTF-16 units, 1 character
  const qm = decodeURIComponent(NR.buildQuery({ q: 'a' + emoji.repeat(70) }).split('&')[0].slice(2));
  assert.strictEqual(Array.from(qm).length, 64);
  assert.strictEqual(qm, 'a' + emoji.repeat(63));
});

t('parseHashState round trip', () => {
  const s = { q: 'a&b c', role: 'room', page: 4, focus: 'ab12cd' };
  const h = NR.hashUpdates(s);
  const qs = 'tab=neighbor-rank&' + Object.keys(h).filter(k => h[k]).map(k => k + '=' + encodeURIComponent(h[k])).join('&');
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.parseHashState(qs))), s);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.parseHashState('#/analytics?' + qs))), s);
});

t('parseHashState defaults on missing/bad params', () => {
  const d = { q: '', role: '', page: 1, focus: '' };
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.parseHashState(''))), d);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.parseHashState(null))), d);
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.parseHashState('role=nope&page=0&focus=<x>'))), d);
});

t('hashUpdates drops defaults', () => {
  assert.deepStrictEqual(JSON.parse(JSON.stringify(NR.hashUpdates({ page: 1 }))), { q: '', role: '', page: '', focus: '' });
});

t('rowHtml escapes names', () => {
  const h = NR.rowHtml({ rank: 1, pubkey: 'ab12', name: '<script>alert(1)</script>', role: 'repeater', neighbors: 5 });
  assert.ok(!h.includes('<script>'), h);
  assert.ok(h.includes('&lt;script&gt;'));
  assert.ok(h.includes('href="#/nodes/ab12"') && h.includes('href="#/nodes/ab12/reach"'));
});

t('rowHtml falls back to shortened pubkey and dash role', () => {
  const pk = '0123456789abcdef0123';
  const h = NR.rowHtml({ rank: 3, pubkey: pk, name: '', role: '', neighbors: 2 });
  assert.ok(h.includes('0123456789ab…'), h);
  assert.ok(!h.includes('0123456789abc…'));
  assert.ok(h.includes('#3'));
});

t('rowHtml marks the focus row only', () => {
  const e = { rank: 1, pubkey: 'ABcd', name: 'x', role: 'room', neighbors: 1 };
  assert.ok(NR.rowHtml(e, 'abcd').includes('nr-focus'));
  assert.ok(!NR.rowHtml(e, 'ffff').includes('nr-focus'));
  assert.ok(!NR.rowHtml(e).includes('nr-focus'));
});

t('pagerText', () => {
  assert.strictEqual(NR.pagerText(2, 50, 812), 'Showing 51–100 of 812');
  assert.strictEqual(NR.pagerText(17, 12, 812), 'Showing 801–812 of 812');
  assert.strictEqual(NR.pagerText(1, 0, 0), 'Showing 0 of 0');
});

// node-reach Rank card
const reachSb = load('node-reach.js', { registerPage: function () {}, document: {}, location: { hash: '' } });
const rv = reachSb.window.__meshcoreReachInternals.rankValueHtml;
t('Rank card: Not ranked when degree_rank is 0', () => {
  const h = rv({ degree_rank: 0, nodes_with_edges: 900 }, 'a'.repeat(64)).html;
  assert.ok(h.includes('Not ranked') && !h.includes('#0'), h);
});
t('Rank card: ranked value and leaderboard link', () => {
  const pk = 'abcdef0123456789'.repeat(4);
  const h = rv({ degree_rank: 7, nodes_with_edges: 900 }, pk).html;
  assert.ok(h.includes('#7 / 900'));
  assert.ok(h.includes('href="#/analytics?tab=neighbor-rank&focus=' + pk + '"'), h);
});
t('Rank card: link never computes a page (ties)', () => {
  const pk = 'abcdef0123456789'.repeat(4);
  const h = rv({ degree_rank: 120, nodes_with_edges: 900 }, pk).html;
  assert.ok(!h.includes('page='), h);
});
t('buildQuery sends focus only for a full pubkey', () => {
  const pk = 'ab'.repeat(32);
  assert.ok(NR.buildQuery({ focus: pk }).endsWith('&focus=' + pk));
  assert.ok(!NR.buildQuery({ focus: 'abcd' }).includes('focus='));
});
t('Rank card: unranked node links to the top of the list', () => {
  const h = rv({ degree_rank: 0, nodes_with_edges: 900 }, 'a'.repeat(64)).html;
  assert.ok(h.includes('href="#/analytics?tab=neighbor-rank"'), h);
});

console.log('neighbor-rank: ' + n + ' passed');
