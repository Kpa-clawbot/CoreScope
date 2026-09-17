/* test-ws-stale-watchdog-1074.js: the shared WebSocket in app.js must notice
 * a socket that has gone silent and replace it (#1074).
 *
 * A half-open TCP connection (a proxy idle timeout, NAT state dropped, a
 * laptop that slept) can leave the browser's WebSocket in OPEN state for
 * minutes without ever firing onclose, so the page silently stops updating.
 * The server sends an app-level heartbeat every 30s; the client treats a
 * socket that has received nothing for WS_STALE_MS as dead.
 *
 * Loads the real public/app.js in a vm sandbox with a fake clock, fake timers
 * and a fake WebSocket.
 */
'use strict';

const vm = require('vm');
const fs = require('fs');
const assert = require('assert');

console.log('--- test-ws-stale-watchdog-1074.js ---');

// Mirrors WS_STALE_MS in public/app.js and wsHeartbeat in cmd/server/websocket.go.
const STALE_MS = 75000;
const HEARTBEAT = '{"type":"heartbeat"}';

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}\n     ${e.stack.split('\n').slice(1, 3).join('\n     ')}`); }
}

function inertElement() {
  return new Proxy(function () {}, {
    get(_, key) {
      if (key === Symbol.toPrimitive) return () => '';
      if (key === Symbol.iterator) return function* () {};
      return inertElement();
    },
    set() { return true; },
    apply() { return inertElement(); },
  });
}

function makeSandbox() {
  // now drives timers and performance.now(); wallOffset is added to Date.now()
  // only, so a test can step the wall clock the way NTP or a user can.
  const clock = { now: 1700000000000, wallOffset: 0 };
  let nextId = 1;
  const timers = new Map(); // id -> { fn, at }

  function setTimeoutFake(fn, ms) {
    const id = nextId++;
    timers.set(id, { fn, at: clock.now + Math.max(0, ms || 0) });
    return id;
  }
  function clearTimeoutFake(id) { timers.delete(id); }
  // Runs every timer due within the next `ms`, in due order, moving the clock.
  function advance(ms) {
    const end = clock.now + ms;
    for (let fired = 0; ; fired++) {
      if (fired > 10000) throw new Error('timer storm: over 10000 timers fired within ' + ms + 'ms');
      let dueId = null, due = null;
      for (const [id, t] of timers) {
        if (t.at <= end && (due === null || t.at < due.at)) { dueId = id; due = t; }
      }
      if (dueId === null) break;
      timers.delete(dueId);
      clock.now = due.at;
      due.fn();
    }
    clock.now = end;
  }

  class FakeDate extends Date {}
  FakeDate.now = () => clock.now + clock.wallOffset;

  const sockets = [];
  function FakeWS(url) {
    this.url = url;
    this.readyState = 0; // CONNECTING
    this.closed = false;
    this.onopen = null; this.onclose = null; this.onerror = null; this.onmessage = null;
    sockets.push(this);
  }
  FakeWS.prototype.open = function () { this.readyState = 1; if (this.onopen) this.onopen({}); };
  FakeWS.prototype.message = function (data) { if (this.onmessage) this.onmessage({ data }); };
  FakeWS.prototype.close = function () {
    if (this.closed) return;
    this.closed = true;
    this.readyState = 3;
    if (this.onclose) this.onclose({});
  };
  FakeWS.prototype.send = function () {};

  const docListeners = {};
  const winListeners = {};
  const document = {
    readyState: 'complete',
    hidden: false,
    documentElement: { scrollTop: 0, style: { setProperty() {} }, setAttribute() {}, getAttribute() { return null; } },
    body: { appendChild() {}, contains() { return true; } },
    head: { appendChild() {} },
    createElement() { return { style: {}, classList: { add() {}, remove() {} }, appendChild() {}, setAttribute() {} }; },
    getElementById() { return null; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    addEventListener(ev, fn) { (docListeners[ev] = docListeners[ev] || []).push(fn); },
    removeEventListener() {},
  };
  const window = {
    addEventListener(ev, fn) { (winListeners[ev] = winListeners[ev] || []).push(fn); },
    removeEventListener() {},
    dispatchEvent() {},
    matchMedia() { return { matches: false, addEventListener() {} }; },
    WS_RECONNECT_MS: 3000,
  };
  const ctx = {
    console,
    setTimeout: setTimeoutFake, clearTimeout: clearTimeoutFake,
    setInterval() { return 0; }, clearInterval() {},
    Date: FakeDate, Math, JSON, Object, Array, String, Number, Boolean,
    Error, RegExp, Map, Set, Symbol, Promise,
    requestAnimationFrame() { return 0; },
    performance: { now: () => clock.now },
    location: { protocol: 'http:', host: 'localhost', hash: '' },
    navigator: { userAgent: 'test' },
    WebSocket: FakeWS,
    fetch() { return Promise.resolve({ ok: true, json() { return Promise.resolve({}); } }); },
    localStorage: { getItem() { return null; }, setItem() {}, removeItem() {} },
    document, window,
    CustomEvent: function (type, init) { this.type = type; this.detail = (init || {}).detail; },
  };
  window.location = ctx.location;
  window.document = document;
  ctx.self = window;
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('public/app.js', 'utf8'), ctx);

  return {
    ctx, clock, timers, advance, sockets, document,
    fireDoc(ev) { (docListeners[ev] || []).forEach((fn) => fn({ type: ev })); },
    fireWin(ev) { (winListeners[ev] || []).forEach((fn) => fn({ type: ev })); },
    // Runs app.js's real startup listeners, as the browser does. The startup
    // code wires the whole page shell, so every element lookup gets an inert
    // stand-in that accepts any property or call.
    boot() {
      document.getElementById = document.querySelector = () => inertElement();
      ctx.getComputedStyle = () => inertElement();
      (winListeners.DOMContentLoaded || []).forEach((fn) => fn({ type: 'DOMContentLoaded' }));
    },
  };
}

console.log('\n=== silence past the stale threshold replaces the socket ===');

test('an open socket that receives nothing for WS_STALE_MS is closed and replaced exactly once', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();

  box.advance(STALE_MS - 1);
  assert.strictEqual(box.sockets.length, 1, 'must not reconnect before the threshold');
  assert.strictEqual(first.closed, false, 'must not close before the threshold');

  box.advance(1);
  assert.strictEqual(first.closed, true, 'the silent socket must be closed');
  assert.strictEqual(box.sockets.length, 2, 'exactly one replacement socket must be opened');

  // Nothing else is pending that would open a third socket: the replacement
  // is still connecting, and the dead socket's handlers are detached.
  box.advance(STALE_MS - 1);
  assert.strictEqual(box.sockets.length, 2, 'no second replacement');
  assert.strictEqual(first.onclose, null, 'the dropped socket must not be able to schedule a reconnect');
  assert.strictEqual(first.onmessage, null, 'the dropped socket must not dispatch messages');
});

test('a socket that never opens is also replaced after WS_STALE_MS', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  box.advance(STALE_MS);
  assert.strictEqual(box.sockets[0].closed, true);
  assert.strictEqual(box.sockets.length, 2);
});

console.log('\n=== regular traffic keeps the socket ===');

test('heartbeats every 30s keep one socket alive for ten minutes', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  for (let i = 0; i < 20; i++) {
    box.advance(30000);
    first.message(HEARTBEAT);
  }
  assert.strictEqual(box.sockets.length, 1, 'no reconnect while heartbeats arrive');
  assert.strictEqual(first.closed, false);
});

test('packet traffic without heartbeats also counts as liveness', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  for (let i = 0; i < 20; i++) {
    box.advance(STALE_MS - 1000);
    first.message(JSON.stringify({ type: 'packet', data: { id: i } }));
  }
  assert.strictEqual(box.sockets.length, 1);
  assert.strictEqual(first.closed, false);
});

test('a heartbeat is not dispatched to page listeners and does not pulse the logo', () => {
  const box = makeSandbox();
  const seen = [];
  box.ctx.onWS((m) => seen.push(m));
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  const logo = box.ctx.window.__corescopeLogo;
  const before = logo.stats.triggered + logo.stats.dropped;

  first.message(HEARTBEAT);
  assert.strictEqual(seen.length, 0, 'heartbeat must not reach onWS listeners');
  assert.strictEqual(logo.stats.triggered + logo.stats.dropped, before, 'heartbeat must not pulse the logo');

  first.message(JSON.stringify({ type: 'packet', data: { id: 1 } }));
  assert.strictEqual(seen.length, 1, 'a packet must still reach listeners');
});

console.log('\n=== resuming a hidden tab or coming back online checks immediately ===');

function throttledJump(box, ms) {
  // A hidden tab's timers can be held back; move the clock without running them.
  box.clock.now += ms;
}

test('visibilitychange to visible after a long silence reconnects without waiting for the timer', () => {
  const box = makeSandbox();
  // The real startup path, so a page that never calls setupWSResumeCheck fails here.
  box.boot();
  assert.strictEqual(box.sockets.length, 1, 'startup opens one socket');
  box.sockets[0].open();

  box.document.hidden = true;
  box.fireDoc('visibilitychange');
  throttledJump(box, 5 * 60 * 1000);
  box.document.hidden = false;
  box.fireDoc('visibilitychange');

  assert.strictEqual(box.sockets[0].closed, true, 'the silent socket must be dropped on resume');
  assert.strictEqual(box.sockets.length, 2, 'a replacement must be opened on resume');
});

test('visibilitychange to visible with recent traffic keeps the socket', () => {
  const box = makeSandbox();
  box.ctx.setupWSResumeCheck();
  box.ctx.connectWS();
  box.sockets[0].open();
  throttledJump(box, 20000);
  box.sockets[0].message(HEARTBEAT);
  throttledJump(box, 20000);
  box.fireDoc('visibilitychange');
  assert.strictEqual(box.sockets.length, 1);
  assert.strictEqual(box.sockets[0].closed, false);
  assert.strictEqual(box.timers.size, 1, 'the check must replace the watchdog, not add a second one');
});

test('becoming hidden does not trigger a check', () => {
  const box = makeSandbox();
  box.ctx.setupWSResumeCheck();
  box.ctx.connectWS();
  box.sockets[0].open();
  throttledJump(box, 5 * 60 * 1000);
  box.document.hidden = true;
  box.fireDoc('visibilitychange');
  assert.strictEqual(box.sockets.length, 1);
});

test('the online event after a long silence reconnects immediately', () => {
  const box = makeSandbox();
  box.ctx.setupWSResumeCheck();
  box.ctx.connectWS();
  box.sockets[0].open();
  throttledJump(box, STALE_MS + 1);
  box.fireWin('online');
  assert.strictEqual(box.sockets[0].closed, true);
  assert.strictEqual(box.sockets.length, 2);
});

test('repeated resume events open one replacement, not one each', () => {
  const box = makeSandbox();
  box.ctx.setupWSResumeCheck();
  box.ctx.connectWS();
  box.sockets[0].open();
  throttledJump(box, STALE_MS + 1);
  box.fireWin('online');
  box.fireDoc('visibilitychange');
  box.fireWin('online');
  assert.strictEqual(box.sockets.length, 2, 'the replacement is fresh, so later checks leave it alone');
});

console.log('\n=== a wall-clock step does not disable the watchdog ===');

test('the clock stepping back an hour as the socket goes silent still replaces it within WS_STALE_MS', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  for (let i = 0; i < 3; i++) {
    box.advance(30000);
    first.message(HEARTBEAT);
  }
  box.clock.wallOffset -= 60 * 60 * 1000; // step back, then nothing more arrives
  box.advance(STALE_MS);
  assert.strictEqual(first.closed, true, 'the silent socket must be dropped');
  assert.strictEqual(box.sockets.length, 2, 'one replacement within WS_STALE_MS of the step');
});

for (const [label, stepMs] of [['forward', 60 * 60 * 1000], ['back', -60 * 60 * 1000]]) {
  test(`a clock step ${label} on a healthy socket costs at most one extra reconnect`, () => {
    const box = makeSandbox();
    box.ctx.connectWS();
    box.sockets[0].open();
    box.advance(40000);
    box.sockets[0].message(HEARTBEAT);
    box.advance(10000);
    box.clock.wallOffset += stepMs;
    for (let i = 0; i < 20; i++) {
      box.advance(30000);
      const cur = box.sockets[box.sockets.length - 1];
      if (cur.readyState === 0) cur.open();
      cur.message(HEARTBEAT);
    }
    assert.ok(box.sockets.length <= 2, 'got ' + box.sockets.length + ' sockets over ten minutes');
  });
}

console.log('\n=== timers are cleaned up on close ===');

test('after onclose only the reconnect timer is pending, and it opens one socket', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  first.close(); // the browser saw the close

  assert.strictEqual(box.timers.size, 1, 'the watchdog must be cleared; only the reconnect remains');
  box.advance(3000);
  assert.strictEqual(box.sockets.length, 2, 'the reconnect opens one socket');
  box.sockets[1].open();
  for (let i = 0; i < 6; i++) {
    box.advance(30000);
    box.sockets[1].message(HEARTBEAT);
  }
  assert.strictEqual(box.sockets.length, 2, 'the closed socket\'s watchdog must not fire later');
});

test('pullReconnect on a socket that is not open leaves one socket and no stray reconnect', () => {
  const box = makeSandbox();
  box.ctx.connectWS(); // still CONNECTING
  box.ctx.window.pullReconnect();
  assert.strictEqual(box.sockets.length, 2, 'pull opens a replacement at once');
  assert.strictEqual(box.sockets[0].closed, true, 'the previous socket is closed');
  box.advance(3000);
  assert.strictEqual(box.sockets.length, 2, 'the old socket\'s close must not schedule a third socket');
});

test('pullReconnect on an open socket replaces it at once instead of waiting for onclose', () => {
  const box = makeSandbox();
  box.ctx.connectWS();
  const first = box.sockets[0];
  first.open();
  // A half-open socket may not fire onclose for about a minute after close().
  first.close = function () { this.closed = true; this.readyState = 2; };
  box.ctx.window.pullReconnect();
  assert.strictEqual(box.sockets.length, 2, 'the replacement must exist right after the pull');
  assert.strictEqual(first.closed, true, 'the previous socket is closed');
  assert.strictEqual(first.onclose, null, 'the previous socket is detached, so a late onclose cannot reconnect again');
  box.advance(STALE_MS - 1);
  assert.strictEqual(box.sockets.length, 2, 'exactly one socket results from the pull');
});

console.log('\n=== Results: ' + passed + ' passed, ' + failed + ' failed ===\n');
process.exit(failed > 0 ? 1 : 0);
