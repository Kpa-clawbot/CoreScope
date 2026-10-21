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

  function showConflictPopover(btn, h, conflicts, globalFallback, pick) {
    dismissPopover();
    ensureGlobalListener();

    const regional = pick ? [] : conflicts.filter(c => c.regional);
    const shown = regional.length > 0 ? regional : conflicts;

    // An inferred hop (pick) says what the path settled on; a resolve() hop
    // says how its candidates were narrowed by region.
    const count = `${shown.length} candidate${shown.length > 1 ? 's' : ''}`;
    const header = pick
      ? `${escapeHtml(h)} — ${pick.name ? 'likely ' + escapeHtml(pick.name) : 'unlisted node'} (${pct(pick.confidence)}) · ${count}`
      : `${escapeHtml(h)} — ${count}${regional.length > 0 ? ' in region' : (globalFallback ? ' (global fallback)' : '')}`;
    let html = `<div class="hop-conflict-header">${header}</div>`;
    html += '<div class="hop-conflict-list">';
    for (const c of shown) {
      const name = escapeHtml(c.name || c.pubkey?.slice(0, 16) || '?');
      const metric = pick ? (c.located === false ? 'no GPS' : c.usedAt != null ? 'hop ' + c.usedAt : pct(c.confidence))
        : (c.distKm != null ? c.distKm + 'km' : '');
      const dist = metric ? `<span class="hop-conflict-dist">${metric}</span>` : '';
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
  // An inferred hop (HopResolver.inferPath) below this confidence is shown
  // as a guess: dashed underline and a badge to its candidates.
  const LIKELY = 0.7;

  function pct(confidence) {
    return Math.round((confidence || 0) * 100) + '%';
  }

  function conflictButton(h, conflicts, globalFallback, pick, count) {
    const conflictData = escapeHtml(JSON.stringify({ h, conflicts, globalFallback, pick }));
    return ` <button class="hop-conflict-btn status-warn" data-conflict='${conflictData}' onclick="event.preventDefault();event.stopPropagation();HopDisplay._showFromBtn(this)" title="${count} candidates — click for details"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg>${count}</button>`;
  }

  // A hop from inferPath(): one position in one observation's path, with a
  // confidence. Unlisted means no located node with its prefix fits there.
  function renderInferredHop(h, entry, opts) {
    const candidates = entry.candidates || [];
    const name = entry.name || null;
    // A guess is dashed; the badge only appears when there are candidates to
    // weigh: the alternatives to a guess, or those rejected for an unlisted hop.
    const guess = !entry.unlisted && entry.confidence < LIKELY;
    const unsure = entry.unlisted ? !name && candidates.length > 0 : guess && candidates.length > 1;

    let title;
    if (!entry.unlisted) {
      title = `${h} — ${pct(entry.confidence)} likely` +
        (candidates.length > 1 ? ` · ${candidates.length} nodes share this prefix` : '');
    } else if (name) {
      title = `${h} — the only known node with this prefix, no GPS to check its place in the path`;
    } else if (candidates.length) {
      title = `${h} — no listed node with this prefix fits this point in the path`;
    } else {
      title = `${h} — no known node has this prefix`;
    }

    const display = opts.hexMode || !name ? h
      : escapeHtml(opts.truncate ? name.slice(0, opts.truncate) : name);
    const cls = ['hop', name ? 'hop-named' : '', guess || unsure ? 'hop-ambiguous' : '', entry.unlisted ? 'hop-unlisted' : '']
      .filter(Boolean).join(' ');
    const label = name && entry.unlisted ? `${display} <span class="hop-nogps">(no GPS)</span>` : display;
    const pill = opts.link !== false && entry.pubkey
      ? `<a class="${cls} hop-link" href="#/nodes/${encodeURIComponent(entry.pubkey)}" title="${escapeHtml(title)}" data-hop-link="true">${label}</a>`
      : `<span class="${cls}" title="${escapeHtml(title)}">${label}</span>`;

    if (!unsure || opts.badge === false) return pill;
    const pick = { name, confidence: entry.confidence };
    return `<span class="hop-group">${pill}${conflictButton(h, candidates, false, pick, candidates.length)}</span>`;
  }

  function renderHop(h, entry, opts) {
    opts = opts || {};
    if (!entry) entry = {};
    if (typeof entry === 'string') entry = { name: entry };
    if (typeof entry.confidence === 'number') return renderInferredHop(h, entry, opts);

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
    const badgeCount = regionalConflicts.length > 0 ? regionalConflicts.length : ((globalFallback || entry.regionSkipped) ? conflicts.length : 0);
    const conflictBadge = badgeCount > 1 ? conflictButton(h, conflicts, globalFallback, null, badgeCount) : '';
    const unreliableBadge = unreliable
      ? ' <button class="hop-unreliable-btn status-warn" aria-label="Unreliable name resolution" title="Unreliable name resolution — this hash\u2192name match is geographically inconsistent with the surrounding path hops. The repeater itself may be fine; this specific hop assignment is uncertain."><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-warning"/></svg></button>'
      : '';
    // #2097 — opts.badge === false renders the name without its badge, for the
    // packets list, which summarises per path instead of per hop.
    const warnBadge = opts.badge === false ? '' : (conflictBadge + unreliableBadge);

    const cls = [
      'hop',
      name ? 'hop-named' : '',
      ambiguous ? 'hop-ambiguous' : '',
      unreliable ? 'hop-unreliable' : '',
      globalFallback ? 'hop-global-fallback' : '',
    ].filter(Boolean).join(' ');

    const pill = opts.link !== false
      ? `<a class="${cls} hop-link" href="#/nodes/${encodeURIComponent(pubkey)}" title="${escapeHtml(title)}" data-hop-link="true">${display}</a>`
      : `<span class="${cls}" title="${escapeHtml(title)}">${display}</span>`;

    // #2097 — a badge is a sibling of its pill, so in "A [8] → B [6]" nothing
    // said which name the 8 belonged to. Wrapping the pair keeps them together
    // and puts the separator clearly outside. Only when there is a badge:
    // wrapping every hop would change the layout of every path for nothing.
    if (!warnBadge) return pill;
    return `<span class="hop-group">${pill}${warnBadge}</span>`;
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
      showConflictPopover(btn, data.h, data.conflicts, data.globalFallback, data.pick || null);
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
      description: 'Yellow button next to a hop — N candidates share this hop\u2019s prefix and none is a clear match. Click for the candidate list (in a packet\u2019s detail, with how likely each is).' },
    { glyphHtml: '<span class="status-warn">' + WARN_PH + '</span>',
      description: 'Warning icon alone (no number) — unreliable name resolution: the best-guess pubkey couldn\u2019t be confirmed against surrounding path hops.' },
    { glyphHtml: 'dashed underline',
      description: 'Ambiguous or global-fallback resolution — the name matched outside the current region, or is a guess.' },
    { glyphHtml: '<span class="hop-unlisted">5E</span>',
      description: 'Hex prefix in a packet\u2019s detail — unlisted: no known node with this prefix fits this point in the path. It is likely a node missing from the list, or one without GPS.' },
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
