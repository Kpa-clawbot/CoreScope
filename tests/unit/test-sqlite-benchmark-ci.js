// Exercise the actual CI request parser and shell guard; no network or benchmarks.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const { spawnSync } = require('node:child_process');
const root = path.resolve(__dirname, '../..');
const read = name => {
  const file = path.join(root, '.github/workflows', name);
  return fs.existsSync(file) ? fs.readFileSync(file, 'utf8').replace(/\r/g, '') : '';
};
const deploy = read('deploy.yml');
const workflow = read('sqlite-benchmark.yml');
function block(source, key, indent) {
  const lines = source.split('\n');
  const start = lines.findIndex(line => line.startsWith(' '.repeat(indent) + key + ':'));
  if (start < 0) return '';
  let end = start + 1;
  while (end < lines.length && (!lines[end].trim() || lines[end].search(/\S/) > indent)) end++;
  return lines.slice(start, end).join('\n');
}
function value(source, key, indent) {
  const lines = block(source, key, indent).split('\n');
  if (!lines[0]) return '';
  const first = lines[0].slice(indent + key.length + 1).trim();
  return ['|', '>'].includes(first) ? lines.slice(1).map(line => line.slice(indent + 2)).join('\n') : first;
}
const step = (name, source = workflow) => source.split(/(?=^      - name:)/m).find(part => value(part, '- name', 6) === name) || '';
const evaluate = (expression, context) => vm.runInNewContext(expression.replace(/\$\{\{|\}\}/g, '').trim(), {
  ...context, startsWith: (text, prefix) => text.startsWith(prefix), cancelled: () => false,
});
const needs = Object.fromEntries(['go-test', 'e2e-test', 'image-check', 'build-and-publish'].map(name => [name, { result: 'success' }]));
needs.changes = { outputs: { code: 'true', ingestor: 'true' }, result: 'success' };
for (const ref of ['refs/heads/master', 'refs/tags/v9.8.7']) {
  const context = { github: { event_name: 'workflow_dispatch', ref }, inputs: { sqlite_benchmark: true, images_published: false }, needs, vars: { ENABLE_STAGING_DEPLOY: 'true' } };
  for (const job of ['build-and-publish', 'release-artifacts', 'deploy', 'publish']) {
    assert.equal(Boolean(evaluate(value(block(deploy, job, 2), 'if', 4), context)), false,
      `benchmark-only dispatch must never enter ${job} on ${ref}`);
  }
}
console.log('PASS benchmark-only dispatch cannot publish or deploy even when normal gates are green');
assert(workflow, 'SQLite benchmark workflow is missing');
assert.equal(value(block(workflow, 'permissions', 0), 'contents', 2), 'read');
assert(!/^\s*(secrets:|environment:|packages:|id-token:)/m.test(workflow), 'benchmark gains secrets or write permissions');
assert(!block(deploy, 'pull_request', 2).includes('edited'), 'body edits must not overwrite ordinary required CI checks');
const caller = block(deploy, 'sqlite-benchmark', 2);
assert.equal(value(caller, 'uses', 4), './.github/workflows/sqlite-benchmark.yml');
assert.equal(value(block(caller, 'permissions', 4), 'contents', 6), 'read');
assert(!caller.includes('secrets:'), 'dispatcher must not inherit secrets');
assert(!block(deploy, 'permissions', 0).includes('write'), 'benchmark dispatcher inherits write permissions');
assert(block(deploy, 'concurrency', 0).includes('inputs.sqlite_benchmark'), 'benchmark-only dispatch can block normal CI');

