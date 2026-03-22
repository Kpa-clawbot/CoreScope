/* === MeshCore Analyzer — hop-display.js === */
/* Shared hop rendering with conflict info for all pages */
'use strict';

window.HopDisplay = (function() {
  function escapeHtml(s) {
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  /**
   * Render a hop prefix as HTML with conflict info.
   * @param {string} h - hex hop prefix
   * @param {Object} [entry] - resolved hop entry from HopResolver or hopNameCache
   * @param {Object} [opts] - { link: true, truncate: 0 }
   */
  function renderHop(h, entry, opts) {
    opts = opts || {};
    if (!entry) entry = {};
    if (typeof entry === 'string') entry = { name: entry };

    const name = entry.name || null;
    const pubkey = entry.pubkey || h;
    const ambiguous = entry.ambiguous || false;
    const conflicts = entry.conflicts || [];
    const totalGlobal = entry.totalGlobal || conflicts.length;
    const totalRegional = entry.totalRegional || 0;
    const globalFallback = entry.globalFallback || false;
    const unreliable = entry.unreliable || false;
    const display = opts.hexMode ? h : (name ? escapeHtml(opts.truncate ? name.slice(0, opts.truncate) : name) : h);

    // Build tooltip
    let title = h;
    if (conflicts.length > 0) {
      const lines = conflicts.map(c => {
        let line = c.name || c.pubkey?.slice(0, 12) || '?';
        if (c.distKm != null) line += ` (${c.distKm}km)`;
        if (c.filterMethod === 'geo') line += ' 📍';
        if (!c.regional) line += ' ⚑global';
        return line;
      });
      const regionLabel = totalRegional > 0 ? `${totalRegional} regional` : `${totalGlobal} global`;
      title = `${h} — ${regionLabel} match${conflicts.length > 1 ? 'es' : ''}:\n${lines.join('\n')}`;
    }
    if (unreliable) title += '\n✗ Unreliable — too far from neighbors';
    if (globalFallback) title += '\n⚑ No regional candidates — global fallback';

    // Badge
    const warnBadge = conflicts.length > 1
      ? `<span class="hop-warn" title="${escapeHtml(title)}">⚠${conflicts.length}</span>`
      : '';

    const cls = [
      'hop',
      name ? 'hop-named' : '',
      ambiguous ? 'hop-ambiguous' : '',
      unreliable ? 'hop-unreliable' : '',
      globalFallback ? 'hop-global-fallback' : '',
    ].filter(Boolean).join(' ');

    if (opts.link !== false) {
      return `<a class="${cls} hop-link" href="#/nodes/${encodeURIComponent(pubkey)}" title="${escapeHtml(title)}" data-hop-link="true">${display}${warnBadge}</a>`;
    }
    return `<span class="${cls}" title="${escapeHtml(title)}">${display}${warnBadge}</span>`;
  }

  /**
   * Render a full path as HTML.
   * @param {string[]} hops - array of hex prefixes
   * @param {Object} cache - hop name cache (hop → entry)
   * @param {Object} [opts] - { link: true, separator: ' → ' }
   */
  function renderPath(hops, cache, opts) {
    opts = opts || {};
    const sep = opts.separator || ' → ';
    if (!hops || !hops.length) return '—';
    return hops.filter(Boolean).map(h => renderHop(h, cache[h], opts)).join(sep);
  }

  return { renderHop, renderPath };
})();
