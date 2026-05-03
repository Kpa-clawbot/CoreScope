/* Unit tests for geofilter-builder draft save/load + download config */
'use strict';
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// --- Mock localStorage ---
function makeStorage() {
  const store = {};
  return {
    getItem(k) { return store[k] || null; },
    setItem(k, v) { store[k] = String(v); },
    removeItem(k) { delete store[k]; },
    _store: store
  };
}

// --- Mock DOM helpers ---
function makeDoc() {
  const els = {};
  const listeners = {};
  return {
    getElementById(id) {
      if (!els[id]) els[id] = { value: '', textContent: '', classList: { add(){}, remove(){} }, style: {}, click() { (listeners[id] || []).forEach(fn => fn()); } };
      return els[id];
    },
    createElement(tag) {
      const el = { setAttribute(){}, click(){}, style: {}, href: '', download: '' };
      return el;
    },
    body: { appendChild(el) {}, removeChild(el) {} },
    _els: els,
    _on(id, fn) { (listeners[id] = listeners[id] || []).push(fn); }
  };
}

// --- Tests for the draft module (public/geofilter-draft.js) ---
// The module should export: saveDraft, loadDraft, clearDraft, buildConfigSnippet

const fs = require('fs');
const vm = require('vm');
const path = require('path');

function loadModule(localStorage, document) {
  const code = fs.readFileSync(path.join(__dirname, 'public', 'geofilter-draft.js'), 'utf8');
  const sandbox = { localStorage, document, URL: { createObjectURL() { return 'blob:mock'; }, revokeObjectURL() {} }, Blob: class { constructor(parts, opts) { this.parts = parts; this.opts = opts; } } };
  vm.runInNewContext(code, sandbox);
  return sandbox;
}

console.log('geofilter-draft tests:');

test('saveDraft stores polygon + bufferKm to localStorage', () => {
  const ls = makeStorage();
  const doc = makeDoc();
  const ctx = loadModule(ls, doc);
  const polygon = [[50.1, 4.2], [50.3, 4.5], [49.9, 4.8]];
  ctx.GeofilterDraft.saveDraft(polygon, 20);
  const stored = JSON.parse(ls.getItem('geofilter-draft'));
  assert.deepStrictEqual(stored.polygon, polygon);
  assert.strictEqual(stored.bufferKm, 20);
});

test('loadDraft returns null when nothing saved', () => {
  const ls = makeStorage();
  const doc = makeDoc();
  const ctx = loadModule(ls, doc);
  assert.strictEqual(ctx.GeofilterDraft.loadDraft(), null);
});

test('loadDraft returns saved draft', () => {
  const ls = makeStorage();
  ls.setItem('geofilter-draft', JSON.stringify({ polygon: [[1,2],[3,4],[5,6]], bufferKm: 10 }));
  const doc = makeDoc();
  const ctx = loadModule(ls, doc);
  const draft = ctx.GeofilterDraft.loadDraft();
  assert.deepStrictEqual(draft.polygon, [[1,2],[3,4],[5,6]]);
  assert.strictEqual(draft.bufferKm, 10);
});

test('clearDraft removes from localStorage', () => {
  const ls = makeStorage();
  ls.setItem('geofilter-draft', '{}');
  const doc = makeDoc();
  const ctx = loadModule(ls, doc);
  ctx.GeofilterDraft.clearDraft();
  assert.strictEqual(ls.getItem('geofilter-draft'), null);
});

test('buildConfigSnippet returns correct JSON structure', () => {
  const ls = makeStorage();
  const doc = makeDoc();
  const ctx = loadModule(ls, doc);
  const polygon = [[50.1, 4.2], [50.3, 4.5], [49.9, 4.8]];
  const snippet = ctx.GeofilterDraft.buildConfigSnippet(polygon, 15);
  const parsed = JSON.parse(snippet);
  assert.deepStrictEqual(parsed, { geo_filter: { bufferKm: 15, polygon: polygon } });
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
