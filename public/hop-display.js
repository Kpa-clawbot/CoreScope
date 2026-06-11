/* === CoreScope — hop-display.js === */
/* Shared hop rendering with conflict info for all pages */
'use strict';

window.HopDisplay = (function() {
  function escapeHtml(s) {
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  }

  // Dismiss any open conflict popover
  function dismissPopover() {
    const old = document.querySelector('.hop-conflict-popover');
    if (old) old.remove();
  }

  // Global click handler to dismiss popovers
  let _listenerAttached = false;
  function ensureGlobalListener() {
    if (_listenerAttached) return;
    _listenerAttached = true;
    document.addEventListener('click', (e) => {
      if (!e.target.closest('.hop-conflict-popover') && !e.target.closest('.hop-conflict-btn')) {
        dismissPopover();
      }
    });
  }

  function showConflictPopover(btn, h, conflicts, globalFallback) {
    dismissPopover();
    ensureGlobalListener();

    const regional = conflicts.filter(c => c.regional);
    const shown = regional.length > 0 ? regional : conflicts;

    let html = `<div class="hop-conflict-header">${escapeHtml(h)} — ${shown.length} candidate${shown.length > 1 ? 's' : ''}${regional.length > 0 ? ' in region' : ' (global fallback)'}</div>`;
    html += '<div class="hop-conflict-list">';
    for (const c of shown) {
      const name = escapeHtml(c.name || c.pubkey?.slice(0, 16) || '?');
      const dist = c.distKm != null ? `<span class="hop-conflict-dist">${c.distKm}km</span>` : '';
      const pk = c.pubkey ? c.pubkey.slice(0, 12) + '…' : '';
      html += `<a href="#/nodes/${encodeURIComponent(c.pubkey || '')}" class="hop-conflict-item">
        <span class="hop-conflict-name">${name}</span>
        ${dist}
        <span class="hop-conflict-pk">${pk}</span>
      </a>`;
    }
    html += '</div>';

    const popover = document.createElement('div');
    popover.className = 'hop-conflict-popover';
    popover.innerHTML = html;
    document.body.appendChild(popover);

    // Position near the button
    const rect = btn.getBoundingClientRect();
    popover.style.top = (rect.bottom + window.scrollY + 4) + 'px';
    popover.style.left = Math.max(8, Math.min(rect.left + window.scrollX - 60, window.innerWidth - 280)) + 'px';
  }

  /**
   * Render a hop prefix as HTML with conflict info.
   */
  function renderHop(h, entry, opts) {
    opts = opts || {};
    if (!entry) entry = {};
    if (typeof entry === 'string') entry = { name: entry };

    const name = entry.name || null;
    const pubkey = entry.pubkey || h;
    const ambiguous = entry.ambiguous || false;
    const conflicts = entry.conflicts || [];
    const globalFallback = entry.globalFallback || false;
    const unreliable = entry.unreliable || false;
    const display = opts.hexMode ? h : (name ? escapeHtml(opts.truncate ? name.slice(0, opts.truncate) : name) : h);

    // Simple title for the hop link itself
    let title = h;
    if (unreliable) title += ' — unreliable';

    // Badge — only count regional conflicts
    const regionalConflicts = conflicts.filter(c => c.regional);
    const badgeCount = regionalConflicts.length > 0 ? regionalConflicts.length : (globalFallback ? conflicts.length : 0);
    const conflictData = escapeHtml(JSON.stringify({ h, conflicts, globalFallback }));
    const conflictBadge = badgeCount > 1
      ? ` <button class="hop-conflict-btn status-warn" data-conflict='${conflictData}' onclick="event.preventDefault();event.stopPropagation();HopDisplay._showFromBtn(this)" title="${badgeCount} candidates — click for details"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>${badgeCount}</button>`
      : '';
    const unreliableBadge = unreliable
      ? ' <button class="hop-unreliable-btn status-warn" aria-label="Unreliable name resolution" title="Unreliable name resolution — this hash\u2192name match is geographically inconsistent with the surrounding path hops. The repeater itself may be fine; this specific hop assignment is uncertain."><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg></button>'
      : '';
    const warnBadge = conflictBadge + unreliableBadge;

    const cls = [
      'hop',
      name ? 'hop-named' : '',
      ambiguous ? 'hop-ambiguous' : '',
      unreliable ? 'hop-unreliable' : '',
      globalFallback ? 'hop-global-fallback' : '',
    ].filter(Boolean).join(' ');

    if (opts.link !== false) {
      return `<a class="${cls} hop-link" href="#/nodes/${encodeURIComponent(pubkey)}" title="${escapeHtml(title)}" data-hop-link="true">${display}</a>${warnBadge}`;
    }
    return `<span class="${cls}" title="${escapeHtml(title)}">${display}</span>${warnBadge}`;
  }

  /**
   * Render a full path as HTML.
   */
  function renderPath(hops, cache, opts) {
    opts = opts || {};
    const sep = opts.separator || ' → ';
    if (!hops || !hops.length) return '—';
    return hops.filter(Boolean).map(h => renderHop(h, cache[h], opts)).join(sep);
  }

  // Called from inline onclick
  function _showFromBtn(btn) {
    try {
      const data = JSON.parse(btn.dataset.conflict);
      showConflictPopover(btn, data.h, data.conflicts, data.globalFallback);
    } catch (e) { console.error('Conflict popover error:', e); }
  }

  // #1504 — Path symbols legend (shared by Packets + Nodes pages).
  // Tufte: integrate words and graphics — small, on-data, dismissible.
  // PATH_SYMBOLS_LEGEND mirrors what renderHop() emits (yellow warning+N
  // button, bare warning button, dashed-underline class). Glyphs are
  // pre-rendered Phosphor sprite HTML so the legend visually matches the
  // actual hop chrome (#1648 M3).
  const WARN_PH = '<svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>';
  const PATH_SYMBOLS_LEGEND = [
    { glyphHtml: '<span class="status-warn">' + WARN_PH + 'N</span>',
      description: 'Yellow button next to a hop — N regional candidates share this hop\u2019s prefix. Click for the candidate list.' },
    { glyphHtml: '<span class="status-warn">' + WARN_PH + '</span>',
      description: 'Warning icon alone (no number) — unreliable name resolution: the best-guess pubkey couldn\u2019t be confirmed against surrounding path hops.' },
    { glyphHtml: 'dashed underline',
      description: 'Ambiguous or global-fallback resolution — the name matched outside the current region.' },
  ];

  function renderPathSymbolsLegend() {
    const items = PATH_SYMBOLS_LEGEND.map(function(e) {
      // glyphHtml is trusted (constant strings above), description is escaped.
      return '<li><span class="path-legend-glyph">' + e.glyphHtml + '</span> — ' + escapeHtml(e.description) + '</li>';
    }).join('');
    return '<details class="path-symbols-legend"><summary>Path symbols</summary>' +
           '<ul class="path-legend-list">' + items + '</ul></details>';
  }

  return { renderHop, renderPath, _showFromBtn, PATH_SYMBOLS_LEGEND, renderPathSymbolsLegend };
})();
