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
      (state === 'full' ? ' disabled' : '') + ' title="' + escapeHtml(title) + '"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-envelope-simple"/></svg> ' +
      escapeHtml(on ? 'Notifying' : 'Notify me') + '</button>';
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

  var EVENT_LABELS = {
    'node.offline': 'A watched node goes offline or comes back',
    'node.battery': 'A watched node reports a low battery or recovers',
    'foreign.new': 'A new foreign node appears (admin)',
    'observer.offline': 'An observer goes offline or comes back (admin)'
  };

  function eventLabel(e, data) {
    if (e === 'node.external') return 'A watched node gets a ' + (data.externalLabel || 'external') + ' alert or it clears';
    return EVENT_LABELS[e] || e;
  }

  function eventsHtml(data) {
    return (data.availableEvents || []).map(function (e) {
      var id = 'notifyEv-' + e.replace(/\./g, '-');
      return '<label class="account-check" for="' + escapeHtml(id) + '"><input type="checkbox" id="' + escapeHtml(id) +
        '" data-notify-event="' + escapeHtml(e) + '"' + ((data.events || []).indexOf(e) !== -1 ? ' checked' : '') + '> ' +
        escapeHtml(eventLabel(e, data)) + '</label>';
    }).join('');
  }

  function watchesHtml(data) {
    if (!data.watches.length) return '<p class="account-hint">You watch no nodes yet. Use Notify me on a node page, or Watch my nodes below.</p>';
    return '<ul class="account-watches">' + data.watches.map(function (w) {
      return '<li data-pubkey="' + escapeHtml(w.pubkey) + '"><span><a href="#/nodes/' + encodeURIComponent(w.pubkey) + '">' +
        escapeHtml(w.name || w.pubkey.slice(0, 12)) + '</a>' + (w.known ? '' : ' <small>(no longer in the database)</small>') + '</span>' +
        '<button type="button" class="account-btn account-btn-secondary" data-unwatch="' + escapeHtml(w.pubkey) + '">Remove</button></li>';
    }).join('') + '</ul>';
  }

  function sectionHtml(data) {
    var lim = data.limits || {};
    return '<form id="notifyForm" class="account-form" novalidate>' +
      '<label class="account-check" for="notifyEnabled"><input type="checkbox" id="notifyEnabled"' + (data.enabled ? ' checked' : '') + '> Send me notification mails</label>' +
      '<fieldset class="account-fieldset"><legend>Events</legend>' + eventsHtml(data) + '</fieldset>' +
      '<p class="account-hint">' + escapeHtml('At most ' + lim.perUserPerDay + ' mails per 24 hours; sent in the last 24 hours: ' + lim.mailsLast24h + '.') + '</p>' +
      '<button type="submit" class="account-btn account-btn-primary">Save</button></form>' +
      '<h4>' + escapeHtml('Watched nodes (' + data.watches.length + ' of ' + lim.maxWatches + ')') + '</h4>' +
      '<div id="notifyWatches">' + watchesHtml(data) + '</div>' +
      '<button type="button" class="account-btn account-btn-secondary" id="notifyMyNodes">Watch my nodes</button>';
  }

  function sayIn(msgId, text, ok) { window.CSAuth.say(msgId, text, ok); }
  function failed(msgId) { return function () { sayIn(msgId, 'Network error, try again.', false); }; }

  // draw renders the section from data and binds its controls. Controls are
  // found by id: they are unique on the account page. busy guards against a
  // second request while one is in flight.
  function draw(el, msgId, data) {
    var busy = false;
    function guarded(fn) {
      if (busy) return Promise.resolve();
      busy = true;
      return fn().then(function () { busy = false; }, function () { busy = false; });
    }
    el.innerHTML = sectionHtml(data);
    document.getElementById('notifyForm').addEventListener('submit', function (e) {
      e.preventDefault();
      var events = (data.availableEvents || []).filter(function (ev) {
        var box = document.getElementById('notifyEv-' + ev.replace(/\./g, '-'));
        return !!(box && box.checked);
      });
      var en = document.getElementById('notifyEnabled');
      return guarded(function () {
        return window.CSAuth.request('PUT', '/api/account/notifications', { enabled: !!(en && en.checked), events: events }).then(function (r) {
          if (!r.ok) { sayIn(msgId, window.CSAuth.errText(r), false); return; }
          draw(el, msgId, store(r.data));
          sayIn(msgId, 'Saved.', true);
        }, failed(msgId));
      });
    });
    document.getElementById('notifyWatches').addEventListener('click', function (e) {
      var pk = e.target && e.target.getAttribute && e.target.getAttribute('data-unwatch');
      if (!pk) return;
      return guarded(function () {
        return window.CSAuth.request('DELETE', '/api/account/notifications/watches/' + encodeURIComponent(pk)).then(function (r) {
          if (!r.ok) { sayIn(msgId, window.CSAuth.errText(r), false); return; }
          draw(el, msgId, store(r.data));
          sayIn(msgId, 'Removed.', true);
        }, failed(msgId));
      });
    });
    document.getElementById('notifyMyNodes').addEventListener('click', function () {
      return guarded(function () {
        return window.CSAuth.request('POST', '/api/account/notifications/watch-my-nodes').then(function (r) {
          if (!r.ok) { sayIn(msgId, window.CSAuth.errText(r), false); return; }
          var d = r.data;
          draw(el, msgId, store(d.account));
          sayIn(msgId, (d.added + d.already + d.skipped) === 0
            ? 'Your synced My nodes list is empty. Add nodes to My nodes on the Nodes page and turn on settings sync.'
            : 'Added ' + d.added + ', already watched ' + d.already + ', skipped ' + d.skipped + '.', true);
        }, failed(msgId));
      });
    });
  }

  // mountSection loads the caller's state (always fresh) into el.
  function mountSection(el, msgId) {
    if (!el || !enabled() || !currentUser()) return Promise.resolve();
    return window.CSAuth.request('GET', '/api/account/notifications').then(function (r) {
      if (!r.ok) { sayIn(msgId, window.CSAuth.errText(r), false); return; }
      draw(el, msgId, store(r.data));
    }, failed(msgId));
  }

  window.addEventListener('cs-auth-changed', function () { cache.userId = null; cache.promise = null; });

  window.CSNotify = { enabled: enabled, load: load, store: store, toggleState: toggleState, toggleHtml: toggleHtml, mount: mount,
    sectionHtml: sectionHtml, mountSection: mountSection };
})();
