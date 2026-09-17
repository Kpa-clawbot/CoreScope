const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// Run the real release shell steps with registry/dispatch commands stubbed out.
// No GitHub writes or container builds: node test-issue-1956-release-routing.js
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const { spawnSync } = require('node:child_process');

const read = name => fs.readFileSync(path.join(REPO_ROOT, '.github/workflows', name), 'utf8').replace(/\r/g, '');
const fast = read('release-fast-path.yml');
const deploy = read('deploy.yml');
let bash = process.env.BASH_PATH || 'bash';
if (!process.env.BASH_PATH && process.platform === 'win32') {
  // Prefer Git Bash over Windows' WSL launcher; CI uses the native Linux bash.
  const git = spawnSync('git', ['--exec-path'], { encoding: 'utf8' });
  const gitBash = path.resolve((git.stdout || '').trim(), '../../../bin/bash.exe');
  if (git.status === 0 && fs.existsSync(gitBash)) bash = gitBash;
}

// Extract known YAML blocks, retaining the actual expressions and shell code.
// Full YAML syntax is separately checked by actionlint; no YAML dependency here.
function block(source, key, indent) {
  const lines = source.split('\n');
  const prefix = ' '.repeat(indent) + key + ':';
  const start = lines.findIndex(line => line.startsWith(prefix));
  if (start < 0) return '';
  let end = start + 1;
  while (end < lines.length && (!lines[end].trim() || lines[end].search(/\S/) > indent)) end++;
  return lines.slice(start, end).join('\n');
}

function value(source, key, indent) {
  const raw = block(source, key, indent);
  if (!raw) return '';
  const lines = raw.split('\n');
  const first = lines[0].slice(indent + key.length + 1).trim();
  return first === '|' || first === '>'
    ? lines.slice(1).map(line => line.slice(indent + 2)).join('\n').trimEnd()
    : first;
}

const steps = source => source.split(/(?=^      - name:)/m).slice(1);
function evaluate(expression, context) {
  if (!expression) return true;
  return vm.runInNewContext(expression.replace(/^\$\{\{|\}\}$/g, '').trim(), {
    ...context, startsWith: (text, prefix) => text.startsWith(prefix)
  });
}
const expand = (script, context) => script.replace(/\$\{\{(.*?)\}\}/g, (_, expression) => String(evaluate(expression, context)));
const bashPath = file => process.platform === 'win32' ? file.replace(/\\/g, '/').replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase()) : file;

