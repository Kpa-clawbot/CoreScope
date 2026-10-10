/* === CoreScope — rx-coverage.js ===
   Mobile RX coverage hub (route #/rx-coverage):
   - global H3-style hex coverage map (all mobile observers), time-windowed
   - leaderboard of top mobile observers (companion name + counts)
   - click an observer to filter the map to just their coverage
   Fork-only feature; isolated page (no changes to the core map). */
'use strict';
(function () {
  var map = null, covLayer = null, days = 7, selectedRx = '', selectedName = '', boardCache = [], destroyed = false, generation = 0;
  // layer: 'signal' (default, per-cell best SNR of directly-heard nodes) or
  // 'noise' (RF noise-floor layer, opt-in — see MC_CLIENT_RF_SAMPLES).
  var layer = 'signal';
  // showGaps: on the signal layer, also draw cells a companion drove through
  // with nothing received (/api/rx-coverage?gaps=1). Needs the RF sample
  // track, so only offered with MC_CLIENT_RF_SAMPLES. Default on.
  var showGaps = true;
  // gapRenderer: an SVG renderer of its own, added before the coverage
  // polygons so gap cells sit underneath them and it can hold the hatch <defs>.
  var gapRenderer = null;

  function gapsAvailable() { return !!window.MC_CLIENT_RF_SAMPLES; }

  function cssColor(varName) {
    try { return getComputedStyle(document.documentElement).getPropertyValue(varName).trim() || '#888'; }
    catch (e) { return '#888'; }
  }
  // SF8 SNR thresholds: ≥ −5 good margin, −9..−5 near the limit, < −9 packet loss
  // likely. Grey = heard but no SNR metric.
  function colorVar(p) {
    if (!p || !p.has_sig || p.best_snr == null) return '--nq-cov-grey';
    var s = Number(p.best_snr);
    if (s >= -5) return '--nq-cov-strong';
    if (s >= -9) return '--nq-cov-mid';
    return '--nq-cov-weak';
  }

  function dayBtn(d) { return '<button data-days="' + d + '"' + (d === days ? ' class="active"' : '') + ' aria-pressed="' + (d === days ? 'true' : 'false') + '">' + (d === 1 ? '24h' : d + 'd') + '</button>'; }

  function layerBtn(k, label) { return '<button data-layer="' + k + '"' + (k === layer ? ' class="active"' : '') + ' aria-pressed="' + (k === layer ? 'true' : 'false') + '">' + label + '</button>'; }

  // NOISE_QUIET_MAX / NOISE_BUSY_MIN (dBm) bucket the noise layer into 3 tiers.
  // Fitted to observed data (#4): 1241 production moving samples
  // (stationary=0) ranged -120..-88, mass concentrated -113..-116. Cumulative
  // counts: <= -115 → 504 (41%), -114..-108 → 581 (47%), >= -107 → ~156 (12%).
  // -115/-108 puts all three tiers within reach of that distribution instead
  // of leaving "busy" almost never rendered.
  var NOISE_QUIET_MAX = -115; // <= this: quiet
  var NOISE_BUSY_MIN = -108;  // >  this: busy

  // noiseColorVar reuses the coverage colour tokens (green=good, red=bad), but
  // the axis is INVERTED versus colorVar's SNR check: for noise floor, a LOW
  // (more negative) dBm reading is quieter/better, so it maps to the "strong"
  // (green) token, and a HIGH reading maps to "weak" (red) -- the opposite
  // comparison direction from colorVar.
  function noiseColorVar(p) {
    var m = Number(p.median_noise_floor);
    if (m <= NOISE_QUIET_MAX) return '--nq-cov-strong';
    if (m <= NOISE_BUSY_MIN) return '--nq-cov-mid';
    return '--nq-cov-weak';
  }

  function legendHtml() {
    if (layer === 'noise') {
      return '<div class="nq-cov-legend" id="rxLegend">' +
        '<span><i style="background:var(--nq-cov-strong)"></i>quiet (≤ ' + NOISE_QUIET_MAX + ' dBm)</span>' +
        '<span><i style="background:var(--nq-cov-mid)"></i>medium</span>' +
        '<span><i style="background:var(--nq-cov-weak)"></i>busy (> ' + NOISE_BUSY_MIN + ' dBm)</span>' +
        '</div>';
    }
    return '<div class="nq-cov-legend" id="rxLegend">' +
      '<span><i style="background:var(--nq-cov-strong)"></i>strong</span>' +
      '<span><i style="background:var(--nq-cov-mid)"></i>medium</span>' +
      '<span><i style="background:var(--nq-cov-weak)"></i>weak</span>' +
      '<span><i style="background:var(--nq-cov-grey)"></i>no signal</span>' +
      (gapsAvailable() && showGaps ? '<span><i class="nq-cov-gap-swatch"></i>driven, nothing received</span>' : '') +
      '</div>';
  }

  function gapsBtnHtml() {
    return '<button id="rxGapsBtn" data-gaps="1"' + (showGaps ? ' class="active"' : '') + ' aria-pressed="' + (showGaps ? 'true' : 'false') + '"' +
      ' title="Cells a companion drove through in this period without receiving anything. This is not proof of no coverage: at the edge of a node\'s range, another drive may well receive something.">Nothing received</button>';
  }

  // gapFillOpacity: denser hatch for cells driven through more often (1 / 2-3 / 4+ samples).
  function gapFillOpacity(p) {
    var n = p ? Number(p.samples) : 0;
    return n >= 4 ? 0.95 : n >= 2 ? 0.75 : 0.55;
  }

  function gapCellHtml(p) {
    if (!p) return '';
    return '<div style="font-weight:600;margin-bottom:4px">Nothing received here in this period</div>' +
      '<div style="font-size:12px;color:var(--text-muted)">' + p.samples + (p.samples === 1 ? ' track sample' : ' track samples') + ' while driving</div>';
  }

  // ensureGapHatch creates the gap renderer once per map and puts the 45-degree
  // hatch pattern in its <svg>, coloured by --nq-cov-gap so themes follow.
  function ensureGapHatch() {
    if (gapRenderer || !map) return;
    gapRenderer = L.svg({ padding: 0.5 }).addTo(map);
    var svg = gapRenderer._container;
    if (!svg) return;
    var ns = 'http://www.w3.org/2000/svg';
    var defs = document.createElementNS(ns, 'defs');
    var pat = document.createElementNS(ns, 'pattern');
    pat.setAttribute('id', 'rxGapHatch');
    pat.setAttribute('patternUnits', 'userSpaceOnUse');
    pat.setAttribute('width', '6');
    pat.setAttribute('height', '6');
    pat.setAttribute('patternTransform', 'rotate(45)');
    var line = document.createElementNS(ns, 'line');
    line.setAttribute('x1', '0'); line.setAttribute('y1', '0'); line.setAttribute('x2', '0'); line.setAttribute('y2', '6');
    line.setAttribute('style', 'stroke:var(--nq-cov-gap);stroke-width:2');
    pat.appendChild(line);
    defs.appendChild(pat);
    svg.insertBefore(defs, svg.firstChild);
  }

  // subtitleHtml reflects the active layer (#7): the "Colour = ..." clause
  // must not keep claiming best-signal colouring while the noise legend (an
  // inverted, opposite-direction colour axis) sits right below it.
  function subtitleHtml() {
    var colourText = layer === 'noise' ? 'Colour = median RF noise floor per cell.' : 'Colour = best signal per cell.';
    return 'Where roaming CoreScope-RX clients heard nodes. ' + colourText + ' <a href="https://github.com/efiten/corescope-rx" target="_blank" rel="noopener">Get the companion app →</a>';
  }

  function pageHtml() {
    var layerBar = window.MC_CLIENT_RF_SAMPLES
      ? '<div class="analytics-time-range" id="rxLayerBar" style="margin:8px 0">' + layerBtn('signal', 'Signal') + layerBtn('noise', 'Noise') + '</div>' +
        '<div class="analytics-time-range" id="rxGapsBar" style="margin:8px 0' + (layer === 'signal' ? '' : ';display:none') + '">' + gapsBtnHtml() + '</div>'
      : '';
    return '<div style="max-width:1100px;margin:0 auto;padding:12px 16px">' +
      '<h2 style="margin:4px 0 2px;font-size:18px"><svg class="ph-icon" aria-hidden="true"><use href="/icons/phosphor-sprite.svg#ph-map-trifold"/></svg> Mobile RX coverage</h2>' +
      '<div style="color:var(--text-muted);font-size:11px" id="rxSubtitle">' + subtitleHtml() + '</div>' +
      '<div class="analytics-time-range" id="rxDays" style="margin:8px 0">' + dayBtn(1) + dayBtn(7) + dayBtn(14) + dayBtn(30) + '</div>' +
      layerBar +
      legendHtml() +
      '<div style="position:relative">' +
      '<div id="rxMap" style="height:60vh;min-height:360px;border:1px solid var(--border,#d0d7de);border-radius:6px;margin:8px 0"></div>' +
      '<div id="rxNoiseEmpty" class="nq-msg" style="display:none;position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);z-index:1000;pointer-events:none">No RF samples in this view yet.</div>' +
      '</div>' +
      '<div class="nq-group-h">Top mobile observers</div>' +
      '<div id="rxBoard" class="rxb"></div>' +
      '</div>';
  }

  // coverageNodeRow renders one heard node: name (or heard_key prefix) + latest SNR + count.
  function coverageNodeRow(n) {
    var label = n.name ? escapeHtml(n.name) : '<code>' + escapeHtml(n.prefix || '?') + '</code>';
    var snr = (n.snr != null) ? Number(n.snr).toFixed(1) + ' dB' : 'no sig';
    return '<div style="display:flex;gap:8px;justify-content:space-between;font-size:12px;line-height:1.6">' +
      '<span>' + label + '</span>' +
      '<span style="color:var(--text-muted)">' + snr + ' · ×' + n.count + '</span></div>';
  }

  // coverageNodesHtml lists the nodes directly heard in a cell (properties.nodes:
  // {prefix, name, snr, count}, strongest latest-SNR first; prefix shown when the
  // name is unresolved). Rendered in the hover tooltip; capped at 10 rows with a
  // "(N more)" footer so dense cells don't produce an unwieldy tooltip.
  var COVERAGE_NODE_CAP = 10;
  function coverageNodesHtml(p) {
    var nodes = (p && p.nodes) || [];
    var head = '<div style="font-weight:600;margin-bottom:4px">' +
      nodes.length + (nodes.length === 1 ? ' node heard here' : ' nodes heard here') + '</div>';
    if (!nodes.length) return head + '<div style="color:var(--text-muted)">n=' + (p ? p.count : 0) + '</div>';
    var rows = nodes.slice(0, COVERAGE_NODE_CAP).map(coverageNodeRow).join('');
    var more = (nodes.length > COVERAGE_NODE_CAP)
      ? '<div style="color:var(--text-muted);font-size:11px;margin-top:3px">(' + (nodes.length - COVERAGE_NODE_CAP) + ' more)</div>'
      : '';
    return head + '<div style="min-width:180px">' + rows + '</div>' + more;
  }

  // fillOpacityFor adds a redundant, non-hue cue to the SNR tier so the map is
  // distinguishable for colour-blind users (orange vs red): stronger signal =
  // more opaque. Pairs with the hue and the per-cell SNR in the tooltip (#a11y).
  function fillOpacityFor(p) {
    switch (colorVar(p)) {
      case '--nq-cov-strong': return 0.6;
      case '--nq-cov-mid': return 0.48;
      case '--nq-cov-weak': return 0.34;
      default: return 0.22;
    }
  }

  // noiseFillOpacityFor mirrors fillOpacityFor for the noise tier (#a11y).
  function noiseFillOpacityFor(p) {
    switch (noiseColorVar(p)) {
      case '--nq-cov-strong': return 0.6;
      case '--nq-cov-mid': return 0.48;
      case '--nq-cov-weak': return 0.34;
      default: return 0.22;
    }
  }

  // noiseCellHtml renders the noise-layer tooltip: sample count, median, and
  // the quietest/noisiest single sample in the cell.
  function noiseCellHtml(p) {
    if (!p) return '';
    return '<div style="font-weight:600;margin-bottom:4px">' + p.count + (p.count === 1 ? ' sample' : ' samples') + '</div>' +
      '<div style="font-size:12px;line-height:1.6;min-width:160px">' +
      '<div>median: ' + Number(p.median_noise_floor).toFixed(1) + ' dBm</div>' +
      '<div style="color:var(--text-muted)">quietest: ' + p.quietest_noise_floor + ' dBm · noisiest: ' + p.noisiest_noise_floor + ' dBm</div>' +
      '</div>';
  }

  function drawSignalLayer(bbox) {
    var wantGaps = gapsAvailable() && showGaps;
    var url = '/api/rx-coverage?bbox=' + bbox + '&z=' + map.getZoom() + '&days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '') + (wantGaps ? '&gaps=1' : '');
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !covLayer || layer !== 'signal') return;
      covLayer.clearLayers();
      setNoiseEmpty(false);
      if (wantGaps && showGaps) {
        ensureGapHatch();
        var gapCol = cssColor('--nq-cov-gap');
        (fc.gaps || []).forEach(function (f) {
          var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; });
          L.polygon(ring, { renderer: gapRenderer, color: gapCol, weight: 0.5, opacity: 0.6, fillColor: 'url(#rxGapHatch)', fillOpacity: gapFillOpacity(f.properties), className: 'rx-gap-cell' })
            .addTo(covLayer).bindTooltip(gapCellHtml(f.properties));
        });
      }
      (fc.features || []).forEach(function (f) {
        var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; });
        var col = cssColor(colorVar(f.properties));
        L.polygon(ring, { color: col, weight: 1, fillColor: col, fillOpacity: fillOpacityFor(f.properties) }).addTo(covLayer)
          .bindTooltip(coverageNodesHtml(f.properties));
      });
    }).catch(function (e) {
      console.warn('rx-coverage: coverage fetch failed', e);
      // #1: never leave stale hexes from a previous layer/view on screen.
      if (!destroyed && covLayer && layer === 'signal') covLayer.clearLayers();
    });
  }

  function drawNoiseLayer(bbox) {
    var url = '/api/rf-noise?bbox=' + bbox + '&z=' + map.getZoom() + '&days=' + days;
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !covLayer || layer !== 'noise') return;
      covLayer.clearLayers();
      var features = fc.features || [];
      setNoiseEmpty(features.length === 0);
      features.forEach(function (f) {
        var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; });
        var col = cssColor(noiseColorVar(f.properties));
        L.polygon(ring, { color: col, weight: 1, fillColor: col, fillOpacity: noiseFillOpacityFor(f.properties) }).addTo(covLayer)
          .bindTooltip(noiseCellHtml(f.properties));
      });
    }).catch(function (e) {
      console.warn('rx-coverage: noise fetch failed', e);
      // #1/#2: a slow/failing request must not leave the map showing the
      // other layer's hexes under the noise legend, nor sit there blank and
      // unlabelled indistinguishable from "no samples in this view".
      if (destroyed || !covLayer || layer !== 'noise') return;
      covLayer.clearLayers();
      setNoiseEmpty(true, 'Could not load noise samples. Pan or zoom to retry.');
    });
  }

  // setNoiseEmpty toggles the "no samples in this view" message. Only shown
  // on the noise layer — MC_CLIENT_RF_SAMPLES already keeps the toggle (and
  // so this layer) out of reach entirely when the feature is off, so seeing
  // this message always means "on", never "feature disabled" (the two states
  // this project keeps needing to keep distinct). `text` overrides the
  // default no-samples copy for the fetch-failed case (#2), which must read
  // as distinctly different — and retryable — from "no samples here yet".
  function setNoiseEmpty(show, text) {
    var el = document.getElementById('rxNoiseEmpty');
    if (!el) return;
    if (show && layer === 'noise') {
      el.textContent = text || 'No RF samples in this view yet.';
      el.style.display = 'block';
    } else {
      el.style.display = 'none';
    }
  }

  function drawCoverage() {
    if (!map || destroyed) return;
    var b = map.getBounds();
    var bbox = [b.getSouth(), b.getWest(), b.getNorth(), b.getEast()].join(',');
    if (layer === 'noise') drawNoiseLayer(bbox);
    else drawSignalLayer(bbox);
  }

  // Leaderboard sort state. Default = frontier score, descending. The rank (#)
  // column is not sortable (it just reflects the current order). Numeric columns
  // default to descending on first click; the name column to ascending.
  var boardSort = { key: 'score', dir: 'desc' };
  var BOARD_COLS = [
    { key: 'name', label: 'Observer (companion)', cls: 'rxb-name' },
    { key: 'score', label: 'score', cls: 'rxb-score',
      title: 'Score telt je gedekte cellen, waarbij elke cel zwaarder weegt naarmate minder andere waarnemers ze bereikt hebben — grensverleggende dekking weegt meer dan drukke zones opnieuw afrijden.' },
    { key: 'cells', label: 'cells', cls: 'rxb-cells',
      title: 'Aantal unieke ~150 m-cellen waar deze waarnemer iets hoorde.' },
    { key: 'nodes', label: 'nodes', cls: 'rxb-nodes' },
    { key: 'receptions', label: 'pkts', cls: 'rxb-rec' }
  ];

  function sortBoard() {
    var k = boardSort.key, dir = boardSort.dir === 'asc' ? 1 : -1;
    boardCache.sort(function (a, b) {
      if (k === 'name') {
        var an = (a.name || a.pubkey).toLowerCase(), bn = (b.name || b.pubkey).toLowerCase();
        return an < bn ? -dir : an > bn ? dir : 0;
      }
      return (Number(a[k]) - Number(b[k])) * dir;
    });
  }

  function boardHeadHtml() {
    var cells = BOARD_COLS.map(function (c) {
      var arrow = boardSort.key === c.key ? (boardSort.dir === 'asc' ? ' ▲' : ' ▼') : '';
      return '<span class="' + c.cls + ' rxb-sort" data-sort="' + c.key + '"' +
        (c.title ? ' title="' + escapeHtml(c.title) + '"' : '') +
        ' role="button" tabindex="0">' + escapeHtml(c.label) + arrow + '</span>';
    }).join('');
    return '<div class="rxb-row rxb-head"><span class="rxb-rank">#</span>' + cells + '</div>';
  }

  function renderBoard() {
    var el = document.getElementById('rxBoard');
    if (!el) return;
    if (!boardCache.length) { el.innerHTML = '<div class="muted" style="color:var(--text-muted);font-size:13px">No mobile observers in this window yet.</div>'; return; }
    sortBoard();
    var rows = boardCache.map(function (o, i) {
      var nm = o.name ? escapeHtml(o.name) : (escapeHtml(o.pubkey.slice(0, 10)) + '…');
      return '<div class="rxb-row' + (o.pubkey === selectedRx ? ' sel' : '') + '" data-rx="' + escapeHtml(o.pubkey) + '" data-name="' + escapeHtml(o.name || '') + '"' +
        ' role="button" tabindex="0" aria-pressed="' + (o.pubkey === selectedRx ? 'true' : 'false') + '" aria-label="Show coverage for ' + escapeHtml(o.name || o.pubkey.slice(0, 10)) + '">' +
        '<span class="rxb-rank">' + (i + 1) + '</span><span class="rxb-name">' + nm + '</span>' +
        '<span class="rxb-score">' + Number(o.score).toFixed(1) + '</span>' +
        '<span class="rxb-cells">' + o.cells + '</span>' +
        '<span class="rxb-nodes">' + o.nodes + '</span>' +
        '<span class="rxb-rec">' + o.receptions + '</span></div>';
    }).join('');
    el.innerHTML = (selectedRx ? '<button id="rxAll" class="btn-primary" style="margin:0 0 8px">← Show all observers</button>' : '') +
      boardHeadHtml() + rows;
    // Column sort handlers (click + keyboard).
    el.querySelectorAll('.rxb-sort[data-sort]').forEach(function (h) {
      function applySort() {
        var k = h.dataset.sort;
        if (boardSort.key === k) {
          boardSort.dir = boardSort.dir === 'asc' ? 'desc' : 'asc';
        } else {
          boardSort.key = k;
          boardSort.dir = (k === 'name') ? 'asc' : 'desc';
        }
        renderBoard();
      }
      h.addEventListener('click', applySort);
      h.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') { e.preventDefault(); applySort(); }
      });
    });
    // Row click-to-filter (preserved from the original).
    el.querySelectorAll('.rxb-row[data-rx]').forEach(function (r) {
      function activate() {
        selectedRx = r.dataset.rx; selectedName = r.dataset.name || '';
        renderBoard(); fitToObserver(); syncHash();
      }
      r.addEventListener('click', activate);
      r.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') { e.preventDefault(); activate(); }
      });
    });
    var all = document.getElementById('rxAll');
    if (all) all.addEventListener('click', function () { selectedRx = ''; selectedName = ''; renderBoard(); drawCoverage(); syncHash(); });
  }

  // fitToObserver zooms the map to the selected observer's full coverage extent
  // (fetched with a world bbox so it's independent of the current view), then the
  // resulting moveend redraws the hexes at the fitted resolution.
  function fitToObserver() {
    if (!map || !selectedRx) { drawCoverage(); return; }
    var url = '/api/rx-coverage?bbox=-90,-180,90,180&z=' + Math.max(8, map.getZoom()) + '&days=' + days + '&rx=' + encodeURIComponent(selectedRx);
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !map) return;
      var minLat = 90, minLon = 180, maxLat = -90, maxLon = -180, any = false;
      (fc.features || []).forEach(function (f) {
        (f.geometry.coordinates[0] || []).forEach(function (c) {
          any = true;
          if (c[1] < minLat) minLat = c[1]; if (c[1] > maxLat) maxLat = c[1];
          if (c[0] < minLon) minLon = c[0]; if (c[0] > maxLon) maxLon = c[0];
        });
      });
      if (!any) { drawCoverage(); return; } // observer has no data in window → keep view
      map.fitBounds([[minLat, minLon], [maxLat, maxLon]], { padding: [30, 30], maxZoom: 15 });
      drawCoverage(); // fitBounds may not fire moveend if the view is unchanged
    }).catch(function (e) { console.warn('rx-coverage: observer extent fetch failed', e); drawCoverage(); });
  }

  function loadBoard() {
    fetch('/api/rx-leaderboard?days=' + days + '&limit=25').then(function (r) { return r.json(); })
      .then(function (d) { if (destroyed) return; boardCache = d.observers || []; renderBoard(); })
      .catch(function (e) {
        console.warn('rx-coverage: leaderboard fetch failed', e);
        var el = document.getElementById('rxBoard');
        if (el) el.innerHTML = '<div class="muted" style="color:var(--text-muted);font-size:13px">Could not load mobile observers.</div>';
      });
  }

  function setDays(d) {
    days = d;
    var bar = document.getElementById('rxDays');
    if (bar) bar.querySelectorAll('button').forEach(function (b) { b.classList.toggle('active', +b.dataset.days === d); });
    loadBoard(); drawCoverage(); syncHash();
  }

  // setLayer switches between the 'signal' (SNR) and 'noise' hex layers. Only
  // reachable when MC_CLIENT_RF_SAMPLES rendered the toggle in the first place.
  function setLayer(l) {
    if (l === layer) return;
    layer = l;
    // Clear the old layer's polygons synchronously (#1): the legend swap
    // below is synchronous too, so a slow/failed fetch must never leave the
    // previous layer's hexes on screen under the new layer's legend/tooltips.
    if (covLayer) covLayer.clearLayers();
    var bar = document.getElementById('rxLayerBar');
    if (bar) bar.querySelectorAll('button').forEach(function (b) { b.classList.toggle('active', b.dataset.layer === l); });
    var legend = document.getElementById('rxLegend');
    if (legend) legend.outerHTML = legendHtml();
    var subtitle = document.getElementById('rxSubtitle');
    if (subtitle) subtitle.innerHTML = subtitleHtml();
    var gapsBar = document.getElementById('rxGapsBar');
    if (gapsBar) gapsBar.style.display = l === 'signal' ? '' : 'none';
    setNoiseEmpty(false);
    drawCoverage(); syncHash();
  }

  // setGaps toggles the "nothing received" cells on the signal layer.
  function setGaps(on) {
    showGaps = on;
    var btn = document.getElementById('rxGapsBtn');
    if (btn) { btn.classList.toggle('active', on); btn.setAttribute('aria-pressed', on ? 'true' : 'false'); }
    var legend = document.getElementById('rxLegend');
    if (legend) legend.outerHTML = legendHtml();
    if (covLayer) covLayer.clearLayers();
    drawCoverage(); syncHash();
  }

  function syncHash() {
    var q = 'days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '') + (layer !== 'signal' ? '&layer=' + layer : '') + (gapsAvailable() && !showGaps ? '&gaps=0' : '');
    if (map) {
      var c = map.getCenter();
      q += '&lat=' + c.lat.toFixed(5) + '&lon=' + c.lng.toFixed(5) + '&zoom=' + map.getZoom();
    }
    try { history.replaceState(null, '', '#/rx-coverage?' + q); } catch (e) {}
  }

  function init(container) {
    destroyed = false;
    var current = ++generation;
    // A direct land on #/rx-coverage can run before MeshConfigReady resolves, at
    // which point MC_CLIENT_RX_COVERAGE is still undefined and the page would
    // wrongly show "not enabled". Defer until server config is loaded (#13).
    Promise.resolve(window.MeshConfigReady).then(function () {
      if (!destroyed && current === generation) start(container, current);
    });
  }

  async function start(container, current) {
    if (!window.MC_CLIENT_RX_COVERAGE) {
      container.innerHTML = '<div class="nq-msg">Coverage is not enabled on this deployment.</div>';
      return;
    }
    selectedRx = ''; selectedName = ''; days = 7; boardCache = []; layer = 'signal'; showGaps = true;
    try {
      var p = (typeof getHashParams === 'function') ? getHashParams() : null;
      if (p) {
        var dd = parseInt(p.get('days'), 10); if ([1, 7, 14, 30].indexOf(dd) >= 0) days = dd;
        selectedRx = (p.get('rx') || '').toLowerCase();
        if (window.MC_CLIENT_RF_SAMPLES && p.get('layer') === 'noise') layer = 'noise';
        if (p.get('gaps') === '0') showGaps = false;
      }
    } catch (e) {}
    container.innerHTML = pageHtml();
    // Initialize viewport: explicit URL hash, coverage page's saved position, deployment defaults (#2032).
    var viewport = parseViewportHash(location.hash);
    var explicitViewport = !!viewport;
    if (!viewport) {
      try {
        var saved = JSON.parse(localStorage.getItem('rx-coverage-view'));
        if (saved && saved.lat != null && saved.lng != null && saved.zoom != null) {
          viewport = parseViewportHash(new URLSearchParams({ lat: saved.lat, lon: saved.lng, zoom: saved.zoom }).toString());
        }
      } catch (e) {} // Storage may be disabled or contain an incomplete value.
    }
    if (!viewport) {
      viewport = { lat: 37.6, lon: -122.1, zoom: 9 };
      try {
        var response = await fetch('/api/config/map');
        var cfg = await response.json();
        if (cfg && Array.isArray(cfg.center) && cfg.center.length === 2) {
          viewport = parseViewportHash(new URLSearchParams({ lat: cfg.center[0], lon: cfg.center[1], zoom: cfg.zoom == null ? 9 : cfg.zoom }).toString()) || viewport;
        }
      } catch (e) {} // Match the main map's offline fallback.
    }
    if (destroyed || current !== generation) return;
    map = L.map('rxMap', { zoomControl: true, attributionControl: false }).setView([viewport.lat, viewport.lon], viewport.zoom);
    if (typeof window._applyTilesToNodeMap === 'function') window._applyTilesToNodeMap(map);
    else L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    covLayer = L.layerGroup().addTo(map);
    // Debounce pan/zoom redraws so dragging the map doesn't fire a storm of
    // /api/rx-coverage requests (#6). Direct calls (setDays, fit) stay immediate.
    map.on('moveend zoomend', debounce(function () {
      if (destroyed || current !== generation || !map) return;
      var center = map.getCenter();
      try { localStorage.setItem('rx-coverage-view', JSON.stringify({ lat: center.lat, lng: center.lng, zoom: map.getZoom() })); } catch (e) {}
      syncHash();
      drawCoverage();
    }, 200));
    var bar = document.getElementById('rxDays');
    if (bar) bar.addEventListener('click', function (e) { var b = e.target.closest('button[data-days]'); if (b) setDays(+b.dataset.days); });
    var layerBar = document.getElementById('rxLayerBar');
    if (layerBar) layerBar.addEventListener('click', function (e) { var b = e.target.closest('button[data-layer]'); if (b) setLayer(b.dataset.layer); });
    var gapsBtn = document.getElementById('rxGapsBtn');
    if (gapsBtn) gapsBtn.addEventListener('click', function () { setGaps(!showGaps); });
    setTimeout(function () { if (!destroyed && current === generation && map) { map.invalidateSize(); if (selectedRx && !explicitViewport) fitToObserver(); else drawCoverage(); } }, 150);
    loadBoard();
  }

  function destroy() {
    destroyed = true;
    generation++;
    if (map) { try { map.remove(); } catch (e) {} map = null; }
    covLayer = null;
    gapRenderer = null;
  }

  registerPage('rx-coverage', { init: init, destroy: destroy });
})();
