const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const manageFixture = require('../helpers/manage-shell');
const root = path.resolve(__dirname, '../..');

const bases = ['docker-compose.yml', 'docker-compose.no-mosquitto.yml', 'docker-compose.example.yml',
  'docker-compose.staging.yml', 'docker-compose.staging.no-mosquitto.yml'];
for (const file of bases) {
  const source = fs.readFileSync(path.join(root, file), 'utf8');
  const command = source.match(/healthcheck:\s*\n\s*test:\s*(\[[^\n]+\])/);
  assert(command, `${file} has no health command`);
  assert.equal(JSON.parse(command[1]).at(-1), 'http://localhost:3000/api/healthz', `${file} accepts a partially loaded server`);
}

for (const becomesReady of [true, false]) {
  const fixture = manageFixture();
  try {
    fs.mkdirSync(path.join(fixture.dir, 'caddy-config'));
    fs.writeFileSync(path.join(fixture.dir, 'caddy-config/Caddyfile'), 'scope.example.org {\n}\n');
    const result = fixture.run(`
git(){ printf 'test-revision\\n'; }
docker(){
  case "$*" in
    'compose version') : ;;
    *'/api/stats') printf 'stats\\n' >> probes; return 0 ;;
    *'/api/healthz')
      printf 'ready\\n' >> probes
      [ ${becomesReady} = true ] && [ "$(wc -l < probes)" -ge 3 ] ;;
    logs*) : ;;
    *) return 1 ;;
  esac
}
sleep(){ :; }
curl(){ printf '%s\\n' "$*" > https-probe; }
`, 'verify_health');
    assert.equal(result.status, becomesReady ? 0 : 1, result.stderr || result.stdout);
    const probes = fs.readFileSync(path.join(fixture.dir, 'probes'), 'utf8').trim().split('\n');
    assert.equal(probes.length, becomesReady ? 3 : 45, 'readiness was not polled to success or timeout');
    assert(probes.every(p => p === 'ready'), 'verification used statistics as readiness');
    if (becomesReady) {
      assert.match(fs.readFileSync(path.join(fixture.dir, 'https-probe'), 'utf8'), /https:\/\/scope\.example\.org\/api\/healthz/);
    }
  } finally {
    fixture.cleanup();
  }
}
console.log('PASS all Compose healthchecks and management verification wait for readiness');