function runSteps(source, context, edge, mutateFails = false) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-release-test-'));
  const output = path.join(dir, 'output');
  const log = path.join(dir, 'commands');
  fs.writeFileSync(log, '');
  fs.mkdirSync(path.join(dir, 'cmd/decrypt'), { recursive: true });
  // go() only logs, so nothing real is produced; the release job's own
  // static/runnable verification step still needs these to exist.
  for (const arch of ['amd64', 'arm64']) {
    fs.writeFileSync(path.join(dir, `corescope-decrypt-linux-${arch}`),
      '#!/bin/sh\necho "corescope-decrypt stub"\n', { mode: 0o755 });
  }
  const stubs = `
    log() { node -e 'require("fs").appendFileSync(process.env.COMMAND_LOG, JSON.stringify(process.argv.slice(1))+"\\n")' "$@"; }
    crane() {
      if [ "$1" = config ]; then
        [ "$EDGE_CONFIG" != missing ] || return 1
        printf '%s' "$EDGE_CONFIG"
      elif [ "$1" = manifest ]; then
        log crane "$@"
        printf '%s' "$EDGE_MANIFEST"
      elif [ "$1" = ls ]; then
        log crane "$@"
        printf 'edge\\ntmp-v9.8.7-linux-amd64\\ntmp-v9.8.7-linux-arm64\\n'
      else
        log crane "$@"
        [ "$1" != mutate ] || [ "$MUTATE_FAILS" != true ]
      fi
    }
    # gh api answers the tag lookup a dispatched republish makes; everything
    # else (workflow run) only gets logged.
    gh() {
      log gh "$@"
      if [ "$1" = api ]; then
        case "$*" in
          *.object.type*) printf 'commit\\n' ;;
          *.object.sha*)  printf '%s\\n' "$GH_API_SHA" ;;
        esac
      fi
    }
    tar() { log tar "$@"; }
    go() { log go "$GOOS" "$GOARCH" "$CGO_ENABLED" "$CC" "$@"; }
    file() { log file "$@"; echo "$1: ELF 64-bit LSB executable, statically linked"; }
    # Stand-in for the jq filters the release steps use. Each branch mirrors one
    # filter, so a filter that changes shape without the test knowing fails here
    # instead of silently returning nothing.
    jq() {
      node -e '
        const fs = require("fs");
        const filter = process.argv[1];
        const source = process.argv[2];
        const text = source && fs.existsSync(source) ? fs.readFileSync(source, "utf8") : fs.readFileSync(0, "utf8");
        const data = text.trim() ? JSON.parse(text) : {};
        const platform = m => [m.platform.os, m.platform.architecture].join("/") + (m.platform.variant ? "/" + m.platform.variant : "");
        const runnable = () => (data.manifests || []).filter(m => (m.platform && m.platform.architecture || "unknown") !== "unknown");
        if (filter.includes("org.opencontainers.image.revision")) {
          console.log((((data.config || {}).Labels || {})["org.opencontainers.image.revision"]) || "");
        } else if (filter.trim() === ".mediaType // \\"\\"") {
          console.log(data.mediaType || "");
        } else if (filter.includes("\\\\t\\\\(.digest)")) {
          for (const m of runnable()) console.log(platform(m) + "\\t" + m.digest);
        } else if (filter.includes("index|manifest.list")) {
          const isIndex = /index|manifest.list/.test(data.mediaType || "");
          console.log(isIndex ? runnable().map(platform).sort().join(",") : "single");
        } else {
          console.error("unstubbed jq filter: " + filter);
          process.exit(3);
        }
      ' "$2" "\${3:-}"
    }
  `;
  try {
    for (const step of steps(source)) {
      const script = value(step, 'run', 8);
      if (!script || !evaluate(value(step, 'if', 8), context)) continue;
      fs.writeFileSync(output, '');
      const result = spawnSync(bash, ['--noprofile', '--norc', '-e', '-o', 'pipefail'], {
        input: stubs + '\n' + expand(script, context), cwd: dir, encoding: 'utf8', timeout: 15000,
        env: {
          ...process.env, GITHUB_REF: context.github.ref, GITHUB_SHA: context.github.sha,
          GITHUB_OUTPUT: bashPath(output), COMMAND_LOG: bashPath(log), TMPDIR: bashPath(dir),
          EDGE_CONFIG: edge === null ? 'missing' : JSON.stringify({ config: { Labels: { 'org.opencontainers.image.revision': edge } } }),
          // :edge is a two-platform index plus the two buildx attestation
          // manifests, which is what the registry actually holds.
          EDGE_MANIFEST: JSON.stringify({
            mediaType: 'application/vnd.oci.image.index.v1+json',
            manifests: [
              { digest: 'sha256:' + '1'.repeat(64), platform: { os: 'linux', architecture: 'amd64' } },
              { digest: 'sha256:' + '2'.repeat(64), platform: { os: 'linux', architecture: 'arm64' } },
              { digest: 'sha256:' + '3'.repeat(64), platform: { os: 'unknown', architecture: 'unknown' } },
              { digest: 'sha256:' + '4'.repeat(64), platform: { os: 'unknown', architecture: 'unknown' } }
            ]
          }),
          GH_API_SHA: edge === null ? 'a'.repeat(40) : edge,
          MUTATE_FAILS: String(mutateFails)
        }
      });
      if (mutateFails && result.status !== 0) return { commands: commands(), failed: true };
      assert.equal(result.status, 0, `${value(step, '- name', 6)}: ${result.error || result.stderr}`);
      const id = value(step, 'id', 8);
      if (id) context.steps[id] = { outputs: Object.fromEntries(fs.readFileSync(output, 'utf8').trim().split('\n').filter(Boolean).map(line => {
        const at = line.indexOf('=');
        return [line.slice(0, at), line.slice(at + 1)];
      })) };
    }
    return { commands: commands(), failed: false };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
  function commands() { return fs.readFileSync(log, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line)); }
}

function context(ref = 'refs/tags/v9.8.7', event = 'workflow_dispatch', inputs = {}) {
  return { github: { ref, ref_name: ref.split('/').pop(), sha: 'a'.repeat(40), repository: 'example/corescope', event_name: event }, inputs, steps: {}, needs: {}, vars: {} };
}

// Honor the real job conditions AND implicit success() for needs. This catches
// release-artifacts accidentally depending on the skipped image or E2E jobs.
function route(ctx, failedJob) {
  for (const name of ['changes', 'go-test', 'e2e-test', 'build-and-publish', 'release-artifacts', 'deploy', 'publish']) {
    const job = block(deploy, name, 2);
    assert.ok(job, `missing ${name} job`);
    const needs = value(job, 'needs', 4).replace(/[\[\]\s]/g, '').split(',').filter(Boolean);
    const run = needs.every(need => ctx.needs[need].result === 'success') && evaluate(value(job, 'if', 4), ctx);
    ctx.needs[name] = { result: run ? (name === failedJob ? 'failure' : 'success') : 'skipped', outputs: { code: 'true' } };
  }
  return ctx.needs;
}

