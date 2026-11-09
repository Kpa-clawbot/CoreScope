/* public/beta-ui.js: the channel badge and classic link a static build adds
 * to the nav, the pre-filled beta issue URL, and the note that stands in for
 * a control an anonymous build cannot offer. */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { loadApiBase } = require('./api-base-sandbox');

const SRC = fs.readFileSync(path.join(__dirname, '..', '..', 'public', 'beta-ui.js'), 'utf8');
let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log('  ✅ ' + name); } catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Just enough DOM for renderNav: elements, ids, insertBefore after the brand.
function fakeDoc() {
  const byId = {};
  const make = (tag) => {
    const el = { tagName: tag.toUpperCase(), children: [], className: '', textContent: '', href: '', title: '' };
    el.appendChild = (c) => { el.children.push(c); if (c.id) byId[c.id] = c; return c; };
    return el;
  };
  const nav = make('div');
  const brand = make('a');
  nav.children.push(brand);
  nav.insertBefore = (el, ref) => {
    const i = ref ? nav.children.indexOf(ref) : nav.children.length;
    nav.children.splice(i < 0 ? nav.children.length : i, 0, el);
    (function index(e) { if (e.id) byId[e.id] = e; e.children.forEach(index); })(el);
  };
  brand.parentNode = nav;
  brand.nextSibling = null;
  return {
    nav, readyState: 'complete',
    createElement: make,
    getElementById: (id) => byId[id] || null,
    querySelector: (sel) => (sel === '.top-nav .nav-brand' ? brand : null),
    addEventListener() {},
  };
}

function load(deployment, hash) {
  const listeners = {};
  const doc = fakeDoc();
  const ctx = {
    document: doc,
    location: { protocol: 'https:', host: 'beta.example', hash: hash || '#/packets' },
    navigator: { userAgent: 'TestBrowser/1.0' },
    addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
    fetch() { throw new Error('beta-ui must not fetch'); },
  };
  ctx.window = ctx;
  loadApiBase(ctx, deployment);
  vm.runInContext(SRC, ctx);
  return { ctx, doc, listeners };
}

console.log('beta-ui.js');

test('the embedded UI and an operator static build show no badge', () => {
  for (const channel of ['classic', 'static']) {
    const { doc } = load({ channel, apiBase: channel === 'static' ? 'https://api.example' : '', auth: false });
    assert.strictEqual(doc.getElementById('csChannelBadge'), null, channel);
    assert.strictEqual(doc.nav.children.length, 1, channel);
  }
});

test('beta, next and dev get a labelled badge right after the brand', () => {
  for (const [channel, label] of [['beta', 'Beta'], ['next', 'Next'], ['dev', 'Dev']]) {
    const { doc } = load({ channel, commit: 'abc1234', apiBase: 'https://api.example', auth: false });
    const badge = doc.getElementById('csChannelBadge');
    assert.ok(badge, channel);
    assert.strictEqual(badge.textContent, label);
    assert.strictEqual(badge.title, label + ' abc1234: report an issue with this page');
    assert.strictEqual(badge.target, '_blank');
    assert.strictEqual(badge.rel, 'noopener');
    assert.strictEqual(doc.nav.children[1].className, 'cs-channel-wrap');
  }
});

test('the badge opens a new issue with page, channel, commit and browser filled in, no template needed', () => {
  const { doc } = load({ channel: 'beta', commit: 'abc1234', apiBase: 'https://api.example', auth: false }, '#/nodes/ab?x=1');
  const url = new URL(doc.getElementById('csChannelBadge').href);
  assert.strictEqual(url.origin + url.pathname, 'https://github.com/Kpa-clawbot/CoreScope/issues/new');
  assert.strictEqual(url.searchParams.get('template'), null);
  assert.strictEqual(url.searchParams.get('title'), '[beta] ');
  const body = url.searchParams.get('body');
  assert.ok(body.startsWith('**Page:** #/nodes/ab?x=1\n'), body);
  assert.ok(body.includes('What happened on the beta frontend'), body);
  assert.ok(body.endsWith('Channel: beta · Commit: abc1234 · Browser: TestBrowser/1.0\n'), body);
});

