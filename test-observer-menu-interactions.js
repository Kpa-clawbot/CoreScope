/* test-observer-menu-interactions.js — behavioral tests for the observer
 * dropdown's event handlers (#1884 follow-up review). Exercises the actual
 * handler bodies extracted from packets.js, not source-grep tautology.
 *
 * Covers three interaction bugs found after the matcher itself (tested in
 * test-observer-search-filter.js) was fixed:
 *  1. obsSearchInput's own change/blur events bubble into obsMenu's change
 *     handler and must not be mistaken for a checkbox toggle.
 *  2. Clear Filters must reset the search box and re-apply the filter.
 *  3. The dropdown must not autofocus the search box on touch devices.
 */
'use strict';

const fs = require('fs');
const assert = require('assert');

console.log('--- test-observer-menu-interactions.js ---');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

const SRC = fs.readFileSync(__dirname + '/public/packets.js', 'utf-8');

/**
 * Extract the source between a marker ending in "{" and its matching "}".
 */
function extractBlock(marker) {
  const markerIdx = SRC.indexOf(marker);
  assert(markerIdx !== -1, `marker not found: ${marker}`);
  const fnStart = markerIdx + marker.length - 1;
  assert(SRC[fnStart] === '{', `marker must end just before "{": ${marker}`);
  let depth = 0, fnEnd = -1;
  for (let i = fnStart; i < SRC.length; i++) {
    if (SRC[i] === '{') depth++;
    else if (SRC[i] === '}') { depth--; if (depth === 0) { fnEnd = i; break; } }
  }
  assert(fnEnd > fnStart, `could not find end of block: ${marker}`);
  return SRC.substring(fnStart + 1, fnEnd);
}

function stubEl(overrides) {
  return Object.assign({
    value: '',
    textContent: '',
    style: {},
    classList: { add() {}, remove() {}, contains() { return false; }, toggle() {} },
    querySelectorAll: () => [],
  }, overrides);
}

// --- 1. obsMenu change handler must ignore events with no data-obs-id ---
// (the search input's own change/blur events bubble up to this listener)
{
  const body = extractBlock("obsMenu.addEventListener('change', (e) => {");

  function run(target, { calls, selectedObservers, filters }) {
    const localStorage = { setItem: () => calls.push('setItem'), removeItem: () => calls.push('removeItem') };
    const buildObserverMenu = () => calls.push('buildObserverMenu');
    const updateObsTrigger = () => calls.push('updateObsTrigger');
    const updatePacketsUrl = () => calls.push('updatePacketsUrl');
    const renderTableRows = () => calls.push('renderTableRows');
    const fn = new Function(
      'e', 'selectedObservers', 'filters', 'localStorage',
      'buildObserverMenu', 'updateObsTrigger', 'updatePacketsUrl', 'renderTableRows',
      body
    );
    fn(target, selectedObservers, filters, localStorage, buildObserverMenu, updateObsTrigger, updatePacketsUrl, renderTableRows);
  }

  test('change event with no data-obs-id (bubbled from search input) is a no-op', () => {
    const calls = [];
    const selectedObservers = new Set(['3']);
    run({ target: { dataset: {}, checked: false } }, { calls, selectedObservers, filters: {} });
    assert.deepStrictEqual(calls, [], 'no side effects should run for an id-less change event');
    assert.deepStrictEqual([...selectedObservers], ['3'], 'selection must be untouched');
  });

  test('change event on an actual checkbox still updates the selection', () => {
    const calls = [];
    const selectedObservers = new Set();
    const filters = {};
    run({ target: { dataset: { obsId: '7' }, checked: true } }, { calls, selectedObservers, filters });
    assert.deepStrictEqual([...selectedObservers], ['7'], 'checking observer 7 should select it');
    assert(calls.includes('buildObserverMenu'), 'buildObserverMenu should run for a real toggle');
    assert(calls.includes('updatePacketsUrl'), 'updatePacketsUrl should run for a real toggle');
  });
}

// --- 2. Clear Filters must reset and re-apply the observer search box ---
{
  const body = extractBlock("if (clearBtn) clearBtn.addEventListener('click', function() {");

  test('Clear Filters resets the search box and re-applies the filter', () => {
    const applyCalls = [];
    const obsSearchInput = stubEl({ value: 'brussels' });
    const filters = {};
    const localStorage = { removeItem() {}, setItem() {} };
    const RegionFilter = { setSelected() {} };
    const loadPackets = () => {};
    const updatePacketsUrl = () => {};
    const applyObserverSearchFilter = () => applyCalls.push('applied');
    const documentStub = { getElementById: () => stubEl({}) };

    const fn = new Function(
      'filters', 'localStorage', 'document', 'obsSearchInput', 'applyObserverSearchFilter',
      'savedTimeWindowMin', 'DEFAULT_TIME_WINDOW', 'RegionFilter', 'updatePacketsUrl', 'loadPackets',
      '_observerFilterSet',
      body
    );
    fn(filters, localStorage, documentStub, obsSearchInput, applyObserverSearchFilter,
      15, 15, RegionFilter, updatePacketsUrl, loadPackets, null);

    assert.strictEqual(obsSearchInput.value, '', 'search box should be cleared');
    assert.deepStrictEqual(applyCalls, ['applied'], 'the filter should be re-applied after clearing');
  });
}

// --- 3. Autofocus must be gated on touch (pointer: coarse) devices ---
{
  const body = extractBlock("obsTrigger.addEventListener('click', (e) => {");

  function run(isTouch) {
    const focusCalls = [];
    const obsSearchInput = stubEl({ focus: () => focusCalls.push('focus') });
    const obsMenu = { classList: { toggle() {}, contains: () => true } };
    const typeMenu = { classList: { remove() {} } };
    const window_ = { matchMedia: () => ({ matches: isTouch }) };
    const fn = new Function('e', 'obsMenu', 'typeMenu', 'obsSearchInput', 'window', body);
    fn({ stopPropagation() {} }, obsMenu, typeMenu, obsSearchInput, window_);
    return focusCalls;
  }

  test('touch devices (pointer: coarse) do not get the search box autofocused', () => {
    assert.deepStrictEqual(run(true), [], 'focus() should not be called on touch devices');
  });

  test('non-touch devices still get the search box autofocused', () => {
    assert.deepStrictEqual(run(false), ['focus'], 'focus() should still be called on desktop/mouse');
  });
}

// Summary
console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
console.log('All tests passed ✅');
