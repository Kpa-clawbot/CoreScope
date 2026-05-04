/* === CoreScope — roles-page.js === */
'use strict';

(function () {
  let refreshTimer = null;

  function init(app) {
    app.innerHTML =
      '<div class="roles-page" data-page="roles">' +
      '  <div class="page-header">' +
      '    <h2>Roles</h2>' +
      '    <button class="btn-icon" data-action="roles-refresh" title="Refresh" aria-label="Refresh roles">🔄</button>' +
      '  </div>' +
      '  <p class="text-muted" style="margin:0 0 12px 0">Distribution of node roles across the mesh, with per-role clock-skew posture.</p>' +
      '  <div id="rolesContent"><div class="text-center text-muted" style="padding:40px">Loading…</div></div>' +
      '</div>';
    app.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-action="roles-refresh"]');
      if (btn) load();
    });
    load();
    refreshTimer = setInterval(load, 60000);
  }

  function destroy() {
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = null;
  }

  async function load() {
    var container = document.getElementById('rolesContent');
    if (!container) return;
    try {
      var resp = await fetch('/api/analytics/roles');
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      var data = await resp.json();
      render(container, data);
    } catch (err) {
      container.innerHTML = '<div class="text-center" style="padding:40px;color:var(--color-error,#c00)">Failed to load roles: ' + escapeHtml(String(err.message || err)) + '</div>';
    }
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function fmtSec(v) {
    if (!v && v !== 0) return '—';
    var abs = Math.abs(v);
    if (abs < 1) return v.toFixed(2) + 's';
    if (abs < 60) return v.toFixed(1) + 's';
    if (abs < 3600) return (v / 60).toFixed(1) + 'm';
    if (abs < 86400) return (v / 3600).toFixed(1) + 'h';
    return (v / 86400).toFixed(1) + 'd';
  }

  function roleEmoji(role) {
    if (window.ROLE_EMOJI && window.ROLE_EMOJI[role]) return window.ROLE_EMOJI[role];
    return '•';
  }

  function render(container, data) {
    var roles = (data && data.roles) || [];
    var total = (data && data.totalNodes) || 0;
    if (roles.length === 0) {
      container.innerHTML = '<div class="text-center text-muted" style="padding:40px">No roles to show.</div>';
      return;
    }
    var maxCount = roles.reduce(function (m, r) { return Math.max(m, r.nodeCount || 0); }, 0) || 1;

    var rows = roles.map(function (r) {
      var pct = total > 0 ? ((r.nodeCount / total) * 100).toFixed(1) : '0.0';
      var barW = Math.round((r.nodeCount / maxCount) * 100);
      var sevCells =
        '<span title="OK (skew &lt; 5min)" style="color:var(--color-success,#0a0)">' + (r.okCount || 0) + '</span> / ' +
        '<span title="Warning (5min – 1h)" style="color:var(--color-warning,#e80)">' + (r.warningCount || 0) + '</span> / ' +
        '<span title="Critical (1h – 30d)" style="color:var(--color-error,#c00)">' + (r.criticalCount || 0) + '</span> / ' +
        '<span title="Absurd (&gt; 30d)" style="color:#a0a">' + (r.absurdCount || 0) + '</span> / ' +
        '<span title="No clock (&gt; 365d)" style="color:#888">' + (r.noClockCount || 0) + '</span>';
      return '' +
        '<tr data-role="' + escapeHtml(r.role) + '">' +
          '<td>' + roleEmoji(r.role) + ' <strong>' + escapeHtml(r.role) + '</strong></td>' +
          '<td style="text-align:right">' + r.nodeCount + '</td>' +
          '<td style="text-align:right">' + pct + '%</td>' +
          '<td style="min-width:140px">' +
            '<div style="background:var(--color-surface-2,#eee);height:10px;border-radius:5px;overflow:hidden">' +
              '<div style="background:var(--color-accent,#06c);width:' + barW + '%;height:100%"></div>' +
            '</div>' +
          '</td>' +
          '<td style="text-align:right">' + (r.withSkew || 0) + '</td>' +
          '<td style="text-align:right">' + fmtSec(r.medianAbsSkewSec || 0) + '</td>' +
          '<td style="text-align:right">' + fmtSec(r.meanAbsSkewSec || 0) + '</td>' +
          '<td style="white-space:nowrap">' + sevCells + '</td>' +
        '</tr>';
    }).join('');

    container.innerHTML =
      '<div class="roles-summary" style="margin-bottom:12px;color:var(--color-text-muted,#666)">' +
        '<strong>' + total + '</strong> nodes across <strong>' + roles.length + '</strong> roles' +
      '</div>' +
      '<table id="rolesTable" class="data-table" style="width:100%">' +
        '<thead><tr>' +
          '<th>Role</th>' +
          '<th style="text-align:right">Count</th>' +
          '<th style="text-align:right">Share</th>' +
          '<th>Distribution</th>' +
          '<th style="text-align:right" title="Nodes with clock-skew samples">w/ Skew</th>' +
          '<th style="text-align:right" title="Median absolute skew">Median |skew|</th>' +
          '<th style="text-align:right" title="Mean absolute skew">Mean |skew|</th>' +
          '<th title="OK / Warning / Critical / Absurd / No-clock">Severity</th>' +
        '</tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
      '</table>';
  }

  registerPage('roles', { init: init, destroy: destroy });
})();
