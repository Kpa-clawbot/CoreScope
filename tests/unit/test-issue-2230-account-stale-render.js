/**
 * #2230: an Account page left while CSAuth.ready() is pending must not render
 * over the next page, and destroy() must drop the retained profile view.
 * Loads the real public/account.js in a vm sandbox with a held ready().
 *
 * Usage: node tests/unit/test-issue-2230-account-stale-render.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public/account.js'), 'utf8');
let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}
const flush = () => new Promise((r) => setTimeout(r, 10));

function load(hash, opts) {
  opts = opts || {};
  const nav = { gen: 0 };
  const els = {};
  const listeners = {};
  let release;
  const ready = new Promise((r) => { release = r; });
  const user = { current: opts.user || null };
  const mkEl = () => ({ value: '', textContent: '', innerHTML: '', addEventListener() {}, querySelector: () => null });
  const loc = { hash };
  const CSAuth = {
    ready: () => ready, isEnabled: () => opts.enabled !== false, user: () => user.current,
    request: () => Promise.resolve({ ok: true, status: 200, data: [] }),
    say() {}, errText: () => '', setUser() {}, logout: () => Promise.resolve({ ok: true }),
  };
  const ctx = {
    console, Promise, String, Object, URLSearchParams, CSAuth, location: loc,
    history: { replaceState(a, b, h) { loc.hash = h; } },
    escapeHtml: (s) => String(s),
    viewGeneration: () => nav.gen,
    document: { getElementById: (id) => els[id] || (els[id] = mkEl()) },
  };
  let page = null;
  ctx.registerPage = (name, p) => { if (name === 'account') page = p; };
  ctx.window = Object.assign({ CSAuth, addEventListener(t, fn) { listeners[t] = fn; } }, {});
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  // The shared #app container, counting every render into it.
  const app = { writes: [], set innerHTML(v) { this.writes.push(v); this._html = v; }, get innerHTML() { return this._html; } };
  return {
    page, app, nav, loc, release, user,
    fire(detail) { user.current = detail; listeners['cs-auth-changed']({ detail }); },
  };
}

(async () => {
  console.log('Account: leaving before the session is known');
  {
    const t = load('#/account/login');
    t.page.init(t.app, 'login');
    t.page.destroy(); t.nav.gen++;
    t.loc.hash = '#/nodes';
    t.app.innerHTML = 'NODES LIST';
    t.release();
    await flush();
    assert(t.app.innerHTML === 'NODES LIST', 'the login view does not replace the next page');
  }
  {
    const t = load('#/account/login', { enabled: false });
    t.page.init(t.app, 'login');
    t.page.destroy(); t.nav.gen++;
    t.app.innerHTML = 'NODES LIST';
    t.release();
    await flush();
    assert(t.app.innerHTML === 'NODES LIST', 'nor does the "not enabled" notice');
  }

  console.log('Account: re-entering before the session is known');
  {
    const t = load('#/account/login');
    t.page.init(t.app, 'register');
    t.page.destroy(); t.nav.gen++;
    t.page.init(t.app, 'login');
    t.release();
    await flush();
    assert(t.app.innerHTML.includes('id="loginForm"') && !t.app.writes.some((h) => h.includes('id="registerForm"')),
      'only the current visit renders');
  }

  console.log('Account: the current visit still renders');
  {
    const t = load('#/account/login');
    t.page.init(t.app, 'login');
    t.release();
    await flush();
    assert(t.app.innerHTML.includes('Log in'), 'the login view renders once the session is known');
  }

  console.log('Account: destroy drops the retained profile view');
  {
    const t = load('#/account', { user: { id: 1, email: 'a@b', role: 'user', displayName: 'A' } });
    t.page.init(t.app, null);
    t.release();
    await flush();
    assert(t.app.innerHTML.includes('My account'), 'the profile renders for the signed-in user');
    t.page.destroy(); t.nav.gen++;
    // Back on #/account while the next init still waits for the session.
    t.app.innerHTML = 'LOADING NEXT VISIT';
    t.fire({ id: 2, email: 'c@d', role: 'user', displayName: 'C' });
    await flush();
    assert(t.app.innerHTML === 'LOADING NEXT VISIT', 'an auth change after destroy does not re-render the old view');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
