/**
 * The docked terrain-profile panel shared by route line of sight and the
 * planner: loads the REAL public/los.js and public/los-panel.js in a vm
 * context with a small fake DOM (it parses the panel's own markup), a stub
 * Leaflet and a hand-run requestAnimationFrame.
 *
 * Covers esc and the formatters, the LRU, the profile markup (analysed,
 * loading and failed, with hostile names), the readout, the panel shell
 * (buttons, Escape, focus after a step, navigation), mounting, fit options,
 * the crosshair pointer handling and the map pieces, plus source checks that
 * route-los.js and planner.js keep no copies of what lives here.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const REPO_ROOT = path.resolve(__dirname, '..', '..');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const read = f => fs.readFileSync(path.join(REPO_ROOT, 'public', f), 'utf8');

// ─── Fake DOM: enough to parse and query the panel's markup ──────────────

const ENT = { '&amp;': '&', '&lt;': '<', '&gt;': '>', '&quot;': '"', '&#39;': "'" };
const decode = s => s.replace(/&(amp|lt|gt|quot|#39);/g, m => ENT[m]);
const VOID = new Set(['input', 'br', 'img', 'hr', 'meta', 'link']);

class FakeText {
  constructor(t) { this.nodeType = 3; this.data = t; this.parentNode = null; }
  get textContent() { return this.data; }
}

class FakeEl {
  constructor(tag, doc) {
    this.tagName = String(tag).toUpperCase();
    this.nodeType = 1;
    this.childNodes = [];
    this.parentNode = null;
    this.attrs = {};
    this.listeners = {};
    this.ownerDocument = doc;
    this.disabled = false;
    this.tabIndex = 0;
    this.rect = { left: 0, width: 0 };
  }
  get className() { return this.attrs.class || ''; }
  set className(v) { this.attrs.class = String(v); }
  get classList() {
    const get = () => new Set(this.className.split(/\s+/).filter(Boolean));
    const put = s => { this.className = Array.from(s).join(' '); };
    return {
      add: (...c) => { const s = get(); c.forEach(x => s.add(x)); put(s); },
      remove: (...c) => { const s = get(); c.forEach(x => s.delete(x)); put(s); },
      contains: c => get().has(c),
      toggle: (c, f) => { const s = get(); const on = f === undefined ? !s.has(c) : !!f; if (on) s.add(c); else s.delete(c); put(s); return on; }
    };
  }
  get children() { return this.childNodes.filter(n => n.nodeType === 1); }
  get textContent() { return this.childNodes.map(c => c.textContent).join(''); }
  set textContent(v) { this.childNodes = []; if (String(v) !== '') this.appendChild(new FakeText(String(v))); }
  set innerHTML(html) {
    this.childNodes = [];
    this._html = String(html);
    const re = /<\/([a-zA-Z][\w:-]*)\s*>|<([a-zA-Z][\w:-]*)((?:\s+[\w:-]+="[^"]*")*)\s*(\/?)>|([^<]+)/g;
    const stack = [this];
    let m;
    while ((m = re.exec(this._html))) {
      const top = stack[stack.length - 1];
      if (m[1]) { stack.pop(); continue; }
      if (m[5] != null) { top.appendChild(new FakeText(decode(m[5]))); continue; }
      const el = new FakeEl(m[2], this.ownerDocument);
      (m[3].match(/[\w:-]+="[^"]*"/g) || []).forEach(a => {
        const eq = a.indexOf('=');
        el.attrs[a.slice(0, eq)] = decode(a.slice(eq + 2, -1));
      });
      top.appendChild(el);
      if (!m[4] && !VOID.has(m[2].toLowerCase())) stack.push(el);
    }
  }
  get innerHTML() { return this._html || ''; }
  get isConnected() { let n = this; while (n.parentNode) n = n.parentNode; return n === this.ownerDocument.body; }
  appendChild(n) { if (n.parentNode) n.parentNode.removeChild(n); n.parentNode = this; this.childNodes.push(n); return n; }
  removeChild(n) { const i = this.childNodes.indexOf(n); if (i >= 0) this.childNodes.splice(i, 1); n.parentNode = null; return n; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); }
  // Bubbles from this element up until stopPropagation().
  dispatch(type, extra) {
    const ev = Object.assign({
      type, target: this, stopped: false, prevented: false,
      stopPropagation() { this.stopped = true; }, preventDefault() { this.prevented = true; }
    }, extra || {});
    for (let n = this; n; n = n.parentNode) {
      (n.listeners && n.listeners[type] || []).forEach(fn => fn(ev));
      if (ev.stopped) break;
    }
    return ev;
  }
  click() { if (!this.disabled) this.dispatch('click'); }
  focus() { this.ownerDocument.activeElement = this; }
  getBoundingClientRect() { return this.rect; }
  // Compound selectors (tag, .class, [attr], [attr="v"]), descendant chains.
  matches(sel) { return matchCompound(this, sel); }
  closest(sel) { for (let n = this; n && n.nodeType === 1; n = n.parentNode) if (n.matches(sel)) return n; return null; }
  querySelectorAll(sel) {
    const parts = sel.trim().split(/\s+/);
    const out = [];
    const walk = n => n.children.forEach(c => {
      if (matchChain(c, parts, this)) out.push(c);
      walk(c);
    });
    walk(this);
    return out;
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
}

function matchCompound(el, sel) {
  const re = /^([a-zA-Z][\w-]*)?((?:\.[\w-]+|\[[\w-]+(?:="[^"]*")?\])*)$/;
  const m = re.exec(sel);
  if (!m) throw new Error('fake DOM: unsupported selector ' + sel);
  if (m[1] && el.tagName !== m[1].toUpperCase()) return false;
  const cls = new Set(el.className.split(/\s+/));
  return (m[2].match(/\.[\w-]+|\[[^\]]+\]/g) || []).every(p => {
    if (p[0] === '.') return cls.has(p.slice(1));
    const a = /^\[([\w-]+)(?:="([^"]*)")?\]$/.exec(p);
    const v = el.getAttribute(a[1]);
    return a[2] == null ? v != null : v === a[2];
  });
}
function matchChain(el, parts, root) {
  if (!matchCompound(el, parts[parts.length - 1])) return false;
  let i = parts.length - 2;
  for (let n = el.parentNode; i >= 0 && n && n !== root.parentNode; n = n.parentNode) {
    if (n.nodeType === 1 && matchCompound(n, parts[i])) i--;
  }
  return i < 0;
}

function makeDocument() {
  const doc = { activeElement: null };
  doc.body = new FakeEl('body', doc);
  doc.createElement = t => new FakeEl(t, doc);
  doc.activeElement = doc.body;
  return doc;
}

// ─── Stub Leaflet ─────────────────────────────────────────────────────────

function makeLeaflet() {
  const calls = { disableClick: 0, disableScroll: 0 };
  const L = {
    marker(ll, opts) {
      return {
        ll, opts, tip: null, handlers: {}, el: null,
        bindTooltip(t, o) { this.tip = { text: t, opts: o }; return this; },
        on(t, fn) { (this.handlers[t] = this.handlers[t] || []).push(fn); return this; },
        fire(t, e) { (this.handlers[t] || []).forEach(fn => fn(e)); },
        setLatLng(x) { this.ll = x; return this; },
        getElement() { return this.el; }
      };
    },
    divIcon(o) { return o; },
    polyline(lls, opts) { return { lls, opts }; },
    DomEvent: { disableClickPropagation() { calls.disableClick++; }, disableScrollPropagation() { calls.disableScroll++; } }
  };
  return { L, calls };
}
function makeLayer() {
  const set = new Set();
  return { set, addLayer(l) { set.add(l); return this; }, removeLayer(l) { set.delete(l); return this; }, hasLayer: l => set.has(l) };
}

// ─── Context loader ───────────────────────────────────────────────────────

function load(opts) {
  opts = opts || {};
  const frames = [];
  const doc = makeDocument();
  const { L, calls } = makeLeaflet();
  const ctx = {
    console, Map, Math, String, Number, Array, Object, isFinite,
    document: doc,
    innerWidth: opts.innerWidth || 1280,
    matchMedia: q => ({ matches: !!opts.reducedMotion && /reduce/.test(q) }),
    requestAnimationFrame: fn => { frames.push(fn); return frames.length; },
    cancelAnimationFrame: id => { frames[id - 1] = null; },
    setTimeout, clearTimeout
  };
  if (opts.leaflet !== false) ctx.L = L;
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(read('los.js'), ctx);
  vm.runInContext(read('los-panel.js'), ctx);
  const runFrames = () => { const q = frames.splice(0); q.forEach(fn => fn && fn()); return q.filter(Boolean).length; };
  return { ctx, P: ctx.MeshLOSPanel, LOS: ctx.MeshLOS, doc, calls, runFrames };
}

const N = 100;
const flat = h => Array.from({ length: N }, () => h);
const EVIL_A = '"><img src=x onerror=alert(1)>';
const EVIL_B = "' onfocus=alert(2) x='<script>alert(3)</script>";
const TAG_RE = /<[^>]*>/g;
const WELL_FORMED = /^<\/?[a-zA-Z][\w:-]*(\s+[\w:-]+="[^"<]*")*\s*\/?>$/;
const badTags = html => (html.match(TAG_RE) || []).filter(t => !WELL_FORMED.test(t) || /\son[a-z]+="/i.test(t) || /^<\/?(img|script)\b/i.test(t));

const { P, LOS } = load();
const ridgeElev = flat(10); ridgeElev[50] = 900;
const blocked = LOS.analyze({ distanceKm: 2.7, elevM: ridgeElev, antennaAM: 5, antennaBM: 5, freqMHz: 910.525, kFactor: 4 / 3 });
const clear = LOS.analyze({ distanceKm: 12.5, elevM: flat(10), antennaAM: 30, antennaBM: 30, freqMHz: 910.525, kFactor: 4 / 3 });

console.log('\n=== esc ===');
{
  assert(P.esc === LOS.esc, 'MeshLOSPanel.esc is MeshLOS.esc (one implementation)');
  assert(P.esc('<a href="x">\'&') === '&lt;a href=&quot;x&quot;&gt;&#39;&amp;', 'escapes & < > " \'');
  assert(P.esc(null) === '' && P.esc(undefined) === '' && P.esc(0) === '0', 'null/undefined → empty, numbers → text');
}

console.log('\n=== formatters ===');
{
  assert(P.fmtKm(3.14159) === '3.14 km' && P.fmtKm(42.26) === '42.3 km' && P.fmtKm(123.4) === '123 km', 'fmtKm: 2 / 1 / 0 decimals by size');
  assert(P.fmtKm(0) === '0.00 km' && P.fmtKm('7.5') === '7.50 km', 'fmtKm: zero and numeric strings');
  assert(P.fmtKm(-12.345) === '-12.3 km', 'fmtKm: sizes by magnitude');
  assert([null, undefined, '', 'x', NaN, Infinity].every(x => P.fmtKm(x) === '—'), 'fmtKm: unknown → —');
  assert(P.fmtM(12.6) === '13 m' && P.fmtM(-3.2) === '-3 m' && P.fmtM('40') === '40 m', 'fmtM: whole metres');
  assert([null, '', 'x', NaN].every(x => P.fmtM(x) === '—'), 'fmtM: unknown → —');
  assert(P.fmtObstruction(0.2) === '1 m' && P.fmtObstruction(41.6) === '42 m', 'fmtObstruction: whole metres, never "0 m"');
}

console.log('\n=== createLRU ===');
{
  const c = P.createLRU(3);
  c.put('a', 1); c.put('b', 2); c.put('c', 3);
  assert(c.size() === 3 && c.get('a') === 1, 'holds up to max; get returns the value');
  c.put('d', 4);
  assert(!c.has('b') && c.has('a') && c.has('c') && c.has('d'), 'put past max evicts the least recently used (get bumped "a")');
  c.put('c', 33);
  c.put('e', 5);
  assert(c.get('c') === 33 && !c.has('a') && c.size() === 3, 're-put replaces the value and makes it newest');
  assert(c.get('zz') === null, 'get of a missing key → null');
  assert(c.delete('c') === true && !c.has('c') && c.size() === 2, 'delete removes a key');
  const one = P.createLRU(0);
  one.put('x', 1); one.put('y', 2);
  assert(one.size() === 1 && one.get('y') === 2, 'a non-positive max still holds one entry (no endless eviction)');
}

console.log('\n=== profileHtml: analysed ===');
{
  const html = P.profileHtml({
    an: blocked, km: 99, nameA: 'Hill', nameB: 'Vale',
    meta: ['<span title="Bearing from Hill">35°</span>'],
    clearance: 'F1 clearance >999%',
    pill: { tone: 'bad', label: 'Blocked', title: 'Terrain crosses the line of sight' },
    error: null, hint: 'Move along the profile'
  });
  const meta = /<p class="mc-los-meta">([\s\S]*?)<\/p>/.exec(html)[1];
  const parts = meta.split('<span class="mc-los-sep" aria-hidden="true">·</span>');
  assert(parts.length === 4, 'meta: length, extra item, FSPL, clearance (got ' + parts.length + ')');
  assert(parts[0] === '<span title="Link length">2.70 km</span>', 'length comes from the analysis, not o.km');
  assert(parts[1] === '<span title="Bearing from Hill">35°</span>', 'extra meta items follow the length');
  assert(/^<span title="Free-space path loss">FSPL \d+\.\d dB<\/span>$/.test(parts[2]), 'FSPL to one decimal');
  assert(parts[3].startsWith('<span title="Worst clearance of the first Fresnel zone">F1 clearance &gt;999%</span>'), 'clearance text escaped');
  assert(parts[3].endsWith('<span class="mc-los-pill mc-los-tone-bad" title="Terrain crosses the line of sight">Blocked</span>'), 'verdict pill closes the line');
  assert(/<p class="mc-los-obst">Terrain rises \d+ m above the line of sight 1\.36 km from Hill\.<\/p>/.test(html), 'obstruction note with height, distance and end');
  assert(html.includes('<div class="mc-los-chart"><svg class="mc-los-svg"') && html.includes('<p class="mc-los-readout">Move along the profile</p>'), 'chart and readout hint');
  assert(html.indexOf('mc-los-meta') < html.indexOf('mc-los-obst') && html.indexOf('mc-los-obst') < html.indexOf('mc-los-chart'), 'order: meta, obstruction, chart');
  const noObst = P.profileHtml({ an: clear, nameA: 'A', nameB: 'B', clearance: 'x', pill: { tone: 'good', label: 'Clear', title: '' }, hint: '' });
  assert(!noObst.includes('mc-los-obst'), 'no obstruction note on a clear leg');
  assert(noObst.split('mc-los-sep').length - 1 === 2, 'no extra meta items without o.meta');
}

console.log('\n=== profileHtml: loading and failed ===');
{
  const loading = P.profileHtml({ an: null, km: 4.25, nameA: 'A', nameB: 'B', error: null, hint: 'h' });
  assert(loading === '<p class="mc-los-meta"><span title="Link length">4.25 km</span><span class="mc-los-pill mc-los-tone-muted mc-los-loading">Checking…</span></p>' +
    '<div class="mc-los-chart mc-los-chart-wait"><div class="mc-los-chart-state" role="status">Loading terrain profile…</div></div>', 'loading: length, Checking… pill, full-height wait box');
  const err = P.chartErrorHtml('No terrain profile for this link', 'Terrain data unavailable', '<button type="button" class="mc-los-retry">Try again</button>');
  assert(err === '<strong>No terrain profile for this link</strong><span>Terrain data unavailable.</span><button type="button" class="mc-los-retry">Try again</button>', 'chartErrorHtml: headline, reason, extra');
  assert(P.chartErrorHtml('No terrain data for this leg') === '<strong>No terrain data for this leg</strong>', 'chartErrorHtml: headline only');
  const failed = P.profileHtml({ an: null, km: 4.25, nameA: 'A', nameB: 'B', error: err, hint: 'h' });
  assert(failed.includes('<span class="mc-los-pill mc-los-tone-muted">n/a</span>'), 'failed: n/a pill, not loading');
  assert(failed.includes('<div class="mc-los-chart mc-los-chart-err"><div class="mc-los-chart-state mc-los-chart-error" role="status">' + err + '</div></div>'), 'failed: error box holds chartErrorHtml');
  assert(!failed.includes('mc-los-readout') && !failed.includes('FSPL'), 'failed: no readout, no FSPL');
}

console.log('\n=== hostile names stay inert ===');
{
  const outputs = {
    analysed: P.profileHtml({ an: blocked, nameA: EVIL_A, nameB: EVIL_B, clearance: EVIL_B, pill: { tone: 'bad', label: EVIL_A, title: EVIL_B }, hint: EVIL_A }),
    failed: P.profileHtml({ an: null, km: 1, nameA: EVIL_A, nameB: EVIL_B, error: P.chartErrorHtml(EVIL_A, EVIL_B), hint: '' }),
    readout: P.readoutHtml(blocked, 50, EVIL_A, () => EVIL_B)
  };
  Object.keys(outputs).forEach(k => {
    const bad = badTags(outputs[k]);
    assert(bad.length === 0, k + ': no live tag or handler' + (bad.length ? ' (got ' + bad[0] + ')' : ''));
  });
  assert(outputs.analysed.includes('from &quot;&gt;&lt;img src=x onerror=alert(1)&gt;.</p>'), 'obstruction note escapes the name');
}

console.log('\n=== readoutHtml ===');
{
  const seen = [];
  const ro = P.readoutHtml(blocked, 50, 'Hill', c => { seen.push(c); return Math.round(c * 100) + '%'; });
  const items = ro.split('<span class="mc-los-sep" aria-hidden="true">·</span>');
  assert(items.length === 4 && items.every(s => /^<span class="mc-los-ri">[\s\S]*<\/span>$/.test(s)), 'four mc-los-ri items');
  assert(items[0] === '<span class="mc-los-ri"><b>1.36 km</b> from Hill</span>', 'distance of the sample from A');
  assert(items[1] === '<span class="mc-los-ri">terrain <b>' + Math.round(blocked.terrainM[50]) + ' m</b></span>', 'terrain height');
  assert(items[2] === '<span class="mc-los-ri">LOS <b>' + Math.round(blocked.losM[50]) + ' m</b></span>', 'LOS height');
  const ratio = (blocked.losM[50] - blocked.terrainM[50]) / blocked.fresnelM[50];
  assert(seen.length === 1 && Math.abs(seen[0] - ratio) < 1e-12, 'fmtClearance gets the Fresnel ratio');
  assert(P.readoutHtml(blocked, 0, 'A', () => 'x').includes('clearance <b>—</b>') && P.readoutHtml(blocked, N - 1, 'A', () => 'x').includes('clearance <b>—</b>'), 'ends read "—"');
}

console.log('\n=== buildPanel ===');
{
  const t = load();
  const steps = [], closes = [], escapes = [];
  const el = t.P.buildPanel({
    className: 'mc-pl-profile', ariaLabel: 'Line of sight',
    prevLabel: 'Previous link', nextLabel: 'Next link', closeLabel: 'Close line of sight',
    bodyHtml: '<div class="mc-los-body"></div>',
    onStep: d => steps.push(d), onClose: () => closes.push(1), onEscape: () => escapes.push(1)
  });
  assert(el.className === 'mc-los-panel mc-pl-profile' && el.getAttribute('role') === 'dialog' && el.tabIndex === -1, 'dialog with the extra class, focusable by script');
  assert(el.getAttribute('aria-label') === 'Line of sight', 'initial aria-label');
  assert(el.children.map(c => c.className).join('|') === 'mc-los-head|mc-los-body|mc-los-foot', 'head, the caller body, footer');
  const btn = c => el.querySelector('.' + c);
  assert(btn('mc-los-prev').getAttribute('aria-label') === 'Previous link' && btn('mc-los-next').getAttribute('aria-label') === 'Next link' &&
    btn('mc-los-close').getAttribute('aria-label') === 'Close line of sight', 'button labels from the options');
  assert(['ph-caret-left', 'ph-caret-right', 'ph-x'].every((n, i) =>
    el.querySelectorAll('.mc-los-btn')[i].querySelector('use').getAttribute('href') === '/icons/phosphor-sprite.svg#' + n), 'phosphor glyphs on the buttons');
  assert(el.querySelector('.mc-los-nav').children.map(c => c.className).join('|') === 'mc-los-btn mc-los-prev|mc-los-pos|mc-los-btn mc-los-next|mc-los-btn mc-los-close', 'nav order: prev, position, next, close');
  btn('mc-los-prev').click(); btn('mc-los-next').click(); btn('mc-los-close').click();
  assert(steps.join(',') === '-1,1' && closes.length === 1, 'prev / next step -1 / +1, close closes');
  const esc = el.dispatch('keydown', { key: 'Escape' });
  assert(escapes.length === 1 && esc.stopped && esc.prevented, 'Escape inside the panel: onEscape, stopped, default prevented');
  const other = el.dispatch('keydown', { key: 'Enter' });
  assert(escapes.length === 1 && !other.stopped && !other.prevented, 'other keys pass through');
  assert(t.calls.disableClick === 1 && t.calls.disableScroll === 1, 'clicks and wheel never reach the map');

  const plain = t.P.buildPanel({ prevLabel: 'p', nextLabel: 'n', closeLabel: 'c', onStep() {}, onClose() {} });
  assert(plain.className === 'mc-los-panel' && plain.getAttribute('aria-label') === null, 'no extra class, no aria-label unless given');
  const e2 = plain.dispatch('keydown', { key: 'Escape' });
  assert(!e2.stopped && !e2.prevented, 'without onEscape the panel leaves Escape to the page');
  const hostile = t.P.buildPanel({ prevLabel: EVIL_A, nextLabel: 'n', closeLabel: 'c', onStep() {}, onClose() {} });
  assert(hostile.querySelector('.mc-los-prev').getAttribute('aria-label') === EVIL_A && badTags(hostile.innerHTML).length === 0, 'labels are escaped');
  assert(load({ leaflet: false }).P.buildPanel({ prevLabel: 'p', nextLabel: 'n', closeLabel: 'c', onStep() {}, onClose() {} }).className === 'mc-los-panel', 'builds without Leaflet');
}

console.log('\n=== buildPanel: focus after a step ===');
{
  const t = load();
  let pos = 0;
  const count = 3;
  const el = t.P.buildPanel({
    prevLabel: 'p', nextLabel: 'n', closeLabel: 'c',
    onStep: d => { pos += d; t.P.setNav(el, pos, count, 'Leg'); },
    onClose() {}
  });
  t.P.mountPanel(el, t.doc.body);
  t.P.setNav(el, pos, count, 'Leg');
  const next = el.querySelector('.mc-los-next');
  next.focus();
  next.click();
  assert(pos === 1 && t.doc.activeElement === next, 'a step that leaves the button enabled keeps focus on it');
  next.click();
  assert(pos === 2 && next.disabled && t.doc.activeElement === el, 'stepping onto the last leg moves focus to the panel');
  const prev = el.querySelector('.mc-los-prev');
  prev.click(); prev.click();
  assert(pos === 0 && prev.disabled && t.doc.activeElement === el, 'and onto the first one too');
  const loose = t.P.buildPanel({ prevLabel: 'p', nextLabel: 'n', closeLabel: 'c', onStep() { loose.querySelector('.mc-los-next').disabled = true; }, onClose() {} });
  t.doc.activeElement = t.doc.body;
  loose.querySelector('.mc-los-next').click();
  assert(t.doc.activeElement === t.doc.body, 'a detached panel never takes focus');
}

console.log('\n=== setNav ===');
{
  const t = load();
  const el = t.P.buildPanel({ prevLabel: 'p', nextLabel: 'n', closeLabel: 'c', onStep() {}, onClose() {} });
  const q = c => el.querySelector('.' + c);
  t.P.setNav(el, 1, 3, 'Link');
  assert(q('mc-los-pos').textContent === 'Link 2 of 3' && !q('mc-los-nav').classList.contains('mc-los-single'), 'many: "Link 2 of 3", nav shown');
  assert(!q('mc-los-prev').disabled && !q('mc-los-next').disabled, 'middle leg: both arrows enabled');
  t.P.setNav(el, 0, 3, 'Leg');
  assert(q('mc-los-prev').disabled && !q('mc-los-next').disabled && q('mc-los-pos').textContent === 'Leg 1 of 3', 'first leg: prev disabled, noun from the caller');
  t.P.setNav(el, 2, 3, 'Leg');
  assert(!q('mc-los-prev').disabled && q('mc-los-next').disabled, 'last leg: next disabled');
  t.P.setNav(el, 0, 1, 'Leg');
  assert(q('mc-los-nav').classList.contains('mc-los-single') && q('mc-los-pos').textContent === '' &&
    q('mc-los-prev').disabled && q('mc-los-next').disabled, 'single leg: mc-los-single, no position, arrows off');
}

console.log('\n=== mountPanel / unmountPanel ===');
{
  const t = load();
  const host = t.doc.createElement('div');
  const el = t.doc.createElement('div');
  t.P.mountPanel(el, host);
  assert(el.parentNode === host && host.classList.contains('mc-los-open'), 'mount: appended, host marked mc-los-open');
  t.P.unmountPanel(el);
  assert(el.parentNode === null && !host.classList.contains('mc-los-open'), 'unmount: removed, mark cleared on its parent');
  host.classList.add('mc-los-open');
  t.P.unmountPanel(null, host);
  assert(!host.classList.contains('mc-los-open'), 'unmount with no panel still clears the given host');
  let threw = false;
  try { t.P.unmountPanel(null); t.P.unmountPanel(t.doc.createElement('div')); } catch (e) { threw = true; }
  assert(!threw, 'unmount of nothing or a detached panel is a no-op');
}

console.log('\n=== fitOptions ===');
{
  const map = y => ({ getSize: () => ({ x: 1000, y }) });
  const desk = load().P.fitOptions(map(800), { offsetHeight: 300 });
  assert(JSON.stringify(desk) === JSON.stringify({ paddingTopLeft: [48, 48], paddingBottomRight: [48, 364], maxZoom: 14, animate: true }), 'desktop: panel docked at the bottom pads the bottom');
  const mob = load({ innerWidth: 390 }).P.fitOptions(map(700), { offsetHeight: 300 });
  assert(JSON.stringify(mob) === JSON.stringify({ paddingTopLeft: [24, 340], paddingBottomRight: [24, 24], maxZoom: 14, animate: true }), 'phone: panel docked at the top pads the top');
  const tall = load().P.fitOptions(map(400), { offsetHeight: 900 });
  assert(tall.paddingBottomRight[1] === 320, 'padding never leaves the leg less than 120 px');
  assert(load().P.fitOptions(map(800), {}).paddingBottomRight[1] === 64, 'an unmeasured panel counts as 0 px');
  assert(load({ reducedMotion: true }).P.fitOptions(map(800), { offsetHeight: 0 }).animate === false, 'no animation under prefers-reduced-motion');
  assert(load({ innerWidth: 767 }).P.isMobile() && !load({ innerWidth: 768 }).P.isMobile(), 'isMobile matches the 767 px breakpoint');
}

console.log('\n=== crosshair ===');
{
  const s = P.snap(blocked, 0.5);
  assert(s.i === 50 && s.t === 50 / 99, 'snap: nearest sample and its t');
  assert(P.snap(blocked, 0).i === 0 && P.snap(blocked, 1).i === 99, 'snap: ends');

  // Two charts in one panel, as the planner stacks them.
  const t = load();
  const root = t.doc.createElement('div');
  root.innerHTML = [0, 1].map(k => '<section data-prof="' + k + '"><div class="mc-los-chart">' +
    t.LOS.renderProfileSvg(blocked, { nameA: 'A', nameB: 'B' }) + '</div><p class="mc-los-readout">hint</p></section>').join('');
  const svgs = root.querySelectorAll('.mc-los-svg');
  svgs.forEach(svg => { svg.rect = { left: 100, width: 720 }; });
  const hits = root.querySelectorAll('.mc-los-hit');
  const moves = [], leaves = [];
  const h = t.P.bindCrosshair(root, {
    keyOf: hit => Number(hit.closest('[data-prof]').getAttribute('data-prof')),
    svgOf: k => root.querySelector('[data-prof="' + k + '"] .mc-los-svg'),
    move: (k, tt) => moves.push([k, tt]),
    leave: () => leaves.push(1)
  });
  const x = t.LOS.viewXAtT(0.25) + 100;
  hits[1].dispatch('pointermove', { clientX: x - 50 });
  hits[1].dispatch('pointermove', { clientX: x });
  assert(moves.length === 0, 'moves wait for the next frame');
  assert(t.runFrames() === 1 && moves.length === 1, 'two moves in one frame → one update');
  assert(moves[0][0] === 1 && Math.abs(moves[0][1] - 0.25) < 1e-9, 'the latest pointer, on the chart it is over, as t along the link');
  hits[0].dispatch('pointerdown', { clientX: 100 });
  t.runFrames();
  assert(moves[1][0] === 0 && moves[1][1] === 0, 'pointerdown (touch) reads too; left of the plot clamps to 0');
  svgs[0].dispatch('pointermove', { clientX: x });
  assert(t.runFrames() === 0 && moves.length === 2, 'pointer off the plot area is ignored');

  hits[0].dispatch('pointerout', { pointerType: 'mouse', relatedTarget: hits[1] });
  assert(leaves.length === 0, 'moving from one chart to another is not a leave');
  hits[0].dispatch('pointerout', { pointerType: 'touch', relatedTarget: root });
  assert(leaves.length === 0, 'a touch ending keeps its reading');
  svgs[0].dispatch('pointerout', { pointerType: 'mouse', relatedTarget: root });
  assert(leaves.length === 0, 'leaving something other than the plot is not a leave');
  hits[0].dispatch('pointermove', { clientX: x });
  hits[0].dispatch('pointerout', { pointerType: 'mouse', relatedTarget: root });
  assert(leaves.length === 1 && t.runFrames() === 0 && moves.length === 2, 'a mouse leaving the plot: leave(), and its pending frame dropped');

  hits[0].dispatch('pointermove', { clientX: x });
  h.cancel();
  assert(t.runFrames() === 0 && moves.length === 2, 'cancel() drops a pending frame');
  svgs[0].rect = { left: 0, width: 0 };
  hits[0].dispatch('pointermove', { clientX: x });
  t.runFrames();
  assert(moves.length === 2, 'a chart with no width (hidden or detached) is skipped');

  const skip = load();
  const r2 = skip.doc.createElement('div');
  r2.innerHTML = '<div class="mc-los-chart">' + skip.LOS.renderProfileSvg(blocked, {}) + '</div>';
  r2.querySelector('.mc-los-svg').rect = { left: 0, width: 720 };
  const got = [];
  skip.P.bindCrosshair(r2, { keyOf: () => null, move: (k, tt) => got.push(tt), leave() {} });
  r2.querySelector('.mc-los-hit').dispatch('pointermove', { clientX: 300 });
  assert(skip.runFrames() === 0 && got.length === 0, 'keyOf → null ignores the chart');
  skip.P.bindCrosshair(r2, { move: (k, tt) => got.push([k, tt]), leave() {} });
  r2.querySelector('.mc-los-hit').dispatch('pointermove', { clientX: skip.LOS.viewXAtT(0.5) });
  skip.runFrames();
  assert(got.length === 1 && got[0][0] === 0 && Math.abs(got[0][1] - 0.5) < 1e-9, 'defaults: key 0, the root\'s only chart');
}

console.log('\n=== showChartCursor / hideChartCursor ===');
{
  const t = load();
  const root = t.doc.createElement('div');
  root.innerHTML = [0, 1].map(() => '<section><div class="mc-los-chart">' + t.LOS.renderProfileSvg(blocked, {}) +
    '</div><p class="mc-los-readout">hint</p></section>').join('');
  const [s0, s1] = root.querySelectorAll('section');
  const cur = s => s.querySelector('.mc-los-cursor');
  assert(cur(s0).getAttribute('visibility') === 'hidden', 'the cursor starts hidden');
  t.P.showChartCursor(s0, 50 / 99, '<b>1.36 km</b>');
  const vx = String(Math.round(t.LOS.viewXAtT(50 / 99) * 100) / 100);
  assert(cur(s0).getAttribute('x1') === vx && cur(s0).getAttribute('x2') === vx && cur(s0).getAttribute('visibility') === 'visible', 'cursor line at t, shown');
  assert(s0.querySelector('.mc-los-readout').innerHTML === '<b>1.36 km</b>', 'readout gets the html');
  assert(cur(s1).getAttribute('visibility') === 'hidden' && s1.querySelector('.mc-los-readout').textContent === 'hint', 'only the scope given changes');
  t.P.showChartCursor(s1, 0, 'x');
  t.P.hideChartCursor(root, 'Move along a profile');
  assert(root.querySelectorAll('.mc-los-cursor').every(c => c.getAttribute('visibility') === 'hidden'), 'hide: every cursor in scope hidden');
  assert(root.querySelectorAll('.mc-los-readout').every(r => r.textContent === 'Move along a profile'), 'hide: every readout back to the hint');
}

console.log('\n=== map pieces ===');
{
  const t = load();
  const line = t.P.legLine({ lat: 1, lon: 2 }, { lat: 3, lon: 4 });
  assert(JSON.stringify(line.lls) === '[[1,2],[3,4]]' && line.opts.className === 'mc-los-leg-active' && line.opts.dashArray === '10 8' && line.opts.interactive === false,
    'legLine: dashed, non-interactive mc-los-leg-active');

  const layer = makeLayer();
  const mk = t.P.placeCursorMarker(null, layer, { lat: 5, lon: 6 });
  assert(mk.opts.icon.className === 'mc-los-cursor-marker' && mk.opts.interactive === false && mk.opts.zIndexOffset === 1000 && layer.hasLayer(mk), 'cursor marker made and put on the layer');
  layer.removeLayer(mk);
  const again = t.P.placeCursorMarker(mk, layer, { lat: 7, lon: 8 });
  assert(again === mk && JSON.stringify(mk.ll) === '[7,8]' && layer.hasLayer(mk), 'the same marker moves and goes back on the layer');
  assert(t.P.placeCursorMarker(null, null, { lat: 0, lon: 0 }), 'no layer yet: still returns a marker');

  const clicks = [];
  const om = t.P.obstructionMarker({ lat: 1, lon: 2 }, 'warn', 'Terrain <b>40 m</b> above', { tipClass: 'mc-pl-tip', onClick: e => clicks.push(e) });
  assert(om.opts.icon.className === 'mc-los-obstruction mc-los-tone-warn' && /#ph-warning"/.test(om.opts.icon.html) &&
    om.opts.icon.iconSize.join() === '28,28' && om.opts.keyboard === true && om.opts.zIndexOffset === 400, 'obstruction marker: warning glyph in the tone');
  assert(om.tip.text === 'Terrain &lt;b&gt;40 m&lt;/b&gt; above' && om.tip.opts.className === 'mc-pl-tip' && om.tip.opts.direction === 'top', 'tooltip escaped, tip class passed');
  om.fire('click', 'ev');
  assert(clicks[0] === 'ev', 'click handler gets the event');
  om.el = t.doc.createElement('div');
  om.fire('add');
  assert(om.el.getAttribute('aria-label') === 'Terrain <b>40 m</b> above. Show terrain profile', 'aria-label set each time it is added');
  const bare = t.P.obstructionMarker({ lat: 1, lon: 2 }, 'bad', 'x');
  assert(!('className' in bare.tip.opts) && !bare.handlers.click, 'no tip class or click handler unless given');
}

console.log('\n=== no copies left in route-los.js / planner.js ===');
{
  const files = ['los.js', 'los-panel.js', 'route-los.js', 'planner.js'];
  const src = Object.fromEntries(files.map(f => [f, read(f)]));
  const escDefs = files.filter(f => /function esc\(/.test(src[f]));
  assert(escDefs.join() === 'los.js', 'exactly one esc implementation, in los.js (got ' + escDefs.join() + ')');
  ['route-los.js', 'planner.js'].forEach(f => {
    const s = src[f];
    const copies = ['function fmtKm', 'function fmtM', 'function cacheGet', 'function cachePut', '.keys().next().value', 'function raf', 'function caf',
      'function isMobile', 'function reducedMotion', 'mc-los-cursor-marker', 'mc-los-leg-active', 'mc-los-chart-wait', 'getBoundingClientRect',
      'class="mc-los-head"', 'mc-los-obstruction'].filter(x => s.includes(x));
    assert(copies.length === 0, f + ': uses MeshLOSPanel, keeps no copy' + (copies.length ? ' (found ' + copies.join(', ') + ')' : ''));
    assert(/var UI = window\.MeshLOSPanel;/.test(s), f + ': takes window.MeshLOSPanel');
  });
  const idx = read('index.html');
  const at = f => idx.indexOf('src="' + f + '?');
  assert(at('los.js') > 0 && at('los.js') < at('los-panel.js') && at('los-panel.js') < at('route-los.js') && at('los-panel.js') < at('planner.js'),
    'index.html loads los.js, then los-panel.js, before route-los.js and planner.js');
}

console.log('\n' + passed + ' passed, ' + failed + ' failed');
process.exit(failed ? 1 : 0);
