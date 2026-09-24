/* test-issue-2042-recent-adverts-label.js
 *
 * Issue #2042: the node detail page had a section headed "Recent Packets" that
 * only ever listed adverts. A reader reasonably expects every packet type the
 * node originated.
 *
 * The section cannot show more than adverts, and that is the point rather than
 * an oversight. Measured on a production database: of 1,251,967 transmissions,
 * 191,237 carry a `from_pubkey`, and ALL 191,237 are payload_type 4 (ADVERT).
 * Not one non-advert is attributed to an originating node, because the ingestor
 * only fills that column for adverts. Working out who sent a relayed CHAN or
 * TXT packet is the path-resolution problem, not a filter this section could
 * apply. So the heading was the thing that was wrong.
 *
 * This pins the heading against its own data source. Both copies (full page and
 * side pane) read nodeData.recentAdverts, so a heading saying "Packets" is
 * contradicted by the field feeding it.
 */
'use strict';

const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const SRC = fs.readFileSync(path.join(REPO_ROOT, 'public/nodes.js'), 'utf8');

console.log('\n=== #2042: the node detail advert section is labelled honestly ===');

test('both copies of the section are headed "Recent Adverts"', () => {
  const n = (SRC.match(/<h4[^>]*>Recent Adverts \(/g) || []).length;
  // Two: the full node page and the side pane. If this becomes one, a copy was
  // removed or renamed and the other may have drifted back.
  assert.strictEqual(n, 2, 'expected 2 headings, found ' + n);
});

test('no copy still claims to list packets', () => {
  assert.ok(!/>Recent Packets/.test(SRC),
    'a "Recent Packets" heading is back; the section can only ever hold adverts');
});

test('the heading is fed by recentAdverts, which is what makes the label true', () => {
  // If a future change points this section at a different field, the label has
  // to be revisited with it. Asserting the source keeps the two tied together.
  const n = (SRC.match(/recentAdverts/g) || []).length;
  assert.ok(n >= 2, 'expected the advert field to feed both copies, found ' + n + ' references');
});

test('the heading explains why it is adverts only', () => {
  // The reporter asked either to widen the section or rename it. Renaming
  // without saying why invites the same report again, so the tooltip carries
  // the reason: from_pubkey is advert-only.
  const m = SRC.match(/<h4([^>]*)>Recent Adverts \(/);
  assert.ok(m, 'heading not found');
  assert.ok(/title="/.test(m[1]), 'heading carries no explanation: ' + m[1]);
  assert.ok(/from_pubkey/.test(m[1]), 'the explanation should name the column that limits it');
});

console.log(`\nTotal: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
