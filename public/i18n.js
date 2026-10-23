/* === CoreScope — i18n.js (UI translations) ===
 *
 * Runtime-only translation layer: no build step, no library.
 *
 *   tr('Packets')                         -> "Pacotes"
 *   tr('Last heard {time}', { time: t })  -> "Visto por último {time}" with {time} filled
 *   trn(n, '{count} packet', '{count} packets')
 *
 * The English text is the key. A string missing from the active dictionary
 * falls back to English, so an untranslated or changed string shows up in
 * English instead of breaking or showing a stale translation.
 *
 * Dictionaries live in public/locales/<code>.json: a flat object of
 * "English text": "translation". A plural entry is keyed by the English
 * plural form and holds the CLDR categories of the target language:
 *   "{count} packets": { "one": "{count} pacote", "other": "{count} pacotes" }
 *
 * Placeholder values are inserted as given: escape node-controlled values
 * (escapeHtml) before passing them when the result goes into innerHTML.
 *
 * Static markup is translated through attributes, applied once the
 * dictionary is loaded (and by applyDom(root) for markup built later):
 *   data-i18n="Text"             -> textContent
 *   data-i18n-title="Text"       -> title
 *   data-i18n-aria-label="Text"  -> aria-label
 *   data-i18n-placeholder="Text" -> placeholder
 *
 * The language is the saved choice (localStorage meshcore-lang), else the
 * first browser language with a dictionary, else English. app.js waits for
 * I18n.ready before the first route renders; changing the language reloads
 * the page.
 *
 * To add a language: add public/locales/<code>.json and one LANGUAGES entry.
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'meshcore-lang';
  var READY_TIMEOUT_MS = 3000;
  // `name` is shown in the language picker, in the language itself.
  var LANGUAGES = [
    { code: 'en', name: 'English' },
    { code: 'pt-BR', name: 'Português (Brasil)' }
  ];
  var ATTRS = ['title', 'aria-label', 'placeholder'];

  function canonical(code) {
    if (typeof code !== 'string') return null;
    var lc = code.toLowerCase();
    for (var i = 0; i < LANGUAGES.length; i++) {
      if (LANGUAGES[i].code.toLowerCase() === lc) return LANGUAGES[i].code;
    }
    return null;
  }

  // detect resolves a saved preference ('auto' or a code) and the browser's
  // language list to a supported code. A browser language matches exactly
  // first, then by its primary subtag (pt-PT, pt -> pt-BR).
  function detect(pref, browserLangs) {
    if (pref && pref !== 'auto') {
      var saved = canonical(pref);
      if (saved) return saved;
    }
    var list = (browserLangs || []).filter(function (l) { return typeof l === 'string' && l; });
    for (var i = 0; i < list.length; i++) {
      var exact = canonical(list[i]);
      if (exact) return exact;
      var primary = list[i].split('-')[0].toLowerCase();
      for (var j = 0; j < LANGUAGES.length; j++) {
        if (LANGUAGES[j].code.split('-')[0].toLowerCase() === primary) return LANGUAGES[j].code;
      }
    }
    return 'en';
  }

  function format(text, params) {
    if (!params) return text;
    return text.replace(/\{(\w+)\}/g, function (m, k) {
      return Object.prototype.hasOwnProperty.call(params, k) ? String(params[k]) : m;
    });
  }

  var dict = {};
  var pluralRules = null;

  function tr(text, params) {
    var v = dict[text];
    return format(typeof v === 'string' && v ? v : text, params);
  }

  function trn(count, singular, plural, params) {
    var p = Object.assign({ count: count }, params);
    var v = dict[plural];
    if (v && typeof v === 'object') {
      var cat = pluralRules ? pluralRules.select(count) : (count === 1 ? 'one' : 'other');
      var s = v[cat] || v.other;
      if (typeof s === 'string' && s) return format(s, p);
    }
    return format(count === 1 ? singular : plural, p);
  }

  function applyDom(root) {
    if (!root || typeof root.querySelectorAll !== 'function') return;
    root.querySelectorAll('[data-i18n]').forEach(function (el) {
      el.textContent = tr(el.getAttribute('data-i18n'));
    });
    ATTRS.forEach(function (attr) {
      root.querySelectorAll('[data-i18n-' + attr + ']').forEach(function (el) {
        el.setAttribute(attr, tr(el.getAttribute('data-i18n-' + attr)));
      });
    });
  }

  function readPref() {
    try { return localStorage.getItem(STORAGE_KEY) || 'auto'; } catch (_) { return 'auto'; }
  }

  // setPreference saves 'auto' or a language code and reloads, so every
  // page and widget renders in the new language without per-module wiring.
  function setPreference(code) {
    var c = code === 'auto' ? 'auto' : canonical(code);
    if (!c) return;
    try {
      if (c === 'auto') localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, c);
    } catch (_) {}
    location.reload();
  }

  var browserLangs = (typeof navigator !== 'undefined')
    ? (navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language])
    : [];
  var lang = detect(readPref(), browserLangs);

  var resolveReady;
  var ready = new Promise(function (resolve) { resolveReady = resolve; });
  var settled = false;

  // settle installs the dictionary (or none: English) once, translates the
  // static markup when the body exists, and releases app.js's first render.
  function settle(code, data) {
    if (settled) return;
    settled = true;
    if (data && typeof data === 'object') dict = data;
    else code = 'en';
    lang = code;
    I18n.lang = code;
    try { pluralRules = new Intl.PluralRules(code); } catch (_) { pluralRules = null; }
    document.documentElement.setAttribute('lang', code);
    function finish() {
      if (code !== 'en') {
        applyDom(document);
        // Translated nav labels change width: let the nav overflow logic re-measure.
        window.dispatchEvent(new Event('resize'));
      }
      resolveReady(code);
      window.dispatchEvent(new CustomEvent('i18n-ready', { detail: { lang: code } }));
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', finish, { once: true });
    else finish();
  }

  // The dictionary URL is resolved next to this script and carries its
  // cache buster, so a deploy invalidates both together.
  function dictUrl(code) {
    var src = (document.currentScript && document.currentScript.src) || location.href;
    var u = new URL('locales/' + code + '.json', src);
    var v = new URL(src).searchParams.get('v');
    if (v) u.searchParams.set('v', v);
    return u.toString();
  }

  var I18n = {
    LANGUAGES: LANGUAGES,
    STORAGE_KEY: STORAGE_KEY,
    lang: lang,
    ready: ready,
    tr: tr,
    trn: trn,
    applyDom: applyDom,
    detect: detect,
    getPreference: function () { var p = readPref(); return p === 'auto' ? 'auto' : (canonical(p) || 'auto'); },
    setPreference: setPreference
  };
  window.I18n = I18n;
  window.tr = tr;
  window.trn = trn;

  if (lang === 'en') {
    settle('en', null);
  } else {
    var url = dictUrl(lang);
    var timer = setTimeout(function () {
      console.warn('[i18n] ' + lang + ' dictionary not loaded in time; using English');
      settle('en', null);
    }, READY_TIMEOUT_MS);
    fetch(url).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (data) {
      clearTimeout(timer);
      settle(lang, data);
    }).catch(function (e) {
      clearTimeout(timer);
      console.warn('[i18n] could not load ' + url + ': ' + e.message + '; using English');
      settle('en', null);
    });
  }
})();
