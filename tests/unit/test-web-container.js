/* test-web-container.js — the web container (Dockerfile.web) and
 * docker-compose.split.yml agree with the rest of the repo.
 *
 * The web container serves the static UI and forwards the API and the
 * WebSocket to a CoreScope server container, so the UI is same-origin and
 * needs no CORS. CI builds and smokes the image (deploy.yml, image-check);
 * these pin what that run cannot see: how the UI is packaged, what Caddy
 * forwards, the cache rules a static host gets from deploy/static/_headers,
 * and how compose wires the server.
 */
'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const ROOT = path.join(__dirname, '..', '..');
const read = (p) => fs.readFileSync(path.join(ROOT, p), 'utf8');
const code = (text) => text.split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));

const dockerfile = code(read('Dockerfile.web')).join('\n').replace(/\\\n/g, ' ');
const caddyfile = code(read('docker/web/Caddyfile'));
const compose = read('docker-compose.split.yml');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

console.log('\n=== web container ===');

test('the image packages public/ with scripts/static/build.sh, same-origin with logins', () => {
  const run = dockerfile.split('\n').find((l) => /^RUN\b.*scripts\/static\/build\.sh/.test(l));
  assert.ok(run, 'no RUN of scripts/static/build.sh');
  assert.ok(/\bAPI_BASE=same-origin\b/.test(run), run);
  assert.ok(/\bAUTH=true\b/.test(run), run);
  assert.ok(/\bOUT=\/srv\b/.test(run), run);
});

test('Caddy serves /srv and forwards /api/* and /ws to BACKEND', () => {
  assert.ok(caddyfile.includes('root * /srv'), 'root * /srv');
  assert.ok(caddyfile.includes('file_server'), 'file_server');
  const matcher = caddyfile.find((l) => l.startsWith('@backend '));
  assert.ok(matcher, '@backend matcher');
  assert.deepStrictEqual(matcher.split(/\s+/).slice(2).sort(), ['/api/*', '/ws']);
  assert.ok(caddyfile.includes('reverse_proxy @backend {$BACKEND}'), 'reverse_proxy @backend {$BACKEND}');
});

test('Caddy sends the no-cache rule for exactly the paths in deploy/static/_headers', () => {
  const headers = code(read('deploy/static/_headers'));
  const fromHeaders = headers.filter((l) => l.startsWith('/')).sort();
  const matcher = caddyfile.find((l) => l.startsWith('@nocache '));
  assert.ok(matcher, '@nocache matcher');
  assert.deepStrictEqual(matcher.split(/\s+/).slice(2).sort(), fromHeaders);
  const rule = headers.find((l) => l.startsWith('Cache-Control:')).replace('Cache-Control:', '').trim();
  assert.ok(caddyfile.includes(`header @nocache Cache-Control "${rule}"`), rule);
});

test('compose points BACKEND at the server service, on the port the image runs it on', () => {
  const port = read('docker/supervisord-go-no-caddy.conf').match(/corescope-server .*-port (\d+)/)[1];
  assert.ok(/^ {2}corescope:\s*$/m.test(compose), 'no corescope service');
  assert.ok(compose.includes(`BACKEND=corescope:${port}`), `BACKEND=corescope:${port}`);
  assert.ok(dockerfile.includes(`BACKEND=corescope:${port}`), 'Dockerfile.web default BACKEND');
});

test("compose turns the server's own Caddy off and builds the web image from Dockerfile.web", () => {
  assert.ok(compose.includes('DISABLE_CADDY=true'), 'DISABLE_CADDY=true');
  assert.ok(/dockerfile:\s*Dockerfile\.web\b/.test(compose), 'dockerfile: Dockerfile.web');
});

