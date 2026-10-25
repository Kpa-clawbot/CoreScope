// Resolve the shell used by tests that exercise the real management/CI scripts.
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

module.exports = function bashPath() {
  if (process.env.BASH_PATH) return process.env.BASH_PATH;
  if (process.platform === 'win32') {
    // --exec-path is stable whether PATH finds Git in cmd/, bin/ or mingw64/bin/.
    const git = spawnSync('git', ['--exec-path'], { encoding: 'utf8' });
    const candidate = path.resolve((git.stdout || '').trim(), '../../../bin/bash.exe');
    if (git.status === 0 && fs.existsSync(candidate)) return candidate;
  }
  return 'bash';
};
