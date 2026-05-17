/* Unit tests for public/page-state.js string builders (tested via VM sandbox). */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// Load page-state.js into a sandbox with a minimal window.
const sandbox = { window: {} };
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync('public/page-state.js', 'utf8'), sandbox);
const PS = sandbox.window.PageState;

test('PageState is exported', () => {
  assert.ok(PS, 'window.PageState should be defined');
  ['loading', 'empty', 'skeleton', 'errorText', 'error', 'row'].forEach((k) => {
    assert.strictEqual(typeof PS[k], 'function', k + ' should be a function');
  });
});

test('loading() includes the message and a status role', () => {
  const html = PS.loading('Loading nodes');
  assert.ok(html.includes('Loading nodes'));
  assert.ok(html.includes('role="status"'));
  assert.ok(html.includes('ps-spinner'));
});

test('loading() defaults the message', () => {
  assert.ok(PS.loading().includes('Loading'));
});

test('empty() renders title, icon, and hint', () => {
  const html = PS.empty({ icon: '📦', title: 'No packets', hint: 'Try later' });
  assert.ok(html.includes('No packets'));
  assert.ok(html.includes('Try later'));
  assert.ok(html.includes('ps-icon'));
});

test('errorText() uses role=alert and shows the message', () => {
  const html = PS.errorText('Boom');
  assert.ok(html.includes('role="alert"'));
  assert.ok(html.includes('Boom'));
});

test('builders escape HTML to prevent injection', () => {
  const html = PS.empty({ title: '<img src=x onerror=alert(1)>' });
  assert.ok(!html.includes('<img'), 'raw tag must not appear');
  assert.ok(html.includes('&lt;img'));
});

test('skeleton() non-table emits div rows', () => {
  const html = PS.skeleton({ rows: 3, cols: 2 });
  assert.strictEqual((html.match(/ps-skeleton-row/g) || []).length, 3);
  assert.strictEqual((html.match(/ps-skeleton-cell/g) || []).length, 6);
  assert.ok(!html.includes('<tr'));
});

test('skeleton({table:true}) emits tr/td rows', () => {
  const html = PS.skeleton({ rows: 2, cols: 3, table: true });
  assert.strictEqual((html.match(/<tr/g) || []).length, 2);
  assert.strictEqual((html.match(/<td>/g) || []).length, 6);
});

test('row() wraps content in a full-width table row', () => {
  assert.strictEqual(PS.row(4, 'X'), '<tr><td colspan="4">X</td></tr>');
});

test('error() renders into a container and shows the message', () => {
  let html = '';
  const container = {
    set innerHTML(v) { html = v; },
    get innerHTML() { return html; },
    querySelector() { return null; }
  };
  PS.error(container, new Error('fetch failed'));
  assert.ok(html.includes('fetch failed'));
  assert.ok(html.includes('role="alert"'));
  assert.ok(!html.includes('ps-retry'), 'no retry button without onRetry');
});

test('error() renders a retry button and wires the handler', () => {
  let html = '', clicked = 0, clickHandler = null;
  const btn = { addEventListener(ev, fn) { if (ev === 'click') clickHandler = fn; } };
  const container = {
    set innerHTML(v) { html = v; },
    get innerHTML() { return html; },
    querySelector(sel) { return sel === '.ps-retry' ? btn : null; }
  };
  PS.error(container, 'down', () => { clicked++; });
  assert.ok(html.includes('ps-retry'));
  assert.ok(typeof clickHandler === 'function', 'click handler should be wired');
  clickHandler();
  assert.strictEqual(clicked, 1);
});

test('compact:true adds ps-compact to builders; omitting it does not', () => {
  assert.ok(PS.loading('x', { compact: true }).includes('ps-compact'));
  assert.ok(!PS.loading('x').includes('ps-compact'));
  assert.ok(PS.empty({ title: 'x', compact: true }).includes('ps-compact'));
  assert.ok(!PS.empty({ title: 'x' }).includes('ps-compact'));
  assert.ok(PS.errorText('x', { compact: true }).includes('ps-compact'));
  assert.ok(!PS.errorText('x').includes('ps-compact'));
});

test('error() with compact:true renders ps-compact on the .ps div', () => {
  let html = '';
  const container = {
    set innerHTML(v) { html = v; },
    get innerHTML() { return html; },
    querySelector() { return null; }
  };
  PS.error(container, new Error('boom'), null, { compact: true });
  assert.ok(html.includes('class="ps ps-error ps-compact"'));
  PS.error(container, new Error('boom'));
  assert.ok(!html.includes('ps-compact'));
});


console.log(`\n  ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