for (const [name, edge] of [['matching', 'a'.repeat(40)], ['missing', null], ['mismatched', 'b'.repeat(40)]]) {
  const ctx = context();
  const { commands } = runSteps(block(fast, 'retag-or-fallback', 2), ctx, edge);
  const dispatches = commands.filter(command => command[0] === 'gh');
  assert.equal(dispatches.length, 1, `${name}: tag must dispatch the artifact workflow exactly once`);
  const dispatch = dispatches[0];
  assert.deepEqual(dispatch.slice(1, 4), ['workflow', 'run', 'deploy.yml']);
  assert.equal(dispatch[dispatch.indexOf('--ref') + 1], ctx.github.ref, `${name}: dispatch must preserve the tag source`);
  assert.equal(dispatch[dispatch.indexOf('--repo') + 1], ctx.github.repository);
  const matching = name === 'matching';
  assert.equal(dispatch.includes('images_published=true'), matching);
  if (!matching) assert.ok(!dispatch.includes('--field') && !dispatch.includes('-f'), 'old-tag fallback must not require new workflow inputs');
  // One mutate per runnable platform of the :edge index, never one for the
  // index itself: `crane mutate` on an index silently drops to one platform,
  // which is how v3.11.0 shipped amd64-only.
  const mutates = commands.filter(command => command[0] === 'crane' && command[1] === 'mutate');
  assert.equal(mutates.length, matching ? 2 : 0, `${name}: one mutate per platform`);
  if (matching) {
    assert.deepEqual(mutates.map(command => command.at(-1)).sort(),
      ['ghcr.io/kpa-clawbot/corescope:tmp-v9.8.7-linux-amd64', 'ghcr.io/kpa-clawbot/corescope:tmp-v9.8.7-linux-arm64'],
      'each platform is mutated into its own scratch tag');
    assert.ok(mutates.every(command => command[2].includes('@sha256:')), 'mutate must address a platform by digest, not the index tag');
    const indexes = commands.filter(command => command[0] === 'crane' && command[1] === 'index');
    assert.equal(indexes.length, 1, 'the release tag is assembled as one index');
    assert.deepEqual(indexes[0].slice(1, 3), ['index', 'append']);
    assert.equal(indexes[0].filter(argument => argument === '-m').length, 2, 'the index carries both platforms');
    assert.equal(indexes[0].at(-1), 'ghcr.io/kpa-clawbot/corescope:v9.8.7');
  }
  assert.deepEqual(commands.filter(command => command[0] === 'crane' && command[1] === 'tag').map(command => command.at(-1)), matching ? ['v9.8', 'v9', 'latest'] : []);
  const jobs = route(context(undefined, undefined, { images_published: matching }));
  assert.equal(jobs['release-artifacts'].result, 'success', `${name}: release artifacts must run`);
  assert.equal(jobs['go-test'].result, 'success', `${name}: release still requires Go validation`);
  for (const job of ['e2e-test', 'build-and-publish']) assert.equal(jobs[job].result, matching ? 'skipped' : 'success', `${name}: ${job}`);
  assert.equal(jobs.deploy.result, 'skipped');
  assert.equal(jobs.publish.result, 'skipped');
  console.log(`PASS ${name} edge: one artifact dispatch, correct image route`);
}

const failedRetag = runSteps(block(fast, 'retag-or-fallback', 2), context(), 'a'.repeat(40), true);
assert.equal(failedRetag.failed, true);
assert.equal(failedRetag.commands.filter(command => command[0] === 'gh').length, 0, 'failed retag must not dispatch with images_published=true');

