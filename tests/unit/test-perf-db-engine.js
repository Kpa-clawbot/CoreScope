/* perfDbRows: engine-aware database panel (SQLite vs PostgreSQL). */
'use strict';
const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

const ctx = {
  window: {},
  document: { addEventListener() {}, getElementById() { return null; }, hidden: true },
  console,
  fetch: () => Promise.resolve({ json: () => Promise.resolve(null) }),
  setInterval: () => 0, clearInterval: () => {}, registerPage: () => {},
};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync('public/perf.js', 'utf8'), ctx);
const perfDbRows = ctx.window.perfDbRows;

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); }
  catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}
const labels = (o) => o.rows.map((r) => r.label);
const rows = { transmissions: 12345, observations: 99, nodes: 7, observers: 3 };

test('sqlite stats keep WAL, freelist and busy pages', () => {
  const o = perfDbRows({ engine: 'sqlite', dbSizeMB: 10, walSizeMB: 60, freelistMB: 2, walPages: { busy: 4 }, rows });
  assert.strictEqual(o.title, 'Database (SQLite)');
  assert.deepStrictEqual(Array.from(labels(o)), ['DB Size', 'WAL Size', 'Freelist', 'Transmissions', 'Observations', 'Nodes', 'Observers', 'WAL Busy Pages']);
  assert.strictEqual(o.rows[1].color, 'var(--status-red)');
  assert.strictEqual(o.rows[2].color, 'var(--status-green)');
  assert.strictEqual(o.rows[1].text, '60MB');
});

test('postgres stats hide WAL/freelist/pages and label estimates', () => {
  const o = perfDbRows({ engine: 'postgres', dbSizeMB: 10, walSizeMB: 0, freelistMB: 0, walPages: 0, rows });
  assert.strictEqual(o.engine, 'postgres');
  assert.strictEqual(o.title, 'Database (PostgreSQL)');
  assert.ok(!labels(o).some((l) => /WAL|Freelist/.test(l)));
  assert.strictEqual(labels(o).length, 5);
  assert.ok(labels(o).slice(1).every((l) => l.includes('≈ rows (estimate)') || l.includes('≈ rows, estimate')));
});

test('postgres ignores a stray walPages object', () => {
  const o = perfDbRows({ engine: 'postgres', dbSizeMB: 1, walPages: { busy: 1 }, rows });
  assert.ok(!labels(o).includes('WAL Busy Pages'));
});

test('missing engine means sqlite', () => {
  const o = perfDbRows({ dbSizeMB: 1, walSizeMB: 0, freelistMB: 0, rows });
  assert.strictEqual(o.engine, 'sqlite');
  assert.ok(labels(o).includes('WAL Size'));
  assert.ok(!labels(o).includes('WAL Busy Pages'));
});

console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
