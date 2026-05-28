/* test-issue-1461-mobile-page-actions.js — behavioral test for the mobile
 * page-actions wiring shipped in #1471. Loads mobile-page-actions.js into
 * a vm sandbox with a minimal DOM mock (no jsdom dep), then exercises the
 * public observable surfaces:
 *
 *  1. On mobile + /#/packets, hashchange synthesizes a "Filters ▾" button
 *     and a "⏸" button under .nav-left, and clicking them delegates to the
 *     real #filterToggleBtn / #pktPauseBtn elements.
 *  2. The bottom-nav More sheet receives 3 injected mirrors (Favorites /
 *     Search / Customize) when the sheet element is present.
 *  3. On desktop viewport, no buttons are injected.
 *  4. Idempotent: re-init does not duplicate mirrors.
 *  5. Route change off /packets clears the slot AND closes the detail sheet.
 *
 * No source-grep. All assertions are on real function side effects.
 */
'use strict';
const vm = require('vm');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); }
  catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// ── Minimal Element / Document shim — no jsdom dep ────────────────────────
function makeElement(tag) {
  const el = {
    tagName: String(tag || 'div').toUpperCase(),
    children: [],
    parentNode: null,
    id: '',
    className: '',
    type: '',
    title: '',
    textContent: '',
    _attrs: {},
    _listeners: {},
    _clickCount: 0,
  };
  el.style = { _cssText: '', get cssText() { return this._cssText; }, set cssText(v) { this._cssText = v; } };
  el.setAttribute = function (k, v) {
    this._attrs[k] = String(v);
    if (k === 'class') this.className = String(v);
    if (k === 'id') this.id = String(v);
  };
  el.getAttribute = function (k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; };
  el.appendChild = function (child) { child.parentNode = this; this.children.push(child); return child; };
  el.insertBefore = function (child, ref) {
    child.parentNode = this;
    const idx = this.children.indexOf(ref);
    if (idx < 0) this.children.push(child);
    else this.children.splice(idx, 0, child);
    return child;
  };
  el.removeChild = function (child) {
    const idx = this.children.indexOf(child);
    if (idx >= 0) this.children.splice(idx, 1);
    child.parentNode = null;
    return child;
  };
  el.addEventListener = function (type, fn) { (this._listeners[type] = this._listeners[type] || []).push(fn); };
  el.dispatchEvent = function (ev) { (this._listeners[ev.type] || []).forEach(fn => fn(ev)); };
  el.click = function () {
    this._clickCount++;
    this.dispatchEvent({ type: 'click', target: this, preventDefault() {}, stopPropagation() {} });
  };
  el.closest = function (sel) {
    let cur = this;
    while (cur) {
      if (_elementMatches(cur, sel)) return cur;
      cur = cur.parentNode;
    }
    return null;
  };
  el.querySelector = function (sel) { return _walk(this, sel, true)[0] || null; };
  el.querySelectorAll = function (sel) { return _walk(this, sel, false); };
  el.classList = {
    add(c) { const s = new Set(String(el.className).split(/\s+/).filter(Boolean)); s.add(c); el.className = Array.from(s).join(' '); },
    remove(c) { const s = new Set(String(el.className).split(/\s+/).filter(Boolean)); s.delete(c); el.className = Array.from(s).join(' '); },
    contains(c) { return new Set(String(el.className).split(/\s+/).filter(Boolean)).has(c); },
  };
  Object.defineProperty(el, 'innerHTML', {
    configurable: true,
    get() { return ''; },
    set(v) {
      if (v === '' || v == null) {
        el.children.forEach(c => { c.parentNode = null; });
        el.children = [];
      }
    },
  });
  return el;
}

