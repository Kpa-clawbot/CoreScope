/* Settings sync for optional user management
 * (docs/specs/2026-10-06-user-settings-sync-design.md). Inert unless
 * window.MC_USER_MGMT is on and CSAuth reports a logged-in user. While
 * active it wraps Storage.prototype.setItem/removeItem for the keys the
 * server allowlists, merges this device with the account's copy and
 * pushes changes. Exposes window.CSSettingsSync. */
(function () {
  'use strict';

  // ── Three-way merge (pure) ──
  // Values are raw localStorage strings; undefined means "not set".

  function own(o, k) { return o && Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined; }

  // parseList returns the array behind raw ([] when unset), or null when
  // raw is not a JSON array.
  function parseList(raw) {
    if (raw === undefined) return [];
    try {
      var v = JSON.parse(raw);
      return Array.isArray(v) ? v : null;
    } catch (e) { return null; }
  }

  // identity of a set item: its idField when it is an object that has one,
  // else its JSON (a string item is compared as itself).
  function identity(item, idField) {
    if (idField && item && typeof item === 'object' && item[idField] != null) return 'f:' + String(item[idField]);
    return 'j:' + JSON.stringify(item);
  }

  function indexList(list, idField) {
    var map = Object.create(null), order = [];
    list.forEach(function (item) {
      var id = identity(item, idField);
      if (id in map) return;
      map[id] = item;
      order.push(id);
    });
    return { map: map, order: order };
  }

  function sameItem(a, b) { return JSON.stringify(a) === JSON.stringify(b); }

  // mergeSet computes P + (L - B) - (B - L) by identity. Returns the merged
  // raw string, undefined when neither side has the key, or null when this
  // device or the account holds something that is not a JSON list (the
  // caller then merges it as a scalar). An unparseable baseline counts as
  // none, so nothing added on either side is dropped.
  function mergeSet(l, p, b, idField) {
    var al = parseList(l), ap = parseList(p), ab = parseList(b) || [];
    if (!al || !ap) return null;
    var L = indexList(al, idField), P = indexList(ap, idField), B = indexList(ab, idField);
    var out = [];
    P.order.forEach(function (id) {
      var inL = id in L.map, inB = id in B.map;
      if (inB && !inL) return; // removed on this device since the last sync
      var onlyLocalChanged = inL && inB && !sameItem(L.map[id], B.map[id]) && sameItem(P.map[id], B.map[id]);
      out.push(onlyLocalChanged ? L.map[id] : P.map[id]);
    });
    L.order.forEach(function (id) {
      if (id in P.map || id in B.map) return; // in the profile, or removed on another device
      out.push(L.map[id]); // added on this device since the last sync
    });
    if (!out.length && l === undefined && p === undefined) return undefined;
    var s = JSON.stringify(out);
    // Keep an existing spelling of the same list: formatting alone is no
    // change. The account's spelling goes first, so a list equal to both
    // sides does not count as differing from the account (two devices
    // would otherwise push their spelling back and forth).
    if (p !== undefined && JSON.stringify(ap) === s) return p;
    if (l !== undefined && JSON.stringify(al) === s) return l;
    return s;
  }

  function mergeScalar(l, p, b) { return (l !== b && p === b) ? l : p; }

  // mergeDocs merges local, profile and baseline ({key: raw}) for every
  // allowlisted key. localChanges lists the keys this device must write;
  // differsFromProfile says the result must be pushed.
  function mergeDocs(local, profile, base, allowlist) {
    var keys = {}, localChanges = [], differs = false;
    allowlist.forEach(function (entry) {
      var k = entry.key, l = own(local, k), p = own(profile, k), b = own(base, k), v;
      if (entry.kind === 'set') {
        v = mergeSet(l, p, b, entry.id || '');
        if (v === null) {
          console.warn('[settings-sync] ' + k + ' is not a JSON list on this device or in the account; merged as one value');
          v = mergeScalar(l, p, b);
        }
      } else {
        v = mergeScalar(l, p, b);
      }
      if (v !== undefined) keys[k] = v;
      if (v !== l) localChanges.push(k);
      if (v !== p) differs = true;
    });
    return { keys: keys, localChanges: localChanges, differsFromProfile: differs };
  }

  // ── Sync engine ──

  var BASE_KEY = 'cs-settings-sync-base';
  var REV_KEY = 'cs-settings-sync-rev';
  var PUSH_DELAY_MS = 2000;
  var PULL_EVERY_MS = 60000;
  var BACKOFF_MIN_MS = 2000;
  var BACKOFF_MAX_MS = 300000;
  var MAX_CONFLICT_RETRIES = 3;
  // Keys whose change an existing storage listener applies (app.js,
  // cb-presets.js, map-tile-providers.js).
  var LISTENER_KEYS = {
    'meshcore-theme': true, 'meshcore-cb-preset': true,
    'mc-dark-tile-provider': true, 'mc-light-tile-provider': true
  };

  // Assigning localStorage.setItem would store a key named "setItem"
  // (Storage has a named-property setter), so the wrap goes on the prototype.
  var proto = window.Storage.prototype;
  var origGet = proto.getItem, origSet = proto.setItem, origRemove = proto.removeItem;

  var state = {
    active: false, userId: null, policy: null,
    base: {}, rev: 0, gen: '', hold: false, firstUpload: false,
    dirty: false, seq: 0, pushing: null, pushTimer: null, retryTimer: null, pullTimer: null,
    backoff: BACKOFF_MIN_MS, blocked: null, status: 'idle', lastSyncedAt: null, tooLarge: [],
    // epoch changes on every activate/deactivate: an answer to a request
    // from an earlier session is dropped. putsDone counts PUT answers, so a
    // pull can tell a push finished while its GET was out.
    epoch: 0, putsDone: 0
  };

  function rawGet(k) { var v = origGet.call(window.localStorage, k); return v === null ? undefined : v; }
  function rawSet(k, v) { origSet.call(window.localStorage, k, v); }
  function rawRemove(k) { origRemove.call(window.localStorage, k); }

  function setPolicy(list) {
    var byKey = Object.create(null);
    list.forEach(function (e) { byKey[e.key] = e; });
    state.policy = { list: list, byKey: byKey };
  }

  // pick keeps the allowlisted keys of o.
  function pick(o) {
    var out = {};
    state.policy.list.forEach(function (e) { var v = own(o, e.key); if (v !== undefined) out[e.key] = v; });
    return out;
  }

  function snapshot() {
    var out = {};
    state.policy.list.forEach(function (e) { var v = rawGet(e.key); if (v !== undefined) out[e.key] = v; });
    return out;
  }

  function sameKeys(a, b) {
    var ka = Object.keys(a);
    return ka.length === Object.keys(b).length && ka.every(function (k) { return own(b, k) === a[k]; });
  }

  // The baseline is the last document this device and the account agreed
  // on. It belongs to one user: another user's baseline counts as none. gen
  // is the account document's generation: revisions restart at 1 after a
  // delete, so a revision alone does not say which document it belongs to.
  function loadBase(userId) {
    try {
      var b = JSON.parse(rawGet(BASE_KEY) || 'null');
      if (b && b.user === userId && b.keys && typeof b.keys === 'object') {
        return { keys: b.keys, rev: Number(rawGet(REV_KEY)) || 0, gen: typeof b.gen === 'string' ? b.gen : '', hold: !!b.hold };
      }
    } catch (e) { /* a damaged baseline counts as none */ }
    return { keys: {}, rev: 0, gen: '', hold: false };
  }

  function saveBase(keys, rev, gen, hold) {
    state.base = keys;
    state.rev = rev;
    state.gen = gen;
    state.hold = hold;
    rawSet(BASE_KEY, JSON.stringify({ user: state.userId, keys: keys, gen: gen, hold: hold }));
    rawSet(REV_KEY, String(rev));
  }

  function setStatus(s) { state.status = s; renderStatus(); }

  function watched(storage, k) {
    return state.active && !!state.policy && storage === window.localStorage && !!state.policy.byKey[k];
  }

  function install() {
    proto.setItem = function (k, v) {
      var watch = watched(this, k), before = watch ? origGet.call(this, k) : null;
      origSet.call(this, k, v);
      if (watch && before !== String(v)) markDirty();
    };
    proto.removeItem = function (k) {
      var watch = watched(this, k), before = watch ? origGet.call(this, k) : null;
      origRemove.call(this, k);
      if (watch && before !== null) markDirty();
    };
  }

  function uninstall() {
    proto.setItem = origSet;
    proto.removeItem = origRemove;
  }

  function markDirty() {
    state.dirty = true;
    state.seq++;
    if (state.hold) saveBase(state.base, state.rev, state.gen, false); // the next change starts a new document
    schedulePush(PUSH_DELAY_MS);
  }

  function schedulePush(ms) {
    clearTimeout(state.pushTimer);
    state.pushTimer = setTimeout(function () { state.pushTimer = null; push(); }, ms);
  }

  function retryLater() {
    setStatus('retrying');
    clearTimeout(state.retryTimer);
    var wait = state.backoff;
    state.backoff = Math.min(state.backoff * 2, BACKOFF_MAX_MS);
    state.retryTimer = setTimeout(function () { state.retryTimer = null; push(); }, wait);
  }

  function synced(seq) {
    state.backoff = BACKOFF_MIN_MS;
    state.lastSyncedAt = new Date();
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    if (state.seq === seq) state.dirty = false;
    else schedulePush(PUSH_DELAY_MS); // written again while the request was out
    setStatus('ok');
  }

  function largestKeys(keys) {
    return Object.keys(keys).sort(function (a, b) { return keys[b].length - keys[a].length; }).slice(0, 3);
  }

  // midEdit: re-rendering now would throw away unsaved input. Account
  // pages hold forms; the geofilter editor (customize-v2.js) is a modal
  // over the page. The page the user opens next reads the new values.
  function midEdit() {
    return /^#\/account(\/|\?|$)/.test(location.hash) || !!document.getElementById('cv2-gf-modal-overlay');
  }

  // afterRemoteChange makes the running page show values that arrived from
  // the account: storage listeners for theme, colour-blind preset and map
  // tiles, the customizer pipeline for its overrides, and a router
  // re-render for everything a page reads at init. Before the customizer
  // finished its init the pipeline is skipped: it would render without the
  // server defaults, and the init reads the new overrides itself.
  function afterRemoteChange(changed) {
    changed.forEach(function (k) {
      if (!LISTENER_KEYS[k]) return;
      var v = rawGet(k);
      window.dispatchEvent(new StorageEvent('storage', { key: k, newValue: v === undefined ? null : v }));
    });
    var cz = window._customizerV2;
    if (changed.indexOf('cs-theme-overrides') !== -1 && cz && cz.initDone) cz.runPipeline();
    window.CSAuth.notify('Settings updated from another device');
    if (!midEdit()) window.navigate();
  }

  function writeLocal(keys, changed) {
    changed.forEach(function (k) {
      var v = own(keys, k);
      try {
        if (v === undefined) rawRemove(k); else rawSet(k, v);
      } catch (e) { console.warn('[settings-sync] could not store ' + k + ': ' + e.message); }
    });
  }

  // applyProfile brings the account's document into this device: merge,
  // write what changed, make it the new baseline. Returns true when this
  // device holds values the account lacks. A baseline of another generation
  // (the copy was deleted and started again) counts as none, so nothing on
  // this device is taken for a removal made elsewhere.
  function applyProfile(rev, gen, doc) {
    var local = snapshot();
    if (!rev) {
      if (state.rev > 0 || state.hold) {
        // The account's copy was deleted, here or on another device. Keep
        // this device as it is; its next change starts a new document.
        saveBase({}, 0, '', true);
        return false;
      }
      state.firstUpload = true; // first login: this device's values form the first document
      return Object.keys(local).length > 0;
    }
    var profile = pick((doc && doc.keys) || {});
    var m = mergeDocs(local, profile, state.gen === gen ? state.base : {}, state.policy.list);
    writeLocal(m.keys, m.localChanges);
    saveBase(profile, rev, gen, false);
    if (m.localChanges.length) afterRemoteChange(m.localChanges);
    return m.differsFromProfile;
  }

  // push sends this device's allowlisted values. A 409 merges the returned
  // document and retries (at most MAX_CONFLICT_RETRIES); network errors,
  // 5xx and 429 retry with backoff. Resolves true when the account holds
  // this device's values.
  function push(attempt) {
    if (!state.active || state.blocked || !state.policy) return Promise.resolve(false);
    if (state.pushing) return state.pushing.then(function () { return state.dirty ? push() : true; });
    attempt = attempt || 0;
    var keys = snapshot(), seq = state.seq, epoch = state.epoch;
    if (state.rev > 0 && sameKeys(keys, state.base)) { synced(seq); return Promise.resolve(true); }
    setStatus('syncing');
    var p = window.CSAuth.request('PUT', '/api/account/settings', { baseRevision: state.rev, baseGeneration: state.gen, doc: { v: 1, keys: keys } })
      .then(function (r) {
        if (state.epoch !== epoch) return false; // logged out (and maybe in again) meanwhile
        state.pushing = null;
        state.putsDone++;
        if (r.ok) {
          saveBase(keys, r.data.revision, r.data.generation, false);
          synced(seq);
          if (state.firstUpload) {
            state.firstUpload = false;
            window.CSAuth.notify('Your settings are now saved to your account.');
          }
          return true;
        }
        if (r.status === 409 && attempt < MAX_CONFLICT_RETRIES) {
          applyProfile(r.data.revision, r.data.generation, r.data.doc);
          return push(attempt + 1);
        }
        if (r.status === 413) {
          state.blocked = 'too-large';
          state.tooLarge = largestKeys(keys);
          setStatus('too-large');
          return false;
        }
        if (r.status === 400) {
          console.error('[settings-sync] the server refused the settings: ' + (r.data && r.data.error));
          state.blocked = 'rejected';
          setStatus('rejected');
          return false;
        }
        if (r.status !== 401) retryLater(); // 401: auth.js logged out, which deactivates this module
        return false;
      }, function () {
        if (state.epoch !== epoch) return false;
        state.pushing = null;
        retryLater();
        return false;
      });
    state.pushing = p;
    return p;
  }

  // pull fetches the account's document. An answer is dropped when the
  // session changed or a push finished (or the baseline moved) while the
  // GET was out: it describes the account before that push, and applying
  // it would revert this device's change or mistake the first upload's
  // revision-0 answer for a deleted copy. The next pull catches up.
  function pull() {
    if (!state.active) return Promise.resolve();
    if (state.pushing) return state.pushing.then(pull);
    var epoch = state.epoch, puts = state.putsDone, rev = state.rev;
    var stale = function () { return state.epoch !== epoch || state.putsDone !== puts || state.rev !== rev; };
    return window.CSAuth.request('GET', '/api/account/settings').then(function (r) {
      if (stale()) return;
      if (!r.ok) { if (r.status !== 401) setStatus('retrying'); return; }
      setPolicy(r.data.allowlist || []);
      if (applyProfile(r.data.revision, r.data.generation, r.data.doc)) {
        state.dirty = true;
        state.seq++;
        return push();
      }
      if (state.dirty) return push();
      if (r.data.revision) { state.lastSyncedAt = new Date(); setStatus('ok'); }
      else setStatus(state.hold ? 'held' : 'idle');
    }, function () { if (!stale()) setStatus('retrying'); });
  }

  // syncNow: the account page button. Retries after a 413 (the user may
  // have trimmed), never after a 400 (that needs a reload).
  function syncNow() {
    if (!state.active) return Promise.resolve();
    if (state.blocked === 'too-large') state.blocked = null;
    state.backoff = BACKOFF_MIN_MS;
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    return pull();
  }

  function onVisible() {
    if (document.visibilityState !== 'visible') return;
    clearTimeout(state.retryTimer);
    state.retryTimer = null;
    pull();
  }

  function activate(user) {
    if (state.active && state.userId === user.id) return Promise.resolve();
    if (state.active) deactivate();
    var b = loadBase(user.id);
    state.epoch++;
    state.active = true;
    state.userId = user.id;
    state.policy = null;
    state.base = b.keys;
    state.rev = b.rev;
    state.gen = b.gen;
    state.hold = b.hold;
    state.firstUpload = false;
    state.dirty = false;
    state.blocked = null;
    state.backoff = BACKOFF_MIN_MS;
    install();
    window.CSAuth.setLogoutHandler(onLogout);
    document.addEventListener('visibilitychange', onVisible);
    state.pullTimer = setInterval(function () { if (document.visibilityState === 'visible') pull(); }, PULL_EVERY_MS);
    return pull();
  }

  function deactivate() {
    state.active = false;
    state.epoch++;
    clearTimeout(state.pushTimer);
    clearTimeout(state.retryTimer);
    clearInterval(state.pullTimer);
    state.pushTimer = state.retryTimer = state.pullTimer = null;
    document.removeEventListener('visibilitychange', onVisible);
    uninstall();
    window.CSAuth.setLogoutHandler(null);
    state.policy = null;
    state.userId = null;
    state.pushing = null;
    setStatus('idle');
  }

  // ── Dialog, logout and the account-page section ──

  // showDialog uses the app's modal pattern (.modal-overlay + .modal, as
  // the BYOP dialog in packets.js): role=dialog, focus on the choice
  // opts.focus names (default the first), Tab trapped, Escape or a
  // backdrop click dismiss. Resolves with the chosen id, or null when
  // dismissed.
  function showDialog(opts) {
    return new Promise(function (resolve) {
      var prev = document.activeElement;
      var overlay = document.createElement('div');
      overlay.className = 'modal-overlay cs-dialog-overlay';
      overlay.innerHTML = '<div class="modal cs-dialog" role="dialog" aria-modal="true" aria-labelledby="csDialogTitle" aria-describedby="csDialogText">' +
        '<h3 id="csDialogTitle">' + escapeHtml(opts.title) + '</h3>' +
        '<div id="csDialogText">' + opts.text.map(function (t) { return '<p class="account-hint">' + escapeHtml(t) + '</p>'; }).join('') + '</div>' +
        '<div class="cs-dialog-actions">' + opts.choices.map(function (c) {
          return '<button type="button" class="account-btn ' + (c.primary ? 'account-btn-primary' : 'account-btn-secondary') +
            '" data-choice="' + escapeHtml(c.id) + '">' + escapeHtml(c.label) + '</button>';
        }).join('') + '</div></div>';
      document.body.appendChild(overlay);
      var buttons = overlay.querySelectorAll('button');
      function close(choice) {
        overlay.remove();
        if (prev && prev.focus) prev.focus();
        resolve(choice);
      }
      overlay.addEventListener('keydown', function (e) {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(null); return; }
        if (e.key !== 'Tab') return;
        var first = buttons[0], last = buttons[buttons.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      });
      overlay.addEventListener('click', function (e) {
        var btn = e.target.closest && e.target.closest('[data-choice]');
        if (btn) close(btn.getAttribute('data-choice'));
        else if (e.target === overlay) close(null);
      });
      var start = buttons[0];
      for (var i = 0; i < opts.choices.length; i++) if (opts.choices[i].id === opts.focus) start = buttons[i];
      start.focus();
    });
  }
  var dialog = showDialog;

  // flush pushes pending changes now. Resolves true when the account holds
  // everything this device has.
  function flush() {
    clearTimeout(state.pushTimer);
    state.pushTimer = null;
    if (!state.dirty) return Promise.resolve(true);
    return push().then(function (ok) { return ok && !state.dirty; });
  }

  // removeLocal deletes the synced keys in list and the baseline. Channel
  // keys and the API key are never on the allowlist, so they stay.
  function removeLocal(list) {
    list.forEach(function (e) { rawRemove(e.key); });
    rawRemove(BASE_KEY);
    rawRemove(REV_KEY);
  }

  var LOGOUT_TEXT = [
    'Your settings stay saved in your account.',
    'Channel keys are never synced and stay on this device. They are not removed, because no copy exists anywhere else.'
  ];

  // onLogout is CSAuth's logout handler while active (see auth.js logout).
  function onLogout() {
    return dialog({
      title: 'Log out',
      text: LOGOUT_TEXT,
      choices: [
        { id: 'keep', label: 'Keep my settings on this device', primary: true },
        { id: 'remove', label: 'Remove my settings from this device' },
        { id: 'cancel', label: 'Cancel' }
      ]
    }).then(function (choice) {
      if (choice !== 'keep' && choice !== 'remove') return { cancel: true };
      return flush().then(function (saved) {
        if (choice === 'keep') return {};
        if (!saved || !state.policy) {
          // Removing now would lose changes that exist nowhere else.
          return { afterLogout: function () { window.CSAuth.notify('Your latest settings could not be saved to your account, so they stay on this device.'); } };
        }
        var list = state.policy.list;
        return { afterLogout: function () { removeLocal(list); } };
      });
    });
  }

  // deleteRemote removes the account's copy; this device keeps its values
  // and its next change starts a new document. A push in flight is waited
  // for and pending pushes are cancelled first, so no PUT (or its late
  // answer) recreates the copy or clears the hold. When the DELETE fails,
  // unsynced changes go back to the retry backoff.
  function stopPushing() {
    clearTimeout(state.pushTimer);
    clearTimeout(state.retryTimer);
    state.pushTimer = state.retryTimer = null;
  }

  function deleteRemote() {
    var retryIfDirty = function () { if (state.active && state.dirty) retryLater(); };
    return Promise.resolve(state.pushing).then(function () {
      stopPushing();
      return window.CSAuth.request('DELETE', '/api/account/settings');
    }).then(function (r) {
      if (r.ok) {
        stopPushing();
        state.dirty = false;
        saveBase({}, 0, '', true);
        setStatus('held');
      } else retryIfDirty();
      return r;
    }, function (e) { retryIfDirty(); throw e; });
  }

  function statusText() {
    switch (state.status) {
      case 'ok': return 'Last synced ' + state.lastSyncedAt.toLocaleString();
      case 'syncing': return 'Syncing…';
      case 'retrying': return 'Not synced: retrying';
      case 'too-large': return 'Not synced: your settings are larger than your account can hold. Largest: ' + state.tooLarge.join(', ');
      case 'rejected': return 'Not synced: the server refused your settings. Reload the page to try again.';
      case 'held': return 'No settings saved in your account. Your next change starts a new copy.';
      default: return 'Not synced yet';
    }
  }

  function renderStatus() {
    var el = document.getElementById('syncStatus');
    if (!el) return;
    el.textContent = statusText();
    el.classList.toggle('ok', state.status === 'ok');
    el.classList.toggle('err', state.status === 'retrying' || state.status === 'too-large' || state.status === 'rejected');
  }

  function mountSection(el) {
    el.innerHTML =
      '<p class="account-msg" id="syncStatus" role="status" aria-live="polite"></p>' +
      '<div class="account-actions">' +
      '<button type="button" id="syncNow" class="account-btn account-btn-secondary">Sync now</button>' +
      '<button type="button" id="syncDelete" class="account-btn account-btn-secondary">Delete synced settings from my account</button>' +
      '</div>' +
      '<p class="account-hint">Synced: your nodes, favorites, theme and customizer settings, saved packet filters, and the filter, sort and view choices of each page.</p>' +
      '<p class="account-hint">Not synced: channel keys and decrypted messages, the API key, panel and column sizes, collapsed panels and map positions.</p>' +
      '<p class="account-msg" id="syncMsg" role="status" aria-live="polite"></p>';
    renderStatus();
    document.getElementById('syncNow').addEventListener('click', function () { return syncNow(); });
    document.getElementById('syncDelete').addEventListener('click', function () {
      return dialog({
        title: 'Delete synced settings',
        text: ['This deletes the copy of your settings stored in your account. The settings on this device stay. Your next change starts a new copy.'],
        choices: [{ id: 'delete', label: 'Delete synced settings', primary: true }, { id: 'cancel', label: 'Cancel' }],
        focus: 'cancel' // the destructive choice is never the default
      }).then(function (choice) {
        if (choice !== 'delete') return;
        return deleteRemote().then(function (r) {
          window.CSAuth.say('syncMsg', r.ok ? 'Synced settings deleted from your account.' : window.CSAuth.errText(r), r.ok);
        }, function () { window.CSAuth.say('syncMsg', 'Network error, try again.', false); });
      });
    });
  }

  window.addEventListener('cs-auth-changed', function (e) {
    if (!window.CSAuth.isEnabled()) return;
    if (e.detail) activate(e.detail);
    else if (state.active) deactivate();
  });
  window.CSAuth.ready().then(function (u) { if (u && window.CSAuth.isEnabled()) activate(u); });

  window.CSSettingsSync = {
    mountSection: mountSection,
    syncNow: syncNow,
    _test: {
      mergeDocs: mergeDocs, state: state, activate: activate, deactivate: deactivate, pull: pull, push: push,
      midEdit: midEdit, flush: flush, onLogout: onLogout, deleteRemote: deleteRemote, statusText: statusText,
      showDialog: showDialog, useDialog: function (fn) { dialog = fn; }
    }
  };
})();
