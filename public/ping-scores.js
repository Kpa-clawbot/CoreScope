'use strict';

// Ping Scores — global highscore board + leaderboards derived from every
// ping-bot-triggering channel message ever seen. Backend: cmd/server/
// ping_scores.go, GET /api/ping-scores. Global (not scoped by region/area).

(function () {
  // Phosphor icons, not emoji -- see issue #1648's ongoing emoji-to-Phosphor
  // migration (enforced by test-issue-1648-*-emoji-scan.js on other files;
  // new files should just start clean rather than add to the debt).
  function phIcon(name) {
    return '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-' + name + '"/></svg>';
  }

  function formatKm(km) {
    return km.toFixed(1) + ' km';
  }

  function formatSeconds(s) {
    if (s < 60) return s.toFixed(1) + 's';
    var m = Math.floor(s / 60);
    var sec = Math.round(s % 60);
    return m + 'm ' + sec + 's';
  }

  function formatAgo(iso) {
    if (!iso) return '';
    var then = new Date(iso).getTime();
    if (isNaN(then)) return '';
    var diffSec = Math.max(0, (Date.now() - then) / 1000);
    if (diffSec < 60) return 'just now';
    if (diffSec < 3600) return Math.floor(diffSec / 60) + 'm ago';
    if (diffSec < 86400) return Math.floor(diffSec / 3600) + 'h ago';
    return Math.floor(diffSec / 86400) + 'd ago';
  }

  function viewPathLink(hash) {
    if (!hash) return '';
    return '<button type="button" class="ps-view-path" data-view-path="' + escapeHtml(hash) + '" style="background:none;border:none;padding:0;cursor:pointer;font:inherit;color:var(--link-color);text-decoration:underline">View path &rarr;</button>';
  }

  // recordDefs: each record's headline metric formatter + a short
  // description of what it measures, driving both the card render and the
  // "no record yet" placeholder so a missing metric doesn't just vanish.
  var recordDefs = [
    { key: 'farthestPing', icon: phIcon('broadcast'), title: 'Farthest Ping', desc: 'Longest reach from sender to farthest hearing station.',
      headline: function (p) { return p.farthestKm != null ? formatKm(p.farthestKm) : '—'; },
      sub: function (p) { return p.farthestNodeName ? 'heard by ' + escapeHtml(p.farthestNodeName) : ''; } },
    { key: 'mostHopsPing', icon: phIcon('shuffle'), title: 'Most Hops', desc: 'Deepest relay chain any station\'s observation took.',
      headline: function (p) { return p.deepestHops + ' hop' + (p.deepestHops === 1 ? '' : 's'); },
      sub: function (p) { return p.deepestNodeName ? 'via ' + escapeHtml(p.deepestNodeName) : ''; } },
    { key: 'widestSpreadPing', icon: phIcon('cell-signal-high'), title: 'Widest Spread', desc: 'Most stations that heard the same ping.',
      headline: function (p) { return p.stationCount + ' station' + (p.stationCount === 1 ? '' : 's'); },
      sub: function () { return ''; } },
    { key: 'fastestSpreadPing', icon: phIcon('lightning'), title: 'Fastest Spread', desc: 'Quickest full spread across 2+ stations.',
      headline: function (p) { return p.spreadSeconds != null ? formatSeconds(p.spreadSeconds) : '—'; },
      sub: function (p) { return p.stationCount + ' stations'; } },
    { key: 'mostEfficientPing', icon: phIcon('target'), title: 'Most Efficient', desc: 'Farthest reach per second of estimated RF airtime.',
      headline: function (p) { return p.kmPerSecondAirtime != null ? p.kmPerSecondAirtime.toFixed(1) + ' km/s' : '—'; },
      sub: function (p) { return p.farthestKm != null ? '~' + formatKm(p.farthestKm) + ' total' : ''; } }
  ];

  function recordCardHtml(def, ping) {
    if (!ping) {
      return '<div class="stat-card ps-record-card ps-empty">' +
        '<div class="stat-label">' + def.icon + ' ' + def.title + '</div>' +
        '<div class="stat-value" style="font-size:1rem;color:var(--text-muted)">No record yet</div>' +
        '<div class="ps-record-desc">' + def.desc + '</div>' +
        '</div>';
    }
    return '<div class="stat-card ps-record-card">' +
      '<div class="stat-label">' + def.icon + ' ' + def.title + '</div>' +
      '<div class="stat-value">' + def.headline(ping) + '</div>' +
      '<div class="ps-record-meta">' +
        (def.sub(ping) ? def.sub(ping) + ' · ' : '') +
        (ping.sender ? escapeHtml(ping.sender) + ' · ' : '') +
        formatAgo(ping.timestamp) +
      '</div>' +
      '<div class="ps-record-desc">' + def.desc + '</div>' +
      '<div style="margin-top:6px">' + viewPathLink(ping.hash) + '</div>' +
      '</div>';
  }

  // colLabel defaults to "Node" (Top Relays/Top First-Hearers are real,
  // resolvable mesh nodes) -- Top Senders passes "Sender" instead since
  // entries there are just the display name from the channel message
  // itself, never a resolvable node (see PingLeaderboardEntry.Pubkey's
  // omitempty on the backend, cmd/server/ping_scores.go).
  function leaderboardTableHtml(title, icon, entries, colLabel) {
    if (!entries || entries.length === 0) {
      return '<div class="ps-leaderboard"><h3>' + icon + ' ' + title + '</h3><p class="text-muted" style="font-size:0.9em">No data yet.</p></div>';
    }
    var rows = entries.map(function (e, i) {
      var name = escapeHtml(e.name || e.pubkey || '?');
      var link = e.pubkey ? '<a href="#/nodes/' + encodeURIComponent(e.pubkey) + '">' + name + '</a>' : name;
      return '<tr><td>' + (i + 1) + '</td><td>' + link + '</td><td>' + e.count + '</td></tr>';
    }).join('');
    return '<div class="ps-leaderboard">' +
      '<h3>' + icon + ' ' + title + '</h3>' +
      '<table class="ps-leaderboard-table"><thead><tr><th>#</th><th>' + (colLabel || 'Node') + '</th><th>Pings</th></tr></thead><tbody>' + rows + '</tbody></table>' +
      '</div>';
  }

  function render(container, data) {
    if (!data || data.totalPings === 0) {
      container.innerHTML =
        '<div class="ping-scores-page">' +
        '<h2>' + phIcon('trophy') + ' Ping Scores</h2>' +
        '<p class="text-muted">Global records and leaderboards from every "ping" sent in any channel. Not scoped by region.</p>' +
        '<div class="ps-empty-state" style="padding:40px;text-align:center;color:var(--text-muted)">' +
        '<p style="font-size:1.1em">No pings recorded yet.</p>' +
        '<p>Type <code>ping</code> in any channel to get the highscore board started!</p>' +
        '</div>' +
        '</div>';
      return;
    }

    var recordsHtml = recordDefs.map(function (def) {
      return recordCardHtml(def, data[def.key]);
    }).join('');

    container.innerHTML =
      '<div class="ping-scores-page">' +
      '<h2>' + phIcon('trophy') + ' Ping Scores</h2>' +
      '<p class="text-muted">Global records and leaderboards from every "ping" sent in any channel (' + data.totalPings + ' total). Not scoped by region. Updated ' + escapeHtml(formatAgo(data.generatedAt)) + '.</p>' +
      '<div class="stats-grid ps-records-grid">' + recordsHtml + '</div>' +
      '<div class="ps-leaderboards-grid">' +
        leaderboardTableHtml('Top Senders (30 days)', phIcon('megaphone'), data.senderLeaderboard, 'Sender') +
        leaderboardTableHtml('Top Relays', phIcon('repeat'), data.relayLeaderboard) +
        leaderboardTableHtml('Top First-Hearers', phIcon('eye'), data.observerLeaderboard) +
      '</div>' +
      '</div>';

    container.querySelectorAll('[data-view-path]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        if (window.PacketPathMap) window.PacketPathMap.open(btn.dataset.viewPath);
      });
    });
  }

  registerPage('ping-scores', {
    init: function (container) {
      container.innerHTML = '<div class="ping-scores-page"><h2>' + phIcon('trophy') + ' Ping Scores</h2><p class="text-muted">Loading…</p></div>';
      return api('/ping-scores').then(function (data) {
        render(container, data);
      }).catch(function (e) {
        container.innerHTML = '<div class="ping-scores-page"><h2>' + phIcon('trophy') + ' Ping Scores</h2><p class="text-muted">Failed to load: ' + escapeHtml(e.message) + '</p></div>';
      });
    },
    destroy: function () {}
  });
})();