function _elementMatches(el, sel) {
  if (!el || !sel) return false;
  sel = String(sel).trim();
  // Comma list: any selector matches
  if (sel.indexOf(',') >= 0) {
    return sel.split(',').some(s => _elementMatches(el, s.trim()));
  }
  // Descendant combinator
  if (sel.indexOf(' ') >= 0) {
    const parts = sel.split(/\s+/);
    const last = parts[parts.length - 1];
    if (!_elementMatches(el, last)) return false;
    let cur = el.parentNode;
    for (let i = parts.length - 2; i >= 0; i--) {
      let found = false;
      while (cur) {
        if (_elementMatches(cur, parts[i])) { found = true; cur = cur.parentNode; break; }
        cur = cur.parentNode;
      }
      if (!found) return false;
    }
    return true;
  }
  // ID
  if (sel.startsWith('#')) return el.id === sel.slice(1);
  // Attribute [data-X] or [data-X="v"]
  const attrMatch = sel.match(/^\[([a-zA-Z0-9-]+)(?:="([^"]+)")?\]$/);
  if (attrMatch) {
    const k = attrMatch[1], v = attrMatch[2];
    if (!Object.prototype.hasOwnProperty.call(el._attrs, k)) return false;
    return v == null ? true : el._attrs[k] === v;
  }
  // .class
  if (sel.startsWith('.')) {
    return el.classList && el.classList.contains(sel.slice(1));
  }
  // tag.class
  if (sel.indexOf('.') > 0) {
    const [tag, ...cls] = sel.split('.');
    if (el.tagName.toLowerCase() !== tag.toLowerCase()) return false;
    return cls.every(c => el.classList && el.classList.contains(c));
  }
  // Bare tag
  return el.tagName.toLowerCase() === sel.toLowerCase();
}

function _walk(root, sel, firstOnly) {
  const out = [];
  function visit(n) {
    if (n !== root && _elementMatches(n, sel)) {
      out.push(n);
      if (firstOnly) return true;
    }
    for (const c of (n.children || [])) {
      if (visit(c) && firstOnly) return true;
    }
    return false;
  }
  visit(root);
  return out;
}

function makeSandbox(opts) {
  opts = opts || {};
  const docElements = {};
  const docRoot = makeElement('html');
  const docBody = makeElement('body');
  docRoot.appendChild(docBody);
  const documentListeners = {};
  const windowListeners = {};

  const documentMock = {
    documentElement: docRoot,
    body: docBody,
    createElement(tag) { return makeElement(tag); },
    getElementById(id) {
      if (docElements[id]) return docElements[id];
      function find(n) {
        if (n.id === id) return n;
        for (const c of (n.children || [])) { const r = find(c); if (r) return r; }
        return null;
      }
      return find(docRoot);
    },
    querySelector(sel) { return docRoot.querySelector(sel) || docBody.querySelector(sel); },
    querySelectorAll(sel) { return docRoot.querySelectorAll(sel).concat(docBody.querySelectorAll(sel)); },
    addEventListener(type, fn) { (documentListeners[type] = documentListeners[type] || []).push(fn); },
    dispatchEvent(ev) { (documentListeners[ev.type] || []).forEach(fn => fn(ev)); return true; },
    readyState: 'complete',
  };

  const ctx = {
    console,
    setTimeout, clearTimeout,
    JSON, Date, Math, Object, Array, String, Number, Boolean, Set, Map,
    document: documentMock,
    window: {
      innerWidth: opts.innerWidth || 390,
      addEventListener(type, fn) { (windowListeners[type] = windowListeners[type] || []).push(fn); },
      dispatchEvent(ev) { (windowListeners[ev.type] || []).forEach(fn => fn(ev)); return true; },
    },
    location: { hash: opts.hash || '#/packets' },
  };
  ctx.globalThis = ctx;
  ctx.window.document = documentMock;
  ctx.window.location = ctx.location;
  vm.createContext(ctx);
  ctx._docRoot = docRoot;
  ctx._docBody = docBody;
  ctx._documentListeners = documentListeners;
  ctx._windowListeners = windowListeners;
  ctx._registerElement = (el) => { if (el.id) docElements[el.id] = el; };
  return ctx;
}

function loadMPA(ctx) {
  const src = fs.readFileSync(path.join(__dirname, 'public', 'mobile-page-actions.js'), 'utf8');
  vm.runInContext(src, ctx);
}

function buildMobileDOM(ctx, opts) {
  opts = opts || {};
  const body = ctx._docBody;

  const nav = makeElement('div');
  nav.className = 'nav-left';
  body.appendChild(nav);

  const realFilter = makeElement('button');
  realFilter.id = 'filterToggleBtn';
  realFilter.setAttribute('class', 'filter-toggle-btn');
  body.appendChild(realFilter);
  ctx._registerElement(realFilter);

  const realPause = makeElement('button');
  realPause.id = 'pktPauseBtn';
  body.appendChild(realPause);
  ctx._registerElement(realPause);

  if (opts.bottomNav !== false) {
    const sheet = makeElement('div');
    sheet.setAttribute('data-bottom-nav-sheet', '');
    body.appendChild(sheet);

    ['favToggle', 'searchToggle', 'customizeToggle'].forEach(id => {
      const btn = makeElement('button');
      btn.id = id;
      body.appendChild(btn);
      ctx._registerElement(btn);
    });
    const sep = makeElement('div');
    sep.setAttribute('class', 'bottom-nav-sheet-sep');
    sheet.appendChild(sep);
  }

  const detailSheet = makeElement('div');
  detailSheet.id = 'mobileDetailSheet';
  detailSheet.classList.add('open');
  body.appendChild(detailSheet);
  ctx._registerElement(detailSheet);

  return { nav, realFilter, realPause };
}

console.log('── #1461 mobile-page-actions behavioral tests ──');

test('mobile + /#/packets: hashchange injects ⏸ + Filters ▾ buttons under .nav-left', () => {
  const ctx = makeSandbox({ innerWidth: 390, hash: '#/packets' });
  const { nav, realFilter, realPause } = buildMobileDOM(ctx);
  loadMPA(ctx);
  (ctx._windowListeners.hashchange || []).forEach(fn => fn({ type: 'hashchange' }));

  const slot = ctx.document.getElementById('navPageActions');
  assert.ok(slot, 'navPageActions slot was created');
  assert.strictEqual(slot.parentNode, nav, 'slot is appended under .nav-left');

  const btnTexts = slot.children.map(c => c.textContent);
  assert.ok(btnTexts.includes('⏸'), 'pause button (⏸) injected — got: ' + JSON.stringify(btnTexts));
  assert.ok(btnTexts.some(t => /Filters/.test(t)), 'Filters button injected — got: ' + JSON.stringify(btnTexts));

  const pauseBtn = slot.children.find(c => c.textContent === '⏸');
  pauseBtn.click();
  assert.strictEqual(realPause._clickCount, 1, 'pause-mirror delegates click to #pktPauseBtn');

  const filtBtn = slot.children.find(c => /Filters/.test(c.textContent));
  filtBtn.click();
  assert.strictEqual(realFilter._clickCount, 1, 'Filters-mirror delegates click to #filterToggleBtn');
});

test('mobile: 3 More-sheet mirrors (Favorites/Search/Customize) injected', () => {
  const ctx = makeSandbox({ innerWidth: 390, hash: '#/packets' });
  buildMobileDOM(ctx);
  loadMPA(ctx);

  const sheet = ctx.document.querySelector('[data-bottom-nav-sheet]');
  assert.ok(sheet, 'sheet found');
  const mirrors = sheet.querySelectorAll('[data-mpa-mirror]');
  assert.strictEqual(mirrors.length, 3, 'exactly 3 mirrors injected — got ' + mirrors.length);
  const ids = mirrors.map(m => m.getAttribute('data-mpa-mirror')).sort();
  assert.deepStrictEqual(ids, ['customizeToggle', 'favToggle', 'searchToggle']);

  const favReal = ctx.document.getElementById('favToggle');
  const favMirror = mirrors.find(m => m.getAttribute('data-mpa-mirror') === 'favToggle');
  favMirror.click();
  assert.strictEqual(favReal._clickCount, 1, 'Favorites mirror delegates click to #favToggle');
});

test('mobile: idempotent — re-init does not duplicate More-sheet mirrors', () => {
  const ctx = makeSandbox({ innerWidth: 390, hash: '#/packets' });
  buildMobileDOM(ctx);
  loadMPA(ctx);
  (ctx._windowListeners.hashchange || []).forEach(fn => fn({ type: 'hashchange' }));
  const sheet = ctx.document.querySelector('[data-bottom-nav-sheet]');
  const mirrors = sheet.querySelectorAll('[data-mpa-mirror]');
  assert.strictEqual(mirrors.length, 3, 'still 3 mirrors after re-init — got ' + mirrors.length);
});

test('desktop viewport (innerWidth=1280): slot stays empty', () => {
  const ctx = makeSandbox({ innerWidth: 1280, hash: '#/packets' });
  buildMobileDOM(ctx);
  loadMPA(ctx);
  (ctx._windowListeners.hashchange || []).forEach(fn => fn({ type: 'hashchange' }));
  const slot = ctx.document.getElementById('navPageActions');
  if (slot) {
    assert.strictEqual(slot.children.length, 0, 'desktop slot empty — got ' + slot.children.length);
  }
});

test('mobile + non-packets route: hashchange clears the slot', () => {
  const ctx = makeSandbox({ innerWidth: 390, hash: '#/packets' });
  buildMobileDOM(ctx);
  loadMPA(ctx);
  ctx.location.hash = '#/map';
  (ctx._windowListeners.hashchange || []).forEach(fn => fn({ type: 'hashchange' }));
  const slot = ctx.document.getElementById('navPageActions');
  if (slot) {
    assert.strictEqual(slot.children.length, 0, 'slot cleared off /packets — got ' + slot.children.length);
  }
});

test('mobile detail sheet closes on route change away from /packets', () => {
  const ctx = makeSandbox({ innerWidth: 390, hash: '#/packets' });
  buildMobileDOM(ctx);
  const sheet = ctx.document.getElementById('mobileDetailSheet');
  assert.ok(sheet.classList.contains('open'), 'pre: sheet open');
  loadMPA(ctx);
  ctx.location.hash = '#/map';
  (ctx._windowListeners.hashchange || []).forEach(fn => fn({ type: 'hashchange' }));
  assert.ok(!sheet.classList.contains('open'), 'sheet closed after route change off /packets');
});

console.log(`\n#1461 mobile-page-actions: ${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
