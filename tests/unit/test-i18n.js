/* Unit tests for public/i18n.js (UI translations) and public/locales/*.json.
 *
 * Runs the real i18n.js in a vm sandbox with a fake document, storage and
 * fetch, and checks every dictionary against the rules translators follow:
 * same {placeholders} as the English key, no markup, plural entries with an
 * "other" form, one file per language listed in I18n.LANGUAGES.
 *
 * Usage: node tests/unit/test-i18n.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const assert = require('assert');

const ROOT = path.join(__dirname, '..', '..');
const SRC = fs.readFileSync(path.join(ROOT, 'public', 'i18n.js'), 'utf8');
const LOCALES_DIR = path.join(ROOT, 'public', 'locales');

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  try {
    const r = fn();
    if (r && typeof r.then === 'function') {
      pending.push(r.then(() => { passed++; console.log('  ✅ ' + name); },
        (e) => { failed++; console.log('  ❌ ' + name + ': ' + e.message); }));
      return;
    }
    passed++; console.log('  ✅ ' + name);
  } catch (e) { failed++; console.log('  ❌ ' + name + ': ' + e.message); }
}

// Minimal element: attributes + textContent, enough for applyDom.
function el(attrs, text) {
  const a = Object.assign({}, attrs);
  return {
    textContent: text || '',
    getAttribute: (k) => (k in a ? a[k] : null),
    setAttribute: (k, v) => { a[k] = String(v); },
    attrs: a,
  };
}
function fakeRoot(elements) {
  return {
    querySelectorAll(sel) {
      const m = /^\[([\w-]+)\]$/.exec(sel);
      if (!m) throw new Error('unexpected selector ' + sel);
      return elements.filter(e => e.getAttribute(m[1]) !== null);
    },
  };
}

// load runs i18n.js once. opts: stored (localStorage), langs (navigator),
// dicts ({code: object} served by fetch), failFetch, readyState.
function load(opts) {
  opts = opts || {};
  const store = Object.assign({}, opts.stored);
  const htmlAttrs = {};
  const events = [];
  const fetched = [];
  const elements = opts.elements || [];
  let reloaded = 0;
  const dclListeners = [];
  const doc = Object.assign(fakeRoot(elements), {
    readyState: opts.readyState || 'complete',
    currentScript: { src: 'https://mesh.example/i18n.js?v=1700000000' },
    documentElement: { setAttribute: (k, v) => { htmlAttrs[k] = v; } },
    addEventListener: (type, fn) => { if (type === 'DOMContentLoaded') dclListeners.push(fn); },
  });
  const ctx = {
    console: { warn: () => {}, log: console.log, error: console.error },
    URL, Intl, Promise, Object, setTimeout, clearTimeout,
    Event: class { constructor(type) { this.type = type; } },
    CustomEvent: class { constructor(type, init) { this.type = type; this.detail = init && init.detail; } },
    document: doc,
    navigator: { languages: opts.langs || ['en-US'], language: (opts.langs || ['en-US'])[0] },
    location: { href: 'https://mesh.example/', reload: () => { reloaded++; } },
    localStorage: {
      getItem: (k) => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); },
      removeItem: (k) => { delete store[k]; },
    },
    fetch: (url) => {
      fetched.push(url);
      if (opts.failFetch) return Promise.reject(new Error('offline'));
      const code = /locales\/([\w-]+)\.json/.exec(url)[1];
      const body = (opts.dicts || {})[code];
      if (!body) return Promise.resolve({ ok: false, status: 404 });
      return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
    },
    dispatchEvent: (e) => { events.push(e.type); },
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(SRC, ctx);
  return {
    I18n: ctx.I18n, ctx, store, htmlAttrs, events, fetched, elements,
    fireDOMContentLoaded: () => dclListeners.splice(0).forEach(fn => fn()),
    reloads: () => reloaded,
  };
}

console.log('i18n.js — language detection');
test('defaults to English for an English browser', () => {
  const r = load({ langs: ['en-US', 'en'] });
  assert.strictEqual(r.I18n.lang, 'en');
  assert.strictEqual(r.fetched.length, 0, 'English needs no dictionary');
});
test('an exact browser language match picks it', () => {
  assert.strictEqual(load({ langs: ['pt-BR'] }).I18n.detect('auto', ['pt-BR']), 'pt-BR');
});
test('matching is case-insensitive and falls back to the primary subtag', () => {
  const { I18n } = load();
  assert.strictEqual(I18n.detect('auto', ['pt-br']), 'pt-BR');
  assert.strictEqual(I18n.detect('auto', ['pt-PT']), 'pt-BR');
  assert.strictEqual(I18n.detect('auto', ['pt']), 'pt-BR');
});
test('the first supported browser language wins', () => {
  const { I18n } = load();
  assert.strictEqual(I18n.detect('auto', ['xx-YY', 'pt-BR', 'en']), 'pt-BR');
  assert.strictEqual(I18n.detect('auto', ['en-GB', 'pt-BR']), 'en');
});
test('an unsupported browser language falls back to English', () => {
  assert.strictEqual(load().I18n.detect('auto', ['ja-JP']), 'en');
  assert.strictEqual(load().I18n.detect('auto', []), 'en');
});
test('a saved choice wins over the browser language', () => {
  const { I18n } = load();
  assert.strictEqual(I18n.detect('en', ['pt-BR']), 'en');
  assert.strictEqual(I18n.detect('pt-BR', ['en-US']), 'pt-BR');
});
test('an unknown saved choice is ignored', () => {
  assert.strictEqual(load().I18n.detect('klingon', ['pt-BR']), 'pt-BR');
});

console.log('i18n.js — tr / trn without a dictionary (English)');
test('tr returns the English text', () => {
  assert.strictEqual(load().I18n.tr('Packets'), 'Packets');
});
test('tr fills placeholders and leaves unknown ones', () => {
  const { I18n } = load();
  assert.strictEqual(I18n.tr('Last heard {time}', { time: '3m ago' }), 'Last heard 3m ago');
  assert.strictEqual(I18n.tr('Hello {who}', {}), 'Hello {who}');
  assert.strictEqual(I18n.tr('{a}{a}', { a: 1 }), '11');
});
test('trn picks the English singular or plural', () => {
  const { I18n } = load();
  assert.strictEqual(I18n.trn(1, '{count} packet', '{count} packets'), '1 packet');
  assert.strictEqual(I18n.trn(0, '{count} packet', '{count} packets'), '0 packets');
  assert.strictEqual(I18n.trn(5, '{count} packet', '{count} packets'), '5 packets');
});
test('tr and trn are installed as globals', () => {
  const r = load();
  assert.strictEqual(typeof r.ctx.tr, 'function');
  assert.strictEqual(typeof r.ctx.trn, 'function');
});

console.log('i18n.js — loading a dictionary');
const DICT = {
  'Packets': 'Pacotes',
  'Last heard {time}': 'Ouvido por último {time}',
  '{count} packets': { one: '{count} pacote', other: '{count} pacotes' },
  'Empty': '',
};
test('fetches the dictionary next to the script with its cache buster', () => {
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT } });
  return r.I18n.ready.then(() => {
    assert.deepStrictEqual(r.fetched, ['https://mesh.example/locales/pt-BR.json?v=1700000000']);
  });
});
test('translates, sets <html lang> and fires i18n-ready', () => {
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT } });
  return r.I18n.ready.then((code) => {
    assert.strictEqual(code, 'pt-BR');
    assert.strictEqual(r.I18n.lang, 'pt-BR');
    assert.strictEqual(r.htmlAttrs.lang, 'pt-BR');
    assert.strictEqual(r.I18n.tr('Packets'), 'Pacotes');
    assert.strictEqual(r.I18n.tr('Last heard {time}', { time: 'há 3m' }), 'Ouvido por último há 3m');
    assert.ok(r.events.includes('i18n-ready'));
  });
});
test('missing and empty entries fall back to English', () => {
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT } });
  return r.I18n.ready.then(() => {
    assert.strictEqual(r.I18n.tr('Channels'), 'Channels');
    assert.strictEqual(r.I18n.tr('Empty'), 'Empty');
  });
});
test('plural entries use the language plural rules (pt: 0 and 1 are "one")', () => {
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT } });
  return r.I18n.ready.then(() => {
    assert.strictEqual(r.I18n.trn(1, '{count} packet', '{count} packets'), '1 pacote');
    assert.strictEqual(r.I18n.trn(0, '{count} packet', '{count} packets'), '0 pacote');
    assert.strictEqual(r.I18n.trn(7, '{count} packet', '{count} packets'), '7 pacotes');
  });
});
test('a failed fetch falls back to English and still resolves ready', () => {
  const r = load({ langs: ['pt-BR'], failFetch: true });
  return r.I18n.ready.then((code) => {
    assert.strictEqual(code, 'en');
    assert.strictEqual(r.htmlAttrs.lang, 'en');
    assert.strictEqual(r.I18n.tr('Packets'), 'Packets');
  });
});
test('a 404 dictionary falls back to English', () => {
  const r = load({ langs: ['pt-BR'], dicts: {} });
  return r.I18n.ready.then((code) => assert.strictEqual(code, 'en'));
});
test('static markup is translated once the dictionary is in', () => {
  const elements = [
    el({ 'data-i18n': 'Packets' }, 'Packets'),
    el({ 'data-i18n-title': 'Packets', 'data-i18n-aria-label': 'Packets', title: 'Packets' }),
    el({ 'data-i18n-placeholder': 'Channels' }),
  ];
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT }, elements });
  return r.I18n.ready.then(() => {
    assert.strictEqual(elements[0].textContent, 'Pacotes');
    assert.strictEqual(elements[1].attrs.title, 'Pacotes');
    assert.strictEqual(elements[1].attrs['aria-label'], 'Pacotes');
    assert.strictEqual(elements[2].attrs.placeholder, 'Channels', 'untranslated stays English');
    assert.ok(r.events.includes('resize'), 'nav re-measures translated labels');
  });
});
test('while the page is parsing, markup waits for DOMContentLoaded', () => {
  const elements = [el({ 'data-i18n': 'Packets' }, 'Packets')];
  const r = load({ langs: ['pt-BR'], dicts: { 'pt-BR': DICT }, elements, readyState: 'loading' });
  let resolved = false;
  r.I18n.ready.then(() => { resolved = true; });
  return new Promise(res => setTimeout(res, 20)).then(() => {
    assert.strictEqual(resolved, false, 'ready waits for the body');
    assert.strictEqual(elements[0].textContent, 'Packets');
    r.fireDOMContentLoaded();
    return r.I18n.ready;
  }).then(() => {
    assert.strictEqual(elements[0].textContent, 'Pacotes');
  });
});

console.log('i18n.js — preference');
test('setPreference saves a language and reloads', () => {
  const r = load();
  r.I18n.setPreference('pt-br');
  assert.strictEqual(r.store['meshcore-lang'], 'pt-BR');
  assert.strictEqual(r.reloads(), 1);
});
test('setPreference("auto") clears the saved choice', () => {
  const r = load({ stored: { 'meshcore-lang': 'pt-BR' } });
  assert.strictEqual(r.I18n.getPreference(), 'pt-BR');
  r.I18n.setPreference('auto');
  assert.strictEqual(r.store['meshcore-lang'], undefined);
  assert.strictEqual(r.I18n.getPreference(), 'auto');
});
test('setPreference ignores an unknown language', () => {
  const r = load();
  r.I18n.setPreference('klingon');
  assert.strictEqual(r.store['meshcore-lang'], undefined);
  assert.strictEqual(r.reloads(), 0);
});
test('a saved choice selects the dictionary to load', () => {
  const r = load({ stored: { 'meshcore-lang': 'pt-BR' }, langs: ['en-US'], dicts: { 'pt-BR': DICT } });
  return r.I18n.ready.then((code) => assert.strictEqual(code, 'pt-BR'));
});

console.log('locales/*.json — dictionary rules');
const placeholders = (s) => (s.match(/\{\w+\}/g) || []).slice().sort().join(',');
const tags = (s) => (s.match(/<\/?[a-z][^>]*>/gi) || []).length;
// Links in markdown defaults ([text](url)) must point where the English does.
const urls = (s) => (s.match(/https?:\/\/[^\s)]+/g) || []).slice().sort().join(' ');
const files = fs.readdirSync(LOCALES_DIR).filter(f => f.endsWith('.json'));
// Copied out of the sandbox realm so deepStrictEqual compares plain arrays.
const codes = Array.from(load().I18n.LANGUAGES, l => String(l.code));
test('every listed language except English has a dictionary, and vice versa', () => {
  const expected = codes.filter(c => c !== 'en').map(c => c + '.json').sort();
  assert.deepStrictEqual(files.slice().sort(), expected);
});
files.forEach((file) => {
  const dict = JSON.parse(fs.readFileSync(path.join(LOCALES_DIR, file), 'utf8'));
  test(file + ': every entry keeps the key\'s placeholders and links, and adds no markup', () => {
    const problems = [];
    Object.entries(dict).forEach(([key, value]) => {
      const forms = typeof value === 'string' ? [value] : Object.values(value || {});
      if (typeof value !== 'string') {
        if (!value || typeof value.other !== 'string') problems.push(key + ': plural entry needs "other"');
      }
      forms.forEach((form) => {
        if (typeof form !== 'string') { problems.push(key + ': not a string'); return; }
        if (!form) return; // empty = untranslated, falls back to English
        if (placeholders(form) !== placeholders(key)) problems.push(key + ': placeholders differ');
        if (tags(form) > tags(key)) problems.push(key + ': adds markup');
        if (urls(form) !== urls(key)) problems.push(key + ': links differ');
      });
    });
    assert.deepStrictEqual(problems, []);
  });
});

Promise.all(pending).then(() => {
  console.log('\n' + passed + ' passed, ' + failed + ' failed');
  if (failed) process.exit(1);
});
