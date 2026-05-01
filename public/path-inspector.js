// Path Inspector — prefix candidate scoring with map overlay (issue #944).
// IIFE; exports window.PathInspector for testability.
(function () {
  'use strict';

  var container = null;
  var currentResults = null;

  function init(app) {
    container = app;
    var params = new URLSearchParams(location.hash.split('?')[1] || '');
    var prefixParam = params.get('prefixes') || '';

    container.innerHTML =
      '<div class="path-inspector-page">' +
        '<h2>Path Inspector</h2>' +
        '<p class="help-text">Enter comma or space-separated hex prefixes (1-3 bytes each, e.g. <code>2C,A1,F4</code> or <code>2C A1 F4</code>).</p>' +
        '<div class="path-inspector-input-row">' +
          '<input type="text" id="path-inspector-input" class="input" placeholder="2C,A1,F4 or 2C A1 F4" value="' + escapeAttr(prefixParam) + '">' +
          '<button id="path-inspector-submit" class="btn btn-primary">Inspect</button>' +
        '</div>' +
        '<div id="path-inspector-error" class="path-inspector-error"></div>' +
        '<div id="path-inspector-results"></div>' +
      '</div>';

    var input = document.getElementById('path-inspector-input');
    var btn = document.getElementById('path-inspector-submit');
    btn.addEventListener('click', function () { submit(input.value); });
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') submit(input.value);
    });

    // Auto-run if prefixes in URL.
    if (prefixParam) submit(prefixParam);
  }

  function destroy() {
    container = null;
    currentResults = null;
  }

  function parsePrefixes(raw) {
    // Accept comma or space separated.
    var parts = raw.trim().split(/[\s,]+/).filter(function (s) { return s.length > 0; });
    return parts.map(function (p) { return p.toLowerCase(); });
  }

  function validatePrefixes(prefixes) {
    if (prefixes.length === 0) return 'Enter at least one prefix.';
    if (prefixes.length > 64) return 'Too many prefixes (max 64).';
    var hexRe = /^[0-9a-f]+$/;
    var byteLen = -1;
    for (var i = 0; i < prefixes.length; i++) {
      var p = prefixes[i];
      if (!hexRe.test(p)) return 'Invalid hex: ' + p;
      if (p.length % 2 !== 0) return 'Odd-length prefix: ' + p;
      var bl = p.length / 2;
      if (bl > 3) return 'Prefix too long (max 3 bytes): ' + p;
      if (byteLen === -1) byteLen = bl;
      else if (bl !== byteLen) return 'Mixed prefix lengths not allowed.';
    }
    return null;
  }

  function submit(raw) {
    var errDiv = document.getElementById('path-inspector-error');
    var resultsDiv = document.getElementById('path-inspector-results');
    errDiv.textContent = '';
    resultsDiv.innerHTML = '';

    var prefixes = parsePrefixes(raw);
    var err = validatePrefixes(prefixes);
    if (err) {
      errDiv.textContent = err;
      return;
    }

    // Update URL.
    var base = '#/tools/path-inspector';
    if (location.hash.indexOf(base) === 0) {
      history.replaceState(null, '', base + '?prefixes=' + prefixes.join(','));
    }

    resultsDiv.innerHTML = '<p>Loading...</p>';
    fetch('/api/paths/inspect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prefixes: prefixes })
    })
      .then(function (r) {
        if (r.status === 503) return r.json().then(function (d) { throw new Error('Service warming up, retry in a few seconds.'); });
        if (!r.ok) return r.json().then(function (d) { throw new Error(d.error || 'Request failed'); });
        return r.json();
      })
      .then(function (data) {
        currentResults = data;
        renderResults(data, resultsDiv);
      })
      .catch(function (e) {
        resultsDiv.innerHTML = '';
        errDiv.textContent = e.message;
      });
  }

  function renderResults(data, div) {
    if (!data.candidates || data.candidates.length === 0) {
      div.innerHTML = '<p class="no-results">No candidates found. The prefixes may not match any known path-eligible nodes.</p>';
      return;
    }

    var html = '<table class="path-inspector-table"><thead><tr>' +
      '<th>#</th><th>Score</th><th>Path</th><th>Action</th>' +
      '</tr></thead><tbody>';

    for (var i = 0; i < data.candidates.length; i++) {
      var c = data.candidates[i];
      var rowClass = c.speculative ? 'speculative-row' : '';
      html += '<tr class="' + rowClass + '">';
      html += '<td>' + (i + 1) + '</td>';
      html += '<td class="' + (c.speculative ? 'speculative-warning' : '') + '">' +
        c.score.toFixed(3) +
        (c.speculative ? ' <span class="speculative-badge" title="Low evidence; may be wrong">⚠</span>' : '') +
        '</td>';
      html += '<td>' + escapeHtml(c.names.join(' → ')) + '</td>';
      html += '<td><button class="btn btn-sm" data-idx="' + i + '">Show on Map</button></td>';
      html += '</tr>';

      // Per-hop evidence (collapsed).
      html += '<tr class="evidence-row collapsed" data-evidence="' + i + '"><td colspan="4"><div class="evidence-detail">';
      for (var j = 0; j < c.evidence.perHop.length; j++) {
        var h = c.evidence.perHop[j];
        html += '<div class="hop-evidence">Hop ' + (j + 1) + ': prefix=' + h.prefix +
          ', candidates=' + h.candidatesConsidered +
          ', edge=' + h.edgeWeight.toFixed(3) + '</div>';
      }
      html += '</div></td></tr>';
    }

    html += '</tbody></table>';
    html += '<div class="path-inspector-stats">Beam width: ' + data.stats.beamWidth +
      ' | Expansions: ' + data.stats.expansionsRun +
      ' | Elapsed: ' + data.stats.elapsedMs + 'ms</div>';

    div.innerHTML = html;

    // Wire up Show on Map buttons.
    div.querySelectorAll('button[data-idx]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var idx = parseInt(btn.dataset.idx);
        showOnMap(data.candidates[idx]);
      });
    });

    // Wire up row expand for evidence.
    div.querySelectorAll('.path-inspector-table tbody tr:not(.evidence-row)').forEach(function (row) {
      row.style.cursor = 'pointer';
      row.addEventListener('click', function (e) {
        if (e.target.tagName === 'BUTTON') return;
        var idx = row.querySelector('button[data-idx]');
        if (!idx) return;
        var evidenceRow = div.querySelector('tr[data-evidence="' + idx.dataset.idx + '"]');
        if (evidenceRow) evidenceRow.classList.toggle('collapsed');
      });
    });
  }

  function showOnMap(candidate) {
    // Navigate to map and draw route.
    if (window.drawPacketRoute) {
      // Clear prior route, draw new one.
      if (window.routeLayer) window.routeLayer.clearLayers();
      var hops = candidate.path.slice(1);
      var origin = candidate.path[0] || null;
      window.drawPacketRoute(hops, origin);
    }
    // Switch to map page if not there.
    if (location.hash.indexOf('#/map') !== 0) {
      // Store pending route to draw after map init.
      window._pendingPathInspectorRoute = candidate;
      location.hash = '#/map';
    }
  }

  function escapeAttr(s) {
    return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
  }

  function escapeHtml(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  window.PathInspector = { init: init, destroy: destroy, parsePrefixes: parsePrefixes, validatePrefixes: validatePrefixes };
  if (typeof registerPage === 'function') registerPage('path-inspector', { init: init, destroy: destroy });
})();
