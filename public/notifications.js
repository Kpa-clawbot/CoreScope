/* Node notifications client (docs/specs/2026-10-07-node-notifications-design.md).
 * window.CSNotify: the "Notify me" toggle on the node side pane and full
 * page, and the account page's Notifications section. Inert unless
 * /api/config/client advertises userManagement.notifications (roles.js sets
 * window.MC_USER_MGMT). One GET /api/account/notifications per user, cached;
 * every change answers the full state, which replaces the cache. Every
 * dynamic value in HTML goes through escapeHtml; messages use textContent. */
(function () {
  'use strict';
  var cache = { userId: null, promise: null };

  function enabled() { return !!(window.MC_USER_MGMT && window.MC_USER_MGMT.notifications); }
  function currentUser() { return window.CSAuth ? window.CSAuth.user() : null; }

  // load resolves to the caller's state (the GET body) or null.
  function load() {
    var u = currentUser();
    if (!enabled() || !u) return Promise.resolve(null);
    if (!cache.promise || cache.userId !== u.id) {
      cache.userId = u.id;
      cache.promise = window.CSAuth.request('GET', '/api/account/notifications').then(function (r) {
        if (!r.ok) { cache.promise = null; return null; }
        return r.data;
      }, function () { cache.promise = null; return null; });
    }
    return cache.promise;
  }

  // store makes data (the answer of a change) the cached state.
  function store(data) {
    var u = currentUser();
    cache.userId = u ? u.id : null;
    cache.promise = Promise.resolve(data);
    return data;
  }

  function isWatched(data, pubkey) {
    var pk = String(pubkey || '').toLowerCase();
    return !!(data && data.watches && data.watches.some(function (w) { return w.pubkey === pk; }));
  }

  // toggleState: 'hidden' (no state), 'on', 'off' or 'full' (limit reached).
  function toggleState(data, pubkey) {
    if (!data) return 'hidden';
    if (isWatched(data, pubkey)) return 'on';
    return (data.watches || []).length >= Number(data.limits && data.limits.maxWatches) ? 'full' : 'off';
  }

  function toggleHtml(state, data) {
    if (state === 'hidden') return '';
    var on = state === 'on';
    var title = on ? 'You get a mail when this node goes offline, comes back or reports a low battery. Click to stop.'
                   : 'Get a mail when this node goes offline, comes back or reports a low battery.';
    var html = '<button type="button" class="btn-primary node-notify-btn" data-notify-toggle aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (state === 'full' ? ' disabled' : '') + ' title="' + escapeHtml(title) + '">' + escapeHtml(on ? 'Notifying' : 'Notify me') + '</button>';
    if (state === 'full') {
      html += ' <small class="node-notify-hint">' +
        escapeHtml('You watch the maximum of ' + data.limits.maxWatches + ' nodes; remove one on your account page.') + '</small>';
    }
    return html + ' <small class="node-notify-hint" data-notify-msg role="status" aria-live="polite"></small>';
  }

  function say(slot, text) {
    var m = slot.querySelector('[data-notify-msg]');
    if (m) m.textContent = text;
  }

  function render(slot, pubkey, data) {
    var state = toggleState(data, pubkey);
    slot.innerHTML = toggleHtml(state, data);
    var btn = slot.querySelector('[data-notify-toggle]');
    if (!btn) return;
    btn.addEventListener('click', function () {
      var on = btn.getAttribute('aria-pressed') === 'true';
      btn.disabled = true;
      var path = '/api/account/notifications/watches/' + encodeURIComponent(String(pubkey).toLowerCase());
      return window.CSAuth.request(on ? 'DELETE' : 'PUT', path).then(function (r) {
        if (!r.ok) { btn.disabled = false; say(slot, window.CSAuth.errText(r)); return; }
        render(slot, pubkey, store(r.data));
      }, function () { btn.disabled = false; say(slot, 'Network error, try again.'); });
    });
  }

  // mount fills slot with the toggle for pubkey; empty when the feature is
  // off or nobody is logged in.
  function mount(slot, pubkey) {
    if (!slot) return Promise.resolve();
    slot.innerHTML = '';
    var ready = window.CSAuth && window.CSAuth.ready ? window.CSAuth.ready() : Promise.resolve();
    return Promise.resolve(ready).then(function () {
      if (!enabled() || !currentUser()) return;
      return load().then(function (data) { render(slot, pubkey, data); });
    });
  }

  window.addEventListener('cs-auth-changed', function () { cache.userId = null; cache.promise = null; });

  window.CSNotify = { enabled: enabled, load: load, store: store, toggleState: toggleState, toggleHtml: toggleHtml, mount: mount };
})();
