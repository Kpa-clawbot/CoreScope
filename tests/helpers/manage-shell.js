const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.resolve(__dirname, '../..');
const bash = require('../../scripts/bash-path')();
const quote = value => "'" + value.replace(/'/g, "'\\''") + "'";
const posix = value => value.replace(/\\/g, '/');

// Source the complete production script from its real path. Only external
// commands supplied by each test are replaced; management functions stay real.
module.exports = function manageFixture() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-manage-test-'));
  return {
    dir,
    run(before = '', after = '') {
      return spawnSync(bash, ['--noprofile', '--norc'], {
        input: `set -e\n${before}\nsource ${quote(posix(path.join(root, 'manage.sh')))} help >/dev/null\n${after}\n`,
        cwd: dir,
        env: { ...process.env, PROD_DATA_DIR: posix(path.join(dir, 'data')), STAGING_DATA_DIR: posix(path.join(dir, 'staging')) },
        encoding: 'utf8',
        timeout: 20000,
      });
    },
    cleanup() {
      assert(path.resolve(dir).startsWith(path.resolve(os.tmpdir()) + path.sep));
      assert(path.basename(dir).startsWith('corescope-manage-test-'));
      fs.rmSync(dir, { recursive: true });
    },
  };
};
