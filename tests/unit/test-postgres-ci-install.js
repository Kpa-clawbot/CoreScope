'use strict';
// Execute the real CI installer with package/network/root writes sandboxed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const bash = require('../../scripts/bash-path')();
const script = fs.readFileSync(path.join(__dirname, '../../scripts/install-postgres-ci.sh'), 'utf8').replace(/\r/g, '');

function run(mode, env = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-pg-ci-test-'));
  const stubs = [
    'function . { ID=ubuntu; VERSION_CODENAME=noble; }',
    'curl() { printf "test signing key\\n"; }',
    'sudo() {',
    '  printf "%s\\n" "$*" >> commands',
    '  case "$1" in',
    '    test) [[ "$HAS_UBUNTU" == 1 ]] ;;',
    '    install) if [[ "$*" == *ubuntu.sources* ]]; then printf "Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\\n" > ubuntu.sources; fi ;;',
    '    tee) local name; name=$(basename "$2"); cat > "$name" ;;',
    '    apt-get) shift; apt_get "$@" ;;',
    '    *) echo "unexpected privileged command" >&2; return 99 ;;',
    '  esac',
    '}',
    'apt_get() {',
    '  # Simulate the unrelated runner repository that failed the real job.',
    '  if [[ ! -f 99corescope-ci-sources ]]; then echo "unrelated repository is no longer signed" >&2; return 100; fi',
    '  grep -qF \'Dir::Etc::sourcelist "/dev/null"\' 99corescope-ci-sources || return 101',
    '  grep -qF \'Dir::Etc::sourceparts "/etc/apt/corescope-ci-sources.list.d"\' 99corescope-ci-sources || return 102',
    '  [[ -f ubuntu.sources && -f corescope-pgdg.list ]] || return 103',
    '  grep -qF "signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc" corescope-pgdg.list || return 104',
    '  [[ "$REQUIRED_REPO_OK" == 1 ]] || { echo "required repository signature rejected" >&2; return 100; }',
    '}',
    'apt-cache() { printf "%s | %s | signed archive\\n" "$2" "$PACKAGE_VERSION"; }',
    'function /usr/lib/postgresql/18/bin/pg_dump { printf "pg_dump (PostgreSQL) %s\\n" "$INSTALLED_VERSION"; }',
    'function /usr/lib/postgresql/18/bin/postgres { printf "postgres (PostgreSQL) %s\\n" "$INSTALLED_VERSION"; }',
  ].join('\n');
  try {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s', '--', mode], {
      cwd: dir, input: stubs + '\n' + script, encoding: 'utf8',
      env: { ...process.env, GITHUB_ACTIONS: 'true', GITHUB_PATH: 'github-path',
        HAS_UBUNTU: '1', REQUIRED_REPO_OK: '1', PACKAGE_VERSION: '18.6-1.pgdg24.04+1',
        INSTALLED_VERSION: '18.6', ...env },
    });
    assert.ifError(result.error);
    const files = Object.fromEntries(fs.readdirSync(dir).map(name =>
      [name, fs.readFileSync(path.join(dir, name), 'utf8')]));
    return { status: result.status, stderr: result.stderr, files };
  } finally {
    // Only flat files created in this private mkdtemp directory.
    for (const name of fs.readdirSync(dir)) fs.unlinkSync(path.join(dir, name));
    fs.rmdirSync(dir);
  }
}

for (const mode of ['client', 'server']) {
  const result = run(mode);
  assert.equal(result.status, 0, mode + ': ' + result.stderr);
  assert.match(result.files.commands, /install -m 0644 \/etc\/apt\/sources.list.d\/ubuntu.sources \/etc\/apt\/corescope-ci-sources.list.d\/ubuntu.sources/);
  assert.match(result.files['corescope-pgdg.list'], /https:\/\/apt-archive.postgresql.org\/pub\/repos\/apt noble-pgdg-archive main/);
  assert.match(result.files.commands, /apt-get install -y --no-install-recommends postgresql-client-18=18\.6-1\.pgdg24\.04\+1 sqlite3/);
  assert.equal(result.files['github-path'].trim(), '/usr/lib/postgresql/18/bin');
  assert.doesNotMatch(result.files.commands, /allow-unauthenticated|AllowInsecure|trusted=yes|rm /);
  if (mode === 'server') {
    assert.match(result.files.commands, /postgresql-18=18\.6-1\.pgdg24\.04\+1/);
    assert.equal(result.files['createcluster.conf'].trim(), 'create_main_cluster = false');
  } else {
    assert.equal(result.files['createcluster.conf'], undefined);
    assert.doesNotMatch(result.files.commands, /postgresql-18=/);
  }
}
for (const [label, mode, env] of [
  ['not Actions', 'client', { GITHUB_ACTIONS: 'false' }],
  ['invalid mode', 'invalid', {}],
  ['missing official source', 'client', { HAS_UBUNTU: '0' }],
  ['required signature failure', 'client', { REQUIRED_REPO_OK: '0' }],
  ['pinned version unavailable', 'client', { PACKAGE_VERSION: '18.7-1.pgdg24.04+1' }],
]) {
  const result = run(mode, env);
  assert.notEqual(result.status, 0, label + ' must fail closed');
  assert.doesNotMatch(result.files.commands || '', /apt-get install/, label + ' must not install');
}
assert.notEqual(run('client', { INSTALLED_VERSION: '18.7' }).status, 0, 'wrong installed version must fail');
console.log('PASS PostgreSQL CI source isolation, signatures, exact versions and guards');
