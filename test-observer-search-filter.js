/* test-observer-search-filter.js — behavioral tests for the observer dropdown
 * search box (#1884). Exercises the actual applyObserverSearchFilter logic,
 * not source-grep tautology.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-observer-search-filter.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

/**
 * Extract the applyObserverSearchFilter function body from packets.js.
 */
function extractApplyObserverSearchFilter() {
  const src = fs.readFileSync(__dirname + '/public/packets.js', 'utf-8');
  const marker = 'function applyObserverSearchFilter()';
  const idx = src.indexOf(marker);
  assert(idx !== -1, 'applyObserverSearchFilter not found in packets.js');
  const fnStart = src.indexOf('{', idx);
  let depth = 0, fnEnd = -1;
  for (let i = fnStart; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { fnEnd = i; break; } }
  }
  assert(fnEnd > fnStart, 'could not find end of applyObserverSearchFilter');
  return src.substring(fnStart + 1, fnEnd);
}

/**
 * Build a minimal sandbox: an obsSearchInput with a settable value, and an
 * obsList whose querySelectorAll returns items with a data-obs-name dataset
 * and a style.display we can assert on.
 */
function makeItem(name) {
  return { dataset: { obsName: name }, style: { display: '' } };
}

function runFilter(term, names) {
  const items = names.map(makeItem);
  const obsSearchInput = { value: term };
  const obsList = {
    querySelectorAll: (sel) => {
      assert.strictEqual(sel, '.multi-select-item[data-obs-name]');
      return items;
    },
  };
  const body = extractApplyObserverSearchFilter();
  const fn = new Function('obsSearchInput', 'obsList', body);
  fn(obsSearchInput, obsList);
  return items;
}

const fnBody = extractApplyObserverSearchFilter();

test('both includes and startsWith are used (default substring, ^-anchored prefix)', () => {
  assert(fnBody.includes('.includes('), 'expected substring includes() in filter body');
  assert(fnBody.includes('startsWith'), 'expected startsWith in filter body for ^-anchored matching');
});

test('empty search term shows every item', () => {
  const items = runFilter('', ['on4xyz brussels', 'be1abc', 'be2def']);
  for (const it of items) assert.strictEqual(it.style.display, '', `expected visible: ${it.dataset.obsName}`);
});

test('default matching is substring, not prefix-only', () => {
  const items = runFilter('brussels', ['on4xyz brussels', 'be1abc']);
  assert.strictEqual(items[0].style.display, '', 'mid-string "brussels" should match by default (includes)');
  assert.strictEqual(items[1].style.display, 'none', 'be1abc should not match "brussels"');
});

test('default substring match still finds prefix matches too', () => {
  const items = runFilter('be1', ['be1abc', 'be2def', 'on4xyz brussels']);
  assert.strictEqual(items[0].style.display, '', 'be1abc should match "be1"');
  assert.strictEqual(items[1].style.display, 'none', 'be2def should not match "be1"');
  assert.strictEqual(items[2].style.display, 'none', 'on4xyz brussels should not match "be1"');
});

test('^-anchored term uses prefix-only matching', () => {
  const items = runFilter('^be', ['be1abc', 'on4xyz brussels']);
  assert.strictEqual(items[0].style.display, '', 'be1abc should match anchored prefix "^be"');
  assert.strictEqual(items[1].style.display, 'none', 'brussels contains "be" but not as a prefix, should not match "^be"');
});

test('bare ^ with no remaining term shows every item', () => {
  const items = runFilter('^', ['be1abc', 'on4xyz brussels']);
  for (const it of items) assert.strictEqual(it.style.display, '', `expected visible: ${it.dataset.obsName}`);
});

test('search term is trimmed before matching', () => {
  const items = runFilter('  be1  ', ['be1abc', 'be2def']);
  assert.strictEqual(items[0].style.display, '', 'be1abc should match trimmed term "be1"');
  assert.strictEqual(items[1].style.display, 'none', 'be2def should not match trimmed term "be1"');
});

test('matching is case-insensitive relative to the stored lowercase name', () => {
  // buildObserverMenu() stores data-obs-name already lowercased; the search
  // input itself is lowercased by applyObserverSearchFilter before matching.
  const items = runFilter('BE1', ['be1abc']);
  assert.strictEqual(items[0].style.display, '', 'uppercase search term should still match lowercase stored name');
});

test('^-anchored search is also case-insensitive', () => {
  const items = runFilter('^BE', ['be1abc', 'on4xyz brussels']);
  assert.strictEqual(items[0].style.display, '', 'uppercase anchored term should still match lowercase stored name');
  assert.strictEqual(items[1].style.display, 'none', 'brussels should not match anchored "^BE"');
});

// Summary
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
console.log('All tests passed ✅');