test('compose has no default server image, so the server and the UI versions are picked together', () => {
  // The web image is built from this checkout; a default such as :latest (the
  // last release) would pair it with a different server version unasked.
  const line = compose.split('\n').find((l) => /^\s+image:.*CORESCOPE_IMAGE/.test(l));
  assert.ok(line, 'no image line using CORESCOPE_IMAGE');
  assert.ok(/\$\{CORESCOPE_IMAGE:\?[^}]+\}/.test(line), 'CORESCOPE_IMAGE must be required (${VAR:?message}): ' + line);
});

test("compose publishes the server's MQTT broker on localhost only, unless MQTT_BIND says otherwise", () => {
  // docker/mosquitto.conf accepts anonymous clients, so anyone who reaches a
  // published 1883 could feed the analyzer packets.
  assert.ok(/^allow_anonymous true$/m.test(read('docker/mosquitto.conf')), 'mosquitto.conf changed; revisit this default');
  const mapping = compose.match(/^\s+- "([^"]*:1883)"\s*$/m);
  assert.ok(mapping, 'no 1883 port mapping');
  assert.ok(mapping[1].startsWith('${MQTT_BIND:-127.0.0.1}:'), mapping[1]);
});

test('compose publishes 443 over UDP too, for the HTTP/3 Caddy advertises', () => {
  // With a domain in SITE_ADDRESS, Caddy serves HTTP/3 and sends Alt-Svc:
  // h3=":443". Without the UDP port, browsers try QUIC, time out and fall back.
  assert.ok(compose.includes('- "${HTTPS_PORT:-443}:443"'), 'tcp 443');
  assert.ok(compose.includes('- "${HTTPS_PORT:-443}:443/udp"'), 'udp 443');
});

test('the web image names its build: APP_VERSION becomes VERSION, and compose passes both build args', () => {
  const run = dockerfile.split('\n').find((l) => /^RUN\b.*scripts\/static\/build\.sh/.test(l));
  assert.ok(/\bARG APP_VERSION\b/.test(dockerfile), 'ARG APP_VERSION');
  assert.ok(/\bVERSION="\$APP_VERSION"/.test(run), run);
  assert.ok(compose.includes('APP_VERSION: ${APP_VERSION:-}'), 'compose APP_VERSION build arg');
  assert.ok(compose.includes('GIT_COMMIT: ${GIT_COMMIT:-}'), 'compose GIT_COMMIT build arg');
});

test('without GIT_COMMIT the image uses an id that cannot pass for a commit', () => {
  // A bare timestamp is all digits, which a commit check would take for a
  // short sha and compare with the server's commit.
  assert.ok(dockerfile.includes('build-$(date +%s)'), 'fallback build-<epoch>');
});

test('compose fixes the web container address, so the server can trust that address alone', () => {
  // trustedProxies believes X-Forwarded-For from the listed addresses. The
  // compose subnet also holds its gateway, where a client reaching a published
  // port 3000 through Docker's proxy arrives from, so the docs name only the
  // web container, and its address must survive a recreate.
  const net = '${SPLIT_NET:-172.30.83}';
  assert.ok(compose.includes(`subnet: "${net}.0/24"`), 'fixed subnet');
  assert.ok(compose.includes(`ip_range: "${net}.0/25"`), 'automatic addresses must stay below .128');
  assert.ok(compose.includes(`ipv4_address: "${net}.200"`), 'fixed web container address');
  assert.ok(read('docs/deployment.md').includes('`172.30.83.200`'), 'docs/deployment.md names the default address');
});

test('CI builds and smokes the web image and validates the split compose file', () => {
  // Nothing publishes the web image; without this, a broken base image,
  // package or Caddyfile reaches an operator first.
  const ci = read('.github/workflows/deploy.yml');
  assert.ok(/docker buildx build \. -f Dockerfile\.web --load/.test(ci), 'image-check builds Dockerfile.web');
  assert.ok(/curl[^\n]*\/api\/healthz/.test(ci), 'the smoke checks that /api reaches BACKEND');
  assert.ok(/docker compose -f docker-compose\.split\.yml config --quiet/.test(ci), 'split compose validated');
});

console.log(`\nweb container: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
