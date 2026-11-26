/* Overview tab of #/admin (docs/specs/2026-10-07-admin-dashboard-design.md):
 * "Needs attention", the Users card (GET /api/admin/stats) and the System
 * card (the public /api/health, /api/healthz, /api/mqtt/status and
 * /api/observers). Each source renders on its own, so one that fails shows
 * "Could not load" in its own block only. Refreshes on mount, on Refresh,
 * and every 60 s while the browser tab is visible. Mounted by admin.js
 * after its admin check. Every dynamic value goes through escapeHtml. */
(function () {
  'use strict';
  // Constants in this version; config or the customizer later (AGENTS.md
  // rule 8). The 24-hour and 5-attempt rules are server-side
  // (internal/users/stats.go).
  var MQTT_STALE_MS = 10 * 60 * 1000;
  var REFRESH_MS = 60 * 1000;
  var SOURCES = [
    ['stats', '/api/admin/stats'], ['health', '/api/health'], ['healthz', '/api/healthz'],
    ['mqtt', '/api/mqtt/status'], ['observers', '/api/observers']
  ];
  // mailTest: the Send test mail button and its last result, kept here
  // because the 60 s refresh rebuilds the whole body. userId is the account
  // it belongs to: another account in the same tab gets a fresh one.
  function newMailTest(userId) { return { busy: false, ok: null, text: '', userId: userId }; }
  function currentUserId() { return window.CS_USER ? window.CS_USER.id : null; }
  var state = { timer: null, seq: 0, healthz: null, busy: null, mailTest: newMailTest(null) };

  // Logout and login change the hash without a reload: drop the result (and
  // orphan an answer still in flight) as soon as the account changes.
  function resetMailTestFor(userId) {
    if (state.mailTest.userId !== userId) state.mailTest = newMailTest(userId);
  }
  window.addEventListener('cs-auth-changed', function (e) { resetMailTestFor(e.detail ? e.detail.id : null); });

  function plural(n, one, many) { return n + ' ' + (n === 1 ? one : many); }

  // Down: not connected, never delivered a message, or silent too long.
  function mqttDown(src, nowMs) {
    return !src.connected || !src.lastPacketUnix || nowMs - src.lastPacketUnix * 1000 > MQTT_STALE_MS;
  }

  function attentionItems(res, nowMs) {
    var items = [];
    var s = res.stats && res.stats.data;
    if (s) {
      if (s.stuckPending > 0) {
        items.push({ text: plural(s.stuckPending, 'account', 'accounts') + ' pending for more than 24 hours', href: '#/admin?tab=users&status=pending' });
      }
      if (s.bouncing > 0) {
        items.push({ text: plural(s.bouncing, 'address bounces', 'addresses bounce') + ' mail', href: '#/admin?tab=users&bouncing=1' });
      }
      (s.guessing || []).forEach(function (g) {
        items.push({ text: plural(g.failed, 'failed login', 'failed logins') + ' in 24 hours for ' + (g.displayName || '#' + g.userId),
          href: '#/admin?tab=audit&action=user.login.failed&user=' + g.userId });
      });
    }
    var m = res.mqtt && res.mqtt.data;
    if (m) {
      (m.sources || []).forEach(function (src) {
        if (!mqttDown(src, nowMs)) return;
        items.push({ text: 'MQTT source ' + src.name + (src.connected ? ' has no message in the last 10 minutes' : ' is not connected'), href: '#/observers' });
      });
    }
    return items;
  }

  function attentionHtml(items) {
    if (!items.length) return '';
    var html = '<section class="admin-card admin-attention" id="adminAttention" aria-labelledby="aoAttnH"><h3 id="aoAttnH">Needs attention</h3><ul>';
    items.forEach(function (it) { html += '<li><a href="' + escapeHtml(it.href) + '">' + escapeHtml(it.text) + '</a></li>'; });
    return html + '</ul></section>';
  }

  function failedHtml(what) {
    return '<p class="account-msg err">Could not load ' + escapeHtml(what) +
      '. <button type="button" class="account-btn account-btn-secondary" data-act="retry">Retry</button></p>';
  }

  function stat(key, label, value) {
    return '<div class="admin-stat"><dt>' + escapeHtml(label) + '</dt><dd data-stat="' + escapeHtml(key) + '">' +
      escapeHtml(value == null ? '-' : String(value)) + '</dd></div>';
  }

  function barsHtml(days) {
    var max = 1, total = 0;
    days.forEach(function (d) { var n = Number(d.count) || 0; total += n; if (n > max) max = n; });
    var html = '<div class="admin-bars" role="img" aria-label="' +
      escapeHtml('Registrations per day, last ' + days.length + ' days: ' + total + ' in total') + '">';
    days.forEach(function (d) {
      var n = Number(d.count) || 0;
      html += '<span class="admin-bar" style="height:' + Math.round(n / max * 100) + '%" title="' + escapeHtml(d.day + ': ' + n) + '"></span>';
    });
    return html + '</div>';
  }

  // Notification figures; the server sends them only with notifications on.
  function notifyStatsHtml(n) {
    if (!n) return '';
    return '<h4>Notifications</h4><dl class="admin-stats">' +
      stat('notifyMails', 'Mails, 24 hours', n.mailsLast24h + ' of ' + n.maxMailsPerDay) +
      stat('notifyWatches', 'Watched nodes', n.watches) + stat('notifyUsers', 'Users watching', n.watchingUsers) + '</dl>';
  }

  function usersCardHtml(r) {
    var head = '<section class="admin-card" id="aoUsers" aria-labelledby="aoUsersH"><h3 id="aoUsersH">Users</h3>';
    if (!r || r.error) return head + failedHtml('user figures') + '</section>';
    var s = r.data;
    return head + '<dl class="admin-stats">' +
      stat('total', 'Accounts', s.total) + stat('active', 'Active', s.active) + stat('pending', 'Pending', s.pending) +
      stat('disabled', 'Disabled', s.disabled) + stat('admins', 'Admins', s.admins) +
      stat('new7d', 'New, 7 days', s.new7d) + stat('new30d', 'New, 30 days', s.new30d) +
      stat('active7d', 'Active users, 7 days', s.active7d) + stat('active30d', 'Active users, 30 days', s.active30d) +
      stat('logins24h', 'Logins, 24 hours', s.logins24h) + stat('failedLogins24h', 'Failed logins, 24 hours', s.failedLogins24h) +
      '</dl>' + barsHtml(s.newPerDay || []) + mailStatsHtml(s.mail7d || {}, s.mailDeliveryStatus !== false) +
      notifyStatsHtml(s.notify) + '</section>';
  }

  // Without delivery status (smtp) mails have no event and land in pending
  // or other, so the buckets would mislead: one Sent figure instead.
  function mailStatsHtml(m, deliveryStatus) {
    var html = '<h4>Mail, last 7 days</h4><dl class="admin-stats">';
    if (!deliveryStatus) {
      var sent = 0;
      ['delivered', 'bounced', 'blocked', 'spam', 'pending', 'other'].forEach(function (k) { sent += Number(m[k]) || 0; });
      return html + stat('mailSent', 'Sent', sent) + '</dl><p class="account-hint">No delivery status with SMTP.</p>';
    }
    return html + stat('mailDelivered', 'Delivered', m.delivered) + stat('mailBounced', 'Bounced', m.bounced) +
      stat('mailBlocked', 'Blocked', m.blocked) + stat('mailSpam', 'Spam', m.spam) +
      stat('mailPending', 'Pending', m.pending) + stat('mailOther', 'Other', m.other) + '</dl>';
  }

  function mailTestClass(t) { return 'account-msg' + (t.ok === true ? ' ok' : t.ok === false ? ' err' : ''); }

  function mailTestHtml(t) {
    return '<h4>Mail</h4><div class="admin-mailtest"><button type="button" class="account-btn account-btn-secondary" id="aoMailTest"' +
      (t.busy ? ' disabled' : '') + '>Send test mail</button><span id="aoMailTestResult" class="' + mailTestClass(t) +
      '" role="status" aria-live="polite">' + escapeHtml(t.text) + '</span></div>';
  }

  function mailTestText(r) {
    if (r.ok) return 'Test mail sent to your address through ' + r.data.provider + '.';
    if (r.status === 429) return 'Too many test mails, try again later.';
    return 'Test mail failed: ' + CSAuth.errText(r);
  }

  // Writes t into the live button and result element.
  function showMailTest(t) {
    var b = document.getElementById('aoMailTest');
    if (b) b.disabled = t.busy;
    var el = document.getElementById('aoMailTestResult');
    if (el) { el.textContent = t.text; el.className = mailTestClass(t); }
  }

  // One test mail at a time. The result goes into state.mailTest (the
  // periodic rebuild renders it from there) and into the live elements. An
  // answer that arrives after the account changed writes nothing.
  function sendTestMail() {
    var t = state.mailTest;
    if (t.busy) return;
    t.busy = true;
    t.ok = null;
    t.text = 'Sending...';
    showMailTest(t);
    var done = function (ok, text) {
      t.busy = false;
      t.ok = ok;
      t.text = text;
      if (state.mailTest === t) showMailTest(t);
    };
    CSAuth.request('POST', '/api/admin/mail/test').then(function (r) { done(!!r.ok, mailTestText(r)); },
      function () { done(false, 'Test mail failed: network error, try again.'); });
  }

  function systemCardHtml(res, nowMs) {
    var html = '<section class="admin-card" id="aoSystem" aria-labelledby="aoSysH"><h3 id="aoSysH">System</h3><h4>Server</h4>';
    if (res.health.error) {
      html += failedHtml('server health');
    } else {
      var h = res.health.data;
      var ready = res.healthz.error ? 'unknown' : (res.healthz.data.ready ? 'ready' : 'warming up');
      html += '<dl class="admin-stats">' + stat('version', 'Version', h.version || '-') + stat('commit', 'Commit', h.commit || '-') +
        stat('uptime', 'Uptime', h.uptimeHuman || '-') + stat('ready', 'State', ready) + '</dl>';
    }
    html += '<h4>MQTT sources</h4>';
    if (res.mqtt.error) {
      html += failedHtml('MQTT status');
    } else {
      var srcs = res.mqtt.data.sources || [];
      if (!srcs.length) html += '<p class="account-hint">No MQTT sources reported.</p>';
      else {
        html += '<ul class="admin-list">';
        srcs.forEach(function (s) {
          html += '<li>' + escapeHtml(s.name) + ': ' + (s.connected ? 'connected' : 'not connected') +
            ', last message ' + escapeHtml(window.MqttStatusPanel.fmtRelative(s.lastPacketUnix, nowMs)) + '</li>';
        });
        html += '</ul>';
      }
    }
    html += '<h4>Observers</h4>';
    if (res.observers.error) {
      html += failedHtml('observers');
    } else {
      var c = window.ObserversSummary.computeCounts(res.observers.data.observers || []);
      html += '<dl class="admin-stats">' + stat('observersOnline', 'Online', c.online) + stat('observersTotal', 'Total', c.total) + '</dl>';
    }
    return html + mailTestHtml(state.mailTest) + '<p class="account-links"><a href="#/perf">Perf page</a> <a href="#/observers">MQTT status panel</a></p></section>';
  }

  function render(res, nowMs) {
    return attentionHtml(attentionItems(res, nowMs)) +
      '<div class="admin-cards">' + usersCardHtml(res.stats) + systemCardHtml(res, nowMs) + '</div>';
  }

  // Resolves (never rejects) to {key: {data} | {error: true}} per source.
  // skip names a source to leave out (the timer skips healthz): its
  // entry is then the last result kept in state.
  function fetchAll(request, skip) {
    return Promise.all(SOURCES.map(function (src) {
      if (src[0] === skip) return Promise.resolve(state.healthz || { error: true });
      return request('GET', src[1]).then(function (r) {
        // /api/healthz answers 503 {ready: false} while warming up: that is data.
        if (r.ok || (src[0] === 'healthz' && r.status === 503 && r.data && r.data.ready === false)) return { data: r.data };
        return { error: true };
      }, function () { return { error: true }; });
    })).then(function (list) {
      var out = {};
      SOURCES.forEach(function (src, i) { out[src[0]] = list[i]; });
      return out;
    });
  }

  // full: also read /api/healthz (open and Refresh); the timer passes false.
  function refresh(full) {
    var seq = ++state.seq;
    return fetchAll(CSAuth.request, full ? null : 'healthz').then(function (res) {
      if (full && seq === state.seq) state.healthz = res.healthz;
      var box = document.getElementById('aoBody');
      if (!box || seq !== state.seq) return;
      var now = Date.now();
      var html = render(res, now);
      box.innerHTML = html;
      document.getElementById('aoUpdated').textContent = 'Updated ' + new Date(now).toLocaleTimeString();
    });
  }

  // Open, Refresh and Retry: one full refresh at a time (each one walks the
  // packet store for /api/healthz). Clicks while it runs are ignored and the
  // Refresh button stays disabled until it settles.
  function fullRefresh() {
    if (state.busy) return state.busy;
    var btn = document.getElementById('aoRefresh');
    if (btn) btn.disabled = true;
    var p = refresh(true);
    var settle = function () {
      if (state.busy !== p) return;
      state.busy = null;
      var b = document.getElementById('aoRefresh');
      if (b) b.disabled = false;
    };
    p.then(settle, settle);
    state.busy = p;
    return p;
  }

  function mount(container) {
    container.innerHTML = '<div class="admin-overview"><div class="admin-toolbar">' +
      '<button type="button" class="account-btn account-btn-secondary" id="aoRefresh">Refresh</button>' +
      '<span class="account-hint" id="aoUpdated"></span></div><div id="aoBody"><p>Loading…</p></div></div>';
    document.getElementById('aoRefresh').addEventListener('click', function () { fullRefresh(); });
    document.getElementById('aoBody').addEventListener('click', function (e) {
      if (e.target.closest('button[data-act="retry"]')) fullRefresh();
      else if (e.target.closest('#aoMailTest')) sendTestMail();
    });
    resetMailTestFor(currentUserId());
    clearInterval(state.timer);
    state.timer = setInterval(function () {
      if (document.visibilityState === 'visible') refresh(false);
    }, REFRESH_MS);
    state.busy = null;
    return fullRefresh();
  }

  function unmount() {
    clearInterval(state.timer);
    state.timer = null;
    state.healthz = null;
    state.busy = null;
    state.seq++;
  }

  window.CSAdminOverview = { mount: mount, unmount: unmount,
    _test: { mqttDown: mqttDown, attentionItems: attentionItems, attentionHtml: attentionHtml, usersCardHtml: usersCardHtml,
      systemCardHtml: systemCardHtml, render: render, fetchAll: fetchAll } };
})();