const parser = value(step('Parse benchmark request'), 'run', 8).split("python3 - <<'PY'\n")[1]?.split('\nPY')[0];
assert(parser, 'missing executable Python request parser');
assert(parser.includes('GITHUB_EVENT_PATH'), 'PR body must be parsed from the raw event, not inserted into shell source');
assert(!parser.includes('${{'), 'request parser must not interpolate untrusted expressions');
const A = 'a'.repeat(40), B = 'b'.repeat(40), H = 'c'.repeat(40);
const baseline = '9dbc287579a237ffa744dd0c91fa7227d09763ac';
const checked = '- [x] Run SQLite benchmark (primary)';
const body = `${checked}\nsqlite-baseline-sha: ${A}\nsqlite-candidate-sha: ${B}\nsqlite-harness-sha: ${H}`;
function request(text = '', { action = 'synchronize', previous, changes, event = 'pull_request', inputs = {} } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-ci-request-'));
  const eventFile = path.join(dir, 'event.json'), output = path.join(dir, 'output');
  fs.writeFileSync(eventFile, JSON.stringify({ action, pull_request: { body: text },
    changes: changes || (previous === undefined ? {} : { body: { from: previous } }) }));
  fs.writeFileSync(output, '');
  try {
    const result = spawnSync(process.platform === 'win32' ? 'python' : 'python3', ['-c', parser], {
      encoding: 'utf8', env: { ...process.env, GITHUB_EVENT_PATH: eventFile, GITHUB_OUTPUT: output,
        EVENT_NAME: event, EVENT_SHA: H, SOURCE_REPOSITORY: 'contributor/project',
        INPUT_PROFILE: inputs.profile || '', INPUT_BASELINE: inputs.baseline || '',
        INPUT_CANDIDATE: inputs.candidate || '', INPUT_HARNESS: inputs.harness || '' },
    });
    return { status: result.status, error: result.stderr, values: Object.fromEntries(fs.readFileSync(output, 'utf8').trim().split(/\r?\n/).filter(Boolean).map(line => {
      const at = line.indexOf('='); return [line.slice(0, at), line.slice(at + 1)];
    })) };
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
}
const smoke = request();
assert.equal(smoke.status, 0, smoke.error);
assert.deepEqual(smoke.values, { profile: 'smoke', baseline_sha: baseline, candidate_sha: H, harness_sha: H, repository: 'contributor/project' });
const primary = request(body);
assert.equal(primary.status, 0, primary.error);
assert.deepEqual(primary.values, { profile: 'primary', baseline_sha: A, candidate_sha: B, harness_sha: H, repository: 'contributor/project' });
assert.equal(request(body.replace('(primary)', '(diagnostic)')).values.profile, 'diagnostic');
for (const text of ['', '- [ ] Run SQLite benchmark (primary)', '```\n' + body + '\n```', '````\n```\n' + body + '\n````', '~~~text\n' + body + '\n~~~']) {
  assert.equal(request(text, { action: 'edited', previous: body }).values.profile, 'off', 'unchecked/code-fenced body started a benchmark');
}
assert.equal(request(body + '\nUnrelated prose.', { action: 'edited', previous: body }).values.profile, 'off');
assert.equal(request(body, { action: 'edited', changes: { title: { from: 'old' } } }).values.profile, 'off');
assert.equal(request(body, { action: 'edited', previous: '' }).values.profile, 'primary');
assert.equal(request(body.replace(B, 'd'.repeat(40)), { action: 'edited', previous: body }).values.profile, 'primary');
assert.equal(request(body, { action: 'edited', previous: body.replace(B, 'master') }).values.profile, 'primary');
for (const bad of [body.replace(B, 'master'), body.replace(B, '$(' + 'echo unsafe)'), body + `\nsqlite-candidate-sha: ${A}`,
  body + '\n- [x] Run SQLite benchmark (diagnostic)']) {
  assert.notEqual(request(bad).status, 0, 'ambiguous or mutable request accepted');
}
for (const profile of ['smoke', 'primary', 'diagnostic']) {
  const result = request('', { event: 'workflow_dispatch', inputs: { profile, baseline: A, candidate: B, harness: H } });
  assert.equal(result.status, 0, result.error);
  assert.equal(result.values.profile, profile);
  assert.equal(result.values.candidate_sha, B);
}
assert.notEqual(request('', { event: 'workflow_dispatch', inputs: { profile: 'primary;echo unsafe' } }).status, 0);
console.log('PASS raw PR requests, edit gating, immutable independent refs, and explicit profiles');

const checkout = step('Checkout the exact harness');
assert.equal(value(checkout, 'persist-credentials', 10), 'false');
assert.equal(value(checkout, 'fetch-depth', 10), '0');
assert(value(checkout, 'ref', 10).includes('harness_sha'));
const guard = value(step('Verify exact refs and workflow source'), 'run', 8);
assert(guard.includes('WORKFLOW_SHA') && guard.includes('EVENT_SHA') && guard.includes('git diff --quiet'), 'executed workflow is not pinned');
let bash = process.env.BASH_PATH || 'bash';
if (process.platform === 'win32' && !process.env.BASH_PATH) {
  const git = spawnSync('git', ['--exec-path'], { encoding: 'utf8' });
  const found = path.resolve(git.stdout.trim(), '../../../bin/bash.exe');
  if (fs.existsSync(found)) bash = found;
}
function sourceGuard({ checkout = H, matches = true, candidate = B } = {}) {
  const stub = `git(){ case "$1" in rev-parse) if [ "$2" = HEAD ]; then printf '%s\\n' "$ACTUAL_HEAD"; else printf '%s\\n' "\${2%\\^\\{commit\\}}"; fi;; fetch) return 0;; diff) [ "$MATCHES" = true ];; *) return 99;; esac; }\n`;
  return spawnSync(bash, ['--noprofile', '--norc'], { input: stub + guard, encoding: 'utf8', env: { ...process.env,
    BASELINE_SHA: A, CANDIDATE_SHA: candidate, HARNESS_SHA: H, EVENT_SHA: 'd'.repeat(40), WORKFLOW_SHA: 'e'.repeat(40),
    ACTUAL_HEAD: checkout, MATCHES: String(matches), GITHUB_SERVER_URL: 'https://github.com', GITHUB_REPOSITORY: 'example/project' } });
}
const goodGuard = sourceGuard();
assert.equal(goodGuard.status, 0, goodGuard.stderr);
assert.notEqual(sourceGuard({ checkout: A }).status, 0, 'wrong harness checkout accepted');
assert.notEqual(sourceGuard({ matches: false }).status, 0, 'mismatched workflow accepted');
assert.notEqual(sourceGuard({ candidate: 'master' }).status, 0, 'mutable candidate accepted by execution guard');
console.log('PASS actual shell guard rejects mismatched source and mutable refs');

const bashPath = file => process.platform === 'win32' ? file.replace(/\\/g, '/').replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase()) : file;
const scopeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-ci-scope-'));
try {
  const output = path.join(scopeDir, 'output');
  const scope = value(step('Decide whether anything but documentation changed', deploy), 'run', 8);
  const result = spawnSync(bash, ['--noprofile', '--norc'], {
    input: 'git(){ echo "unexpected checkout operation" >&2; return 99; }\n' + scope.replace(/\$\{\{.*?\}\}/g, 'unused'),
    encoding: 'utf8', env: { ...process.env, BENCHMARK_ONLY: 'true', GITHUB_OUTPUT: bashPath(output) },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.readFileSync(output, 'utf8').replace(/\r/g, ''), 'code=false\ningestor=false\n');
  const context = { github: { event_name: 'workflow_dispatch', ref: 'refs/heads/master' },
    inputs: { sqlite_benchmark: true }, needs: { changes: { outputs: { code: 'false', ingestor: 'false' } } } };
  for (const job of ['go-test', 'race-test', 'e2e-shard', 'e2e-test', 'image-check']) {
    assert.equal(Boolean(evaluate(value(block(deploy, job, 2), 'if', 4), context)), false, `benchmark dispatch enters ${job}`);
  }
} finally { fs.rmSync(scopeDir, { recursive: true, force: true }); }
console.log('PASS actual dispatch scope disables ordinary test and image jobs');

const run = value(step('Run bounded smoke and selected profile'), 'run', 8);
for (const flag of ['--baseline-sha "$BASELINE_SHA"', '--candidate-sha "$CANDIDATE_SHA"', '--harness-sha "$HARNESS_SHA"']) assert(run.includes(flag), 'missing independent ref ' + flag);
assert(run.includes('--corpus S --profile smoke --pairs 1'), 'mandatory S smoke missing');
assert(run.includes('--corpus B --profile primary --pairs 5'), 'paired B comparison missing');
assert(run.includes('--corpus B --profile diagnostic --pairs 1'), 'separate diagnostics missing');
assert(run.includes('cpu memory io') && run.includes('cgroup.subtree_control'), 'required cgroup delegation missing');
assert(workflow.includes('python3 -m unittest discover -s scripts/sqlite-benchmark'), 'cheap harness controls missing');
assert(workflow.includes("go-version: '1.27.2'") && workflow.includes("CGO_ENABLED: '1'"), 'native compiler configuration changed');
const artifacts = value(step('Upload only sanitized public evidence'), 'path', 10).trim().split('\n');
assert(artifacts.length > 0 && artifacts.every(file => file.endsWith('/public/')), 'private database/profile files may be uploaded');
assert(value(step('Remove only the owned cgroup'), 'run', 8).includes('realpath'), 'owned cleanup guard missing');
for (const name of ['Validate runner capacity', 'Verify exact refs and workflow source', 'Run bounded smoke and selected profile', 'Remove only the owned cgroup']) {
  const result = spawnSync(bash, ['-n'], { input: value(step(name), 'run', 8), encoding: 'utf8' });
  assert.equal(result.status, 0, `${name}: ${result.stderr}`);
}
for (const profile of ['smoke', 'primary', 'diagnostic']) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-ci-run-'));
  try {
    const log = path.join(dir, 'commands');
    fs.writeFileSync(log, '');
    // Replace privileged host setup and the workload with command capture only.
    // Execute the actual workflow shell and assert exactly what it would run.
    const stubs = `
      sudo(){ case "$1" in mkdir|chown) return 0;; tee) cat >/dev/null;; *) return 99;; esac; }
      grep(){ [ "$1" = -qw ] && [[ "$2" =~ ^(cpu|memory|io)$ ]]; }
      python3(){ node -e 'require("fs").appendFileSync(process.env.COMMAND_LOG, JSON.stringify(process.argv.slice(1))+"\\n")' "$@"; }
    `;
    const result = spawnSync(bash, ['--noprofile', '--norc'], { input: stubs + run, encoding: 'utf8',
      env: { ...process.env, PROFILE: profile, BASELINE_SHA: A, CANDIDATE_SHA: B, HARNESS_SHA: H,
        GITHUB_RUN_ID: 'unit-' + process.pid, GITHUB_RUN_ATTEMPT: '1', COMMAND_LOG: log,
        SMOKE_OUTPUT: '/tmp/corescope-bench-smoke', BENCH_OUTPUT: '/tmp/corescope-bench-selected' } });
    assert.equal(result.status, 0, result.stderr);
    const commands = fs.readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line));
    assert.equal(commands.length, profile === 'smoke' ? 1 : 2);
    for (const [index, args] of commands.entries()) {
      const option = name => args[args.indexOf(name) + 1];
      assert.equal(args[0], 'scripts/sqlite-benchmark/run.py');
      assert.equal(option('--baseline-sha'), A);
      assert.equal(option('--candidate-sha'), B);
      assert.equal(option('--harness-sha'), H);
      assert.equal(option('--corpus'), index === 0 ? 'S' : 'B');
      assert.equal(option('--profile'), index === 0 ? 'smoke' : profile);
      assert.equal(option('--pairs'), index === 1 && profile === 'primary' ? '5' : '1');
      assert.equal(option('--ingest-rate'), '50');
      assert.equal(option('--http-rate'), '20');
    }
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
}
console.log('PASS bounded native smoke, separate optional profiles, and public-only evidence');