test('account reports omit private query values even before a token is removed from the address bar', () => {
  for (const auth of [false, true]) {
    for (const route of ['#/account', '#/account/activate', '#/account/reset', '#/account/confirm-email', '#/account/unsubscribe']) {
      const hash = route + '?token=private-link-token&section=notifications';
      const { ctx, doc } = load({ channel: 'beta', apiBase: auth ? '' : 'https://api.example', auth,
        classicUrl: 'https://analyzer.example' }, hash);
      const badge = doc.getElementById('csChannelBadge');
      const body = new URL(badge.href).searchParams.get('body');
      assert.ok(body.startsWith('**Page:** ' + route + '\n'), body);
      assert.ok(!body.includes('private-link-token') && !body.includes('section='), body);
      assert.strictEqual(doc.getElementById('csClassicLink').href, 'https://analyzer.example/' + hash);
      assert.ok(ctx.CSBeta.classicOnlyHtml('Logging in').includes('token=private-link-token&amp;section=notifications'));

      // account.js uses replaceState, which does not emit hashchange. The
      // already-created badge must be safe without waiting for a refresh.
      ctx.location.hash = route;
      assert.ok(!new URL(badge.href).searchParams.get('body').includes('private-link-token'));
    }
  }
});

test('navigating to an account token link keeps the report private and the classic transfer intact', () => {
  const { ctx, doc, listeners } = load({ channel: 'beta', auth: false, classicUrl: 'https://analyzer.example' });
  ctx.location.hash = '#/account/reset?token=another-private-token';
  listeners.hashchange.forEach((fn) => fn());
  assert.ok(new URL(doc.getElementById('csChannelBadge').href).searchParams.get('body').startsWith('**Page:** #/account/reset\n'));
  assert.strictEqual(doc.getElementById('csClassicLink').href, 'https://analyzer.example/#/account/reset?token=another-private-token');
});

test('the classic link appears only with a classic URL, and both links follow the hash', () => {
  const none = load({ channel: 'beta', apiBase: 'https://api.example', auth: false });
  assert.strictEqual(none.doc.getElementById('csClassicLink'), null);

  const { ctx, doc, listeners } = load({ channel: 'beta', apiBase: 'https://api.example', auth: false, classicUrl: 'https://analyzer.example' });
  assert.strictEqual(doc.getElementById('csClassicLink').href, 'https://analyzer.example/#/packets');
  ctx.location.hash = '#/map';
  listeners.hashchange.forEach((fn) => fn());
  assert.strictEqual(doc.getElementById('csClassicLink').href, 'https://analyzer.example/#/map');
  assert.ok(new URL(doc.getElementById('csChannelBadge').href).searchParams.get('body').startsWith('**Page:** #/map\n'));
});

test('renderNav runs once', () => {
  const { ctx, doc } = load({ channel: 'beta', apiBase: 'https://api.example', auth: false });
  assert.strictEqual(ctx.CSBeta.renderNav(doc), false);
  assert.strictEqual(doc.nav.children.length, 2);
});

test('classicOnlyHtml names the feature, escapes it, and links to the same page on classic', () => {
  const { ctx } = load({ channel: 'beta', apiBase: 'https://api.example', auth: false, classicUrl: 'https://analyzer.example' }, '#/perf');
  assert.strictEqual(ctx.CSBeta.classicOnlyHtml('Resetting <stats>'),
    '<p class="cs-classic-only">Resetting &lt;stats&gt; is available on the classic site. <a class="cs-classic-link" href="https://analyzer.example/#/perf">Open in classic</a></p>');
});

test('classicOnlyHtml escapes the link too, and drops it without a classic URL', () => {
  const { ctx } = load({ channel: 'beta', apiBase: 'https://api.example', auth: false, classicUrl: 'https://analyzer.example' }, '#/x"><b>');
  assert.ok(ctx.CSBeta.classicOnlyHtml('Admin').includes('href="https://analyzer.example/#/x&quot;&gt;&lt;b&gt;"'));
  const bare = load({ channel: 'beta', apiBase: 'https://api.example', auth: false });
  assert.strictEqual(bare.ctx.CSBeta.classicOnlyHtml('Admin'), '<p class="cs-classic-only">Admin is available on the classic site.</p>');
});

console.log(`\nbeta-ui: ${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