for (const [ref, event] of [['refs/heads/master', 'push'], ['refs/heads/master', 'workflow_dispatch'], ['refs/pull/1/merge', 'pull_request']]) {
  const jobs = route(context(ref, event, { images_published: true }));
  assert.equal(jobs['release-artifacts'].result, 'skipped', `${event}: no GitHub release`);
  assert.equal(jobs['build-and-publish'].result, 'success', `${event}: tag-only input must not skip branch/PR checks`);
  // There is more than one build-push-action step now: a PR-only two-arch build
  // that must NOT publish, and the GHCR push. Pin the pushing one by `push: true`
  // rather than by being first in the job.
  const buildSteps = steps(block(deploy, 'build-and-publish', 2)).filter(step => step.includes('uses: docker/build-push-action'));
  const publishing = buildSteps.filter(step => value(step, 'push', 10) === 'true');
  assert.equal(publishing.length, 1, 'exactly one step may publish to GHCR');
  assert.equal(Boolean(evaluate(value(publishing[0], 'if', 8), context(ref, event))), event === 'push', `${event}: GHCR publishing`);
  // The cross-toolchain gate: since the SQLite driver became cgo, a PR must
  // still build both architectures, and must do it without publishing.
  const prBuild = buildSteps.filter(step => value(step, 'push', 10) === 'false');
  assert.equal(prBuild.length, 1, 'PRs must get exactly one non-publishing two-arch build');
  assert.equal(value(prBuild[0], 'platforms', 10), 'linux/amd64,linux/arm64', 'the PR gate must cover both shipped architectures');
  assert.equal(Boolean(evaluate(value(prBuild[0], 'if', 8), context(ref, event))), event === 'pull_request', `${event}: PR-only two-arch gate`);
}
assert.equal(route(context(), 'go-test')['release-artifacts'].result, 'skipped', 'failed Go validation must block release');
const dispatchInput = block(deploy, 'images_published', 6);
assert.equal(value(dispatchInput, 'type', 8), 'boolean', 'dispatch flag must retain boolean semantics');
assert.equal(value(dispatchInput, 'default', 8), 'false', 'manual and fallback dispatches must build images by default');

const release = block(deploy, 'release-artifacts', 2);
const builds = runSteps(release, context(), null).commands.filter(command => command[0] === 'go');
// CGO_ENABLED=1 since the SQLite driver became github.com/mattn/go-sqlite3, and
// CC must be zig targeting musl — that is what makes the artifact static and
// cross-buildable. A silent revert to the Go-only toolchain fails here.
assert.deepEqual(builds.map(command => command.slice(1, 5)), [
  ['linux', 'amd64', '1', 'zig cc -target x86_64-linux-musl'],
  ['linux', 'arm64', '1', 'zig cc -target aarch64-linux-musl'],
]);
for (const command of builds) {
  assert.ok(command.includes("-ldflags=-s -w -extldflags '-static -Wl,-s' -X main.version=v9.8.7"), 'binary version must come from tag, and the artifact must stay static');
  assert.ok(command.includes('-tags'), 'netgo/osusergo/sqlite_omit_load_extension must survive');
}
const upload = steps(release).filter(step => step.includes('uses: softprops/action-gh-release@v2'));
assert.equal(upload.length, 1, 'publish both architectures together, before the release becomes immutable');
assert.equal(value(upload[0], 'fail_on_unmatched_files', 10), 'true', 'missing assets must prevent publication');
assert.deepEqual(value(upload[0], 'files', 10).trim().split('\n').map(line => line.trim()), ['corescope-decrypt-linux-amd64', 'corescope-decrypt-linux-arm64']);
assert.equal(value(upload[0], 'draft', 10), '', 'standard release action must finalize after both uploads');
assert.equal(value(upload[0], 'prerelease', 10), '', 'standard release action must upload before publishing');
const checkout = steps(release).find(step => step.includes('uses: actions/checkout@'));
assert.equal(value(checkout, 'ref', 10), '', 'checkout must retain the dispatched tag/SHA');
assert.ok(!value(block(deploy, 'push', 2), 'tags', 4), 'fast path must remain the sole tag-triggered image writer');
console.log('PASS failed retag/Go gates, branch/PR routes, and complete tagged release assets');

// Republishing the images for a tag that already has a release: the tag comes
// from the dispatch input, the commit is resolved from the tag itself (the
// workflow file's own ref is master there), and deploy.yml must NOT be
// dispatched again — the release exists and releases here are immutable.
{
  const sha = 'c'.repeat(40);
  const ctx = context('refs/heads/master', 'workflow_dispatch', { tag: 'v9.8.7' });
  const { commands } = runSteps(block(fast, 'retag-or-fallback', 2), ctx, sha);
  assert.equal(ctx.steps.semver.outputs.tag, 'v9.8.7', 'the dispatched tag drives the release tags');
  assert.equal(ctx.steps.semver.outputs.targetSha, sha, 'the tagged commit is resolved from the tag, not from github.sha');
  assert.ok(commands.some(command => command[0] === 'gh' && command[1] === 'api'), 'the tag has to be looked up');
  assert.equal(commands.filter(command => command[0] === 'gh' && command[1] === 'workflow').length, 0,
    'a republish must not dispatch deploy.yml into an immutable release');
  assert.equal(commands.filter(command => command[0] === 'crane' && command[1] === 'mutate').length, 2, 'still one mutate per platform');
  assert.equal(commands.filter(command => command[0] === 'crane' && command[1] === 'index').length, 1, 'still one index');
  assert.deepEqual(commands.filter(command => command[0] === 'crane' && command[1] === 'tag').map(command => command.at(-1)), ['v9.8', 'v9', 'latest']);
  console.log('PASS dispatched republish: tags rebuilt, no second release dispatch');
}
