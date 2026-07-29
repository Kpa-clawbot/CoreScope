// Network Digest — third and final piece of dborup's requested
// network-changes tooling (Tools > Network Digest). Rolling-window
// summary of New Nodes (new-nodes.js) + Node Changes (node-changes.js)
// activity: "what happened lately" at a glance. Backend:
// cmd/server/network_digest.go, GET /api/analytics/network-digest.
(function () {
  'use strict';

  var container = null;
  var window_ = '7d'; // 'window' shadows the DOM global if unqualified

  function escapeHtml(s) {
    return s == null ? '' : String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  var WINDOW_TABS = [
    { key: '24h', label: '24 Hours' },
    { key: '7d', label: '7 Days' },
    { key: '30d', label: '30 Days' },
  ];

  function windowTabsHtml() {
    return WINDOW_TABS.map(function (t) {
      return '<button type="button" class="tab-btn' + (t.key === window_ ? ' active' : '') + '" data-window="' + t.key + '">' + t.label + '</button>';
    }).join('');
  }

  function init(app) {
    container = app;
    window_ = '7d';

    container.innerHTML =
      '<div class="tools-landing" style="max-width:1100px">' +
        '<h2><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-chart-bar"/></svg> Network Digest</h2>' +
        '<p class="help-text">What happened lately, at a glance -- built on top of ' +
          '<a href="#/tools/new-nodes">New Nodes</a> and <a href="#/tools/node-changes">Node Changes</a>.</p>' +
        '<div id="network-digest-window-tabs" style="display:flex;gap:6px;margin:12px 0 16px">' + windowTabsHtml() + '</div>' +
        '<div id="network-digest-status" class="text-muted" style="font-size:12px;margin-bottom:8px"></div>' +
        '<div id="network-digest-content"></div>' +
      '</div>';

    var tabsWrap = document.getElementById('network-digest-window-tabs');
    if (tabsWrap) {
      tabsWrap.addEventListener('click', function (evt) {
        var el = evt.target;
        if (el && el.closest) el = el.closest('[data-window]');
        var w = el && el.dataset ? el.dataset.window : null;
        if (!w || w === window_) return;
        window_ = w;
        tabsWrap.innerHTML = windowTabsHtml();
        load();
      });
    }

    load();
  }

  function destroy() {
    container = null;
  }

  function load() {
    var statusEl = document.getElementById('network-digest-status');
    var contentEl = document.getElementById('network-digest-content');
    if (contentEl) contentEl.innerHTML = '<p class="text-muted">Loading…</p>';
    if (statusEl) statusEl.textContent = '';
    api('/analytics/network-digest?window=' + encodeURIComponent(window_))
      .then(function (data) {
        render(data);
      })
      .catch(function (e) {
        if (contentEl) contentEl.innerHTML = '';
        if (statusEl) statusEl.textContent = 'Failed to load: ' + escapeHtml(e.message);
      });
  }

  function tile(icon, value, label, link) {
    var valueHtml = '<div class="stat-value">' + value + '</div>';
    var inner = '<div class="stat-label">' + icon + ' ' + escapeHtml(label) + '</div>' + valueHtml;
    if (link) {
      return '<a href="' + link + '" class="stat-card" style="display:block;text-decoration:none;color:inherit">' + inner + '</a>';
    }
    return '<div class="stat-card">' + inner + '</div>';
  }

  function phIcon(name) {
    return '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-' + name + '"/></svg>';
  }

  function render(data) {
    var statusEl = document.getElementById('network-digest-status');
    var contentEl = document.getElementById('network-digest-content');
    if (!contentEl) return;

    if (statusEl) {
      statusEl.textContent = 'Since ' + escapeHtml(data.since) + (typeof timeAgo === 'function' ? ' (' + timeAgo(data.since) + ')' : '');
    }

    var tiles =
      tile(phIcon('rocket'), data.newNodes, 'New Nodes', '#/tools/new-nodes') +
      tile(phIcon('shuffle'), data.roleChanges, 'Role Changes', '#/tools/node-changes') +
      tile(phIcon('tag'), data.nameChanges, 'Name Changes', '#/tools/node-changes') +
      tile(phIcon('map-pin'), data.positionMoves, 'Position Moves', '#/tools/node-changes') +
      tile(phIcon('arrow-clockwise'), data.resurrections, 'Returned', '#/tools/node-changes');

    var topAreaHtml = '';
    if (data.topArea) {
      topAreaHtml =
        '<div class="analytics-card" style="margin-top:16px">' +
          '<h3 style="margin:0 0 4px">' + phIcon('map-trifold') + ' Most Growth</h3>' +
          '<p style="margin:0">' +
            '<span class="badge-region">' + escapeHtml(data.topArea.label) + '</span> — ' +
            data.topArea.count + ' new node' + (data.topArea.count === 1 ? '' : 's') +
          '</p>' +
        '</div>';
    }

    var allZero = !data.newNodes && !data.roleChanges && !data.nameChanges && !data.positionMoves && !data.resurrections;
    if (allZero) {
      contentEl.innerHTML = '<p class="text-muted" style="padding:20px 0">Nothing to report in this window.</p>';
      return;
    }

    contentEl.innerHTML =
      '<div class="stats-grid">' + tiles + '</div>' +
      topAreaHtml;
  }

  window.NetworkDigestTool = { init: init, destroy: destroy };
  if (typeof registerPage === 'function') registerPage('network-digest-tool', { init: init, destroy: destroy });
})();
