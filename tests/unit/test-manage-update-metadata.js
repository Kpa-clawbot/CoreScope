const assert = require('node:assert/strict');
const manageFixture = require('../helpers/manage-shell');

for (const target of ['latest', 'v-new', '']) {
  const fixture = manageFixture();
  try {
    const result = fixture.run(`
task_ref=old
git(){
  case "$1" in
    describe) printf 'v-%s\\n' "$task_ref" ;;
    rev-parse) printf '%s-sha\\n' "$task_ref" ;;
    checkout) task_ref=new ;;
    tag) printf 'v-new\\n' ;;
    fetch) : ;;
    *) return 1 ;;
  esac
}
docker(){
  case "$*" in
    'compose version') : ;;
    *'build prod') printf 'source=%s version=%s commit=%s\\n' "$task_ref" "$APP_VERSION" "$GIT_COMMIT" ;;
    *'/app/storage.sh prod field state') printf 'ready\\n' ;;
    *'/app/storage.sh prod field backend') printf 'sqlite\\n' ;;
    *'up -d --force-recreate prod') : ;;
    *) printf 'unexpected docker call\\n' >&2; return 1 ;;
  esac
}
`, `cmd_update '${target}'`);
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    assert.match(result.stdout, /source=new version=v-new commit=new-sha/, 'new image was labelled with the previous checkout');
  } finally {
    fixture.cleanup();
  }
}
console.log('PASS update build identity follows latest, explicit, and default release checkout');
