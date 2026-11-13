/* Dockerfile build stages must copy every internal module their binary needs.
 *
 * Each `WORKDIR /build/<cmd>` stage copies an explicit list of internal/
 * modules before `go mod download`; the cmd's go.mod reaches them through
 * `replace … => ../../internal/<m>` directives (and those modules may need
 * others). A module missing from the list only fails at image build time —
 * which CI does late and locally nobody runs — so this checks the lists
 * against the transitive go.mod replaces instead.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

let pass = 0, fail = 0;
function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); }
  catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// internalDeps returns every internal/<m> module reached from goModDir's
// go.mod through relative replace directives, transitively.
function internalDeps(goModDir, seen = new Set()) {
  const mod = fs.readFileSync(path.join(goModDir, 'go.mod'), 'utf8');
  const re = /^replace\s+\S+\s+=>\s+(\.\.?\/\S+)/gm;
  let m;
  while ((m = re.exec(mod)) !== null) {
    const target = path.normalize(path.join(goModDir, m[1]));
    const parts = target.split(path.sep);
    const i = parts.lastIndexOf('internal');
    if (i < 0 || i !== parts.length - 2) continue;
    const name = parts[i + 1];
    if (seen.has(name)) continue;
    seen.add(name);
    internalDeps(target, seen);
  }
  return seen;
}

// dockerStages maps each /build/<cmd> stage to the internal modules it copies.
function dockerStages(dockerfile) {
  const stages = {};
  let cur = null;
  for (const line of dockerfile.split('\n')) {
    const w = line.match(/^WORKDIR\s+\/build\/(\S+)/);
    if (w) { cur = w[1]; stages[cur] = stages[cur] || new Set(); continue; }
    if (/^WORKDIR\s/.test(line)) { cur = null; continue; }
    const c = cur && line.match(/^COPY\s+internal\/([^/\s]+)\/?\s/);
    if (c) stages[cur].add(c[1]);
  }
  return stages;
}

const stages = dockerStages(fs.readFileSync('Dockerfile', 'utf8'));

for (const cmd of ['server', 'ingestor', 'decrypt', 'migrate']) {
  test(`Dockerfile builds cmd/${cmd} in its own stage`, () => {
    assert.ok(stages[cmd], `no WORKDIR /build/${cmd} stage`);
  });
  if (!stages[cmd]) continue;
  test(`stage ${cmd} copies every internal module cmd/${cmd} needs`, () => {
    const need = internalDeps(path.join('cmd', cmd));
    const missing = [...need].filter((m) => !stages[cmd].has(m)).sort();
    assert.deepStrictEqual(missing, [], `missing COPY internal/<m>/ for: ${missing.join(', ')}`);
  });
}

test('the runtime image ships corescope-migrate', () => {
  const df = fs.readFileSync('Dockerfile', 'utf8');
  assert.match(df, /COPY --from=builder[^\n]*\/corescope-migrate/);
});

console.log(`\n${pass} passed, ${fail} failed`);
if (fail > 0) process.exit(1);
