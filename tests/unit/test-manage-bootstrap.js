const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const manageFixture = require('../helpers/manage-shell');

for (const endings of ['\r\n', '\n', null]) {
  const fixture = manageFixture();
  try {
    if (endings !== null) {
      fs.writeFileSync(path.join(fixture.dir, '.env'), ['PROD_HTTP_PORT=18080', 'DISABLE_MOSQUITTO=true', ''].join(endings));
    }
    const result = fixture.run(`
docker(){ [ "$*" = 'compose version' ]; }
git(){ printf 'test-revision\\n'; }
# Git Bash grep otherwise strips CRLF before matching. Binary mode reproduces
# the Linux host's CR detection without changing the management code under test.
grep(){ command grep -U "$@"; }
`, `printf 'port=%s mqtt=%s\\n' "\${PROD_HTTP_PORT:-unset}" "\${DISABLE_MOSQUITTO:-unset}"`);
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    if (endings !== null) {
      assert.match(result.stdout, /port=18080 mqtt=true/);
      assert(!fs.readFileSync(path.join(fixture.dir, '.env'), 'utf8').includes('\r'), 'CRLF was not normalized');
    }
  } finally {
    fixture.cleanup();
  }
}
console.log('PASS actual management bootstrap with CRLF, LF, and absent environment file');
