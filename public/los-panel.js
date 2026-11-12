/* los-panel.js — the docked terrain-profile panel shared by route line of
 * sight (route-los.js) and the repeater planner (planner.js).
 *
 * Holds what both tools draw the same way: the panel shell (title, ‹ n of m ›,
 * close, footer), the markup of one profile around MeshLOS.renderProfileSvg
 * (meta line, verdict pill, obstruction note, chart or its loading / error
 * state, readout), the chart ↔ map crosshair and the map pieces that go with
 * the panel, plus the number formatters and an LRU for leg terrain. Wording
 * that differs per tool (verdict labels, clearance phrasing, error reasons)
 * stays in the tool and comes in as options.
 *
 * Needs los.js (window.MeshLOS) first; the map helpers need Leaflet when
 * they run. Exposes window.MeshLOSPanel.
 */
(function () {
  'use strict';

  var LOS = window.MeshLOS;
  var esc = LOS.esc;
  var SPRITE = '/icons/phosphor-sprite.svg';
  var MOBILE_MAX_W = 767;       // the @media (max-width: 767px) rules of both stylesheets
  var SEP = '<span class="mc-los-sep" aria-hidden="true">·</span>';

  // ─── Formatters ─────────────────────────────────────────────────────────

  // "12.3 km" (2 / 1 / 0 decimals by size); '—' when unknown.
  function fmtKm(x) {
    var d = Number(x);
    if (x == null || x === '' || !isFinite(d)) return '—';
    var a = Math.abs(d);
    return (a < 10 ? d.toFixed(2) : a < 100 ? d.toFixed(1) : String(Math.round(d))) + ' km';
  }

  // "123 m"; '—' when unknown.
  function fmtM(x) {
    var v = Number(x);
    return x != null && x !== '' && isFinite(v) ? Math.round(v) + ' m' : '—';
  }

  // Terrain above the line in whole metres, never "0 m" for a real obstruction.
  function fmtObstruction(m) { return Math.max(1, Math.round(m)) + ' m'; }

  // ─── Leg cache ──────────────────────────────────────────────────────────

  // A Map capped at max entries. get() and put() make the key the newest;
  // put() evicts the oldest once full. get() of a missing key is null.
  function createLRU(max) {
    var cap = Math.max(1, Math.floor(Number(max)) || 1);
    var m = new Map();
    return {
      get: function (k) {
        if (!m.has(k)) return null;
        var v = m.get(k);
        m.delete(k);
        m.set(k, v);
        return v;
      },
      put: function (k, v) {
        if (m.has(k)) m.delete(k);
        while (m.size >= cap) m.delete(m.keys().next().value);
        m.set(k, v);
      },
      has: function (k) { return m.has(k); },
      delete: function (k) { return m.delete(k); },
      size: function () { return m.size; }
    };
  }

  // ─── Environment ────────────────────────────────────────────────────────

  function raf(fn) {
    return typeof window.requestAnimationFrame === 'function' ? window.requestAnimationFrame(fn) : setTimeout(fn, 16);
  }

  function caf(id) {
    if (typeof window.cancelAnimationFrame === 'function') window.cancelAnimationFrame(id);
    else clearTimeout(id);
  }

  function isMobile() { return window.innerWidth <= MOBILE_MAX_W; }

  function reducedMotion() {
    try { return window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) { return false; }
  }

  function icon(name) {
    return '<svg class="ph-icon" aria-hidden="true"><use href="' + SPRITE + '#' + name + '"/></svg>';
  }

  // ─── Profile markup (every mesh-controlled string goes through esc) ────

  // v = { tone, label, title } once analysed; otherwise a muted "n/a" for a
  // failed leg or "Checking…" while it loads.
  function pillHtml(v, failed) {
    if (v) return '<span class="mc-los-pill mc-los-tone-' + esc(v.tone) + '" title="' + esc(v.title) + '">' + esc(v.label) + '</span>';
    return '<span class="mc-los-pill mc-los-tone-muted' + (failed ? '' : ' mc-los-loading') + '">' + (failed ? 'n/a' : 'Checking…') + '</span>';
  }

  function metaHtml(o) {
    var an = o.an;
    var items = ['<span title="Link length">' + esc(fmtKm(an ? an.distanceKm : o.km)) + '</span>'].concat(o.meta || []);
    if (an) {
      items.push('<span title="Free-space path loss">FSPL ' + an.fsplDb.toFixed(1) + ' dB</span>');
      items.push('<span title="Worst clearance of the first Fresnel zone">' + esc(o.clearance) + '</span>');
    }
    return '<p class="mc-los-meta">' + items.join(SEP) + pillHtml(an ? o.pill : null, o.error != null) + '</p>';
  }

  function obstructionHtml(an, nameA) {
    if (!an || !(an.obstructionM > 0)) return '';
    return '<p class="mc-los-obst">Terrain rises ' + fmtObstruction(an.obstructionM) + ' above the line of sight ' +
      esc(fmtKm(an.worstKm)) + ' from ' + esc(nameA) + '.</p>';
  }

  // Contents of a failed chart's box: a headline, the reason (optional) and
  // any extra markup, such as a retry button.
  function chartErrorHtml(headline, reason, extra) {
    return '<strong>' + esc(headline) + '</strong>' + (reason ? '<span>' + esc(reason) + '.</span>' : '') + (extra || '');
  }

  // The chart and its readout once analysed, else the error (o.error) or
  // loading state; loading reserves the chart's full box, an error far less.
  function chartHtml(o) {
    if (o.an) {
      return '<div class="mc-los-chart">' + LOS.renderProfileSvg(o.an, { nameA: o.nameA, nameB: o.nameB }) + '</div>' +
        '<p class="mc-los-readout">' + esc(o.hint) + '</p>';
    }
    if (o.error != null) {
      return '<div class="mc-los-chart mc-los-chart-err"><div class="mc-los-chart-state mc-los-chart-error" role="status">' + o.error + '</div></div>';
    }
    return '<div class="mc-los-chart mc-los-chart-wait"><div class="mc-los-chart-state" role="status">Loading terrain profile…</div></div>';
  }

  // One profile: meta line, obstruction note, chart, readout. o: {
  //   an         MeshLOS.analyze() result; null while loading or failed
  //   km         link length shown without an analysis
  //   nameA, nameB  end names (mesh data)
  //   meta       extra HTML items for the meta line, after the length
  //   clearance  F1 clearance text, once analysed
  //   pill       { tone, label, title } verdict pill, once analysed
  //   error      chartErrorHtml() of a failed leg; null while loading
  //   hint       readout text until the pointer is on the chart }
  function profileHtml(o) {
    return metaHtml(o) + obstructionHtml(o.an, o.nameA) + chartHtml(o);
  }

  // Readout for sample i: distance from A, terrain, LOS height and Fresnel
  // clearance (fmtClearance gets the ratio; '—' at the two ends).
  function readoutHtml(an, i, nameA, fmtClearance) {
    var edge = i === 0 || i === an.n - 1;
    var c = (an.losM[i] - an.terrainM[i]) / an.fresnelM[i];
    return [
      '<b>' + esc(fmtKm(i / (an.n - 1) * an.distanceKm)) + '</b> from ' + esc(nameA),
      'terrain <b>' + fmtM(an.terrainM[i]) + '</b>',
      'LOS <b>' + fmtM(an.losM[i]) + '</b>',
      'clearance <b>' + (edge ? '—' : esc(fmtClearance(c))) + '</b>'
    ].map(function (s) { return '<span class="mc-los-ri">' + s + '</span>'; }).join(SEP);
  }

  // ─── Panel shell ────────────────────────────────────────────────────────

  // A dialog with a title, ‹ position › and close buttons, o.bodyHtml and a
  // footer. o: { className, ariaLabel, prevLabel, nextLabel, closeLabel,
  // bodyHtml, onStep(dir), onClose(), onEscape() (optional: Escape inside
  // the panel) }. Clicks, drags and wheel inside it never reach the map.
  function buildPanel(o) {
    var el = document.createElement('div');
    el.className = 'mc-los-panel' + (o.className ? ' ' + o.className : '');
    el.setAttribute('role', 'dialog');
    if (o.ariaLabel) el.setAttribute('aria-label', o.ariaLabel);
    el.tabIndex = -1;
    el.innerHTML =
      '<div class="mc-los-head">' +
        '<h2 class="mc-los-title"></h2>' +
        '<div class="mc-los-nav">' +
          '<button type="button" class="mc-los-btn mc-los-prev" aria-label="' + esc(o.prevLabel) + '">' + icon('ph-caret-left') + '</button>' +
          '<span class="mc-los-pos"></span>' +
          '<button type="button" class="mc-los-btn mc-los-next" aria-label="' + esc(o.nextLabel) + '">' + icon('ph-caret-right') + '</button>' +
          '<button type="button" class="mc-los-btn mc-los-close" aria-label="' + esc(o.closeLabel) + '">' + icon('ph-x') + '</button>' +
        '</div>' +
      '</div>' +
      (o.bodyHtml || '') +
      '<p class="mc-los-foot"></p>';

    function step(btn, dir) {
      o.onStep(dir);
      // Stepping onto the first or last leg disables the button just used and
      // the browser drops its focus to <body>; keep focus in the panel.
      if (btn.disabled && el.parentNode) el.focus({ preventScroll: true });
    }
    var prev = el.querySelector('.mc-los-prev'), next = el.querySelector('.mc-los-next');
    prev.addEventListener('click', function () { step(prev, -1); });
    next.addEventListener('click', function () { step(next, 1); });
    el.querySelector('.mc-los-close').addEventListener('click', function () { o.onClose(); });
    if (o.onEscape) {
      el.addEventListener('keydown', function (e) {
        if (e.key !== 'Escape') return;
        e.stopPropagation();
        e.preventDefault();
        o.onEscape();
      });
    }
    if (typeof L !== 'undefined' && L.DomEvent) {
      L.DomEvent.disableClickPropagation(el);
      L.DomEvent.disableScrollPropagation(el);
    }
    return el;
  }

  // ‹ "Leg 2 of 3" › for leg pos of count; a single leg shows close only.
  function setNav(el, pos, count, noun) {
    var many = count > 1;
    el.querySelector('.mc-los-nav').classList.toggle('mc-los-single', !many);
    el.querySelector('.mc-los-prev').disabled = !many || pos <= 0;
    el.querySelector('.mc-los-next').disabled = !many || pos >= count - 1;
    el.querySelector('.mc-los-pos').textContent = many ? noun + ' ' + (pos + 1) + ' of ' + count : '';
  }

  // host is the map container; mc-los-open marks it while the panel is docked.
  function mountPanel(el, host) {
    host.appendChild(el);
    host.classList.add('mc-los-open');
  }

  function unmountPanel(el, host) {
    var h = host || (el && el.parentNode);
    if (el && el.parentNode) el.parentNode.removeChild(el);
    if (h && h.classList) h.classList.remove('mc-los-open');
  }

  // fitBounds options that keep a fitted leg clear of the docked panel
  // (top on a phone, bottom on a desktop).
  function fitOptions(map, el) {
    var size = map.getSize();
    var pad = Math.max(0, Math.min((el.offsetHeight || 0) + 24, size.y - 120));
    var o = isMobile()
      ? { paddingTopLeft: [24, pad + 16], paddingBottomRight: [24, 24] }
      : { paddingTopLeft: [48, 48], paddingBottomRight: [48, pad + 40] };
    o.maxZoom = 14;
    o.animate = !reducedMotion();
    return o;
  }

  // ─── Chart ↔ map crosshair ──────────────────────────────────────────────

  function hitOf(n) { return n && n.closest ? n.closest('.mc-los-hit') : null; }

  // 0..1 along the link under clientX on a profile <svg>; null off-screen.
  function tAtClientX(svg, clientX) {
    if (!svg) return null;
    var rect = svg.getBoundingClientRect();
    if (!rect.width) return null;
    return LOS.tAtViewX((clientX - rect.left) / rect.width * LOS.PLOT.W);
  }

  // The sample nearest t, and t snapped onto it so the readout matches the
  // plotted point: { i, t }.
  function snap(an, t) {
    var i = Math.round(t * (an.n - 1));
    return { i: i, t: i / (an.n - 1) };
  }

  // Pointer tracking for the profile charts inside root, delegated so charts
  // re-rendered through innerHTML need no re-binding; moves are coalesced to
  // one per frame. o: {
  //   keyOf(hit)   which chart a .mc-los-hit is (null: ignore); default 0
  //   svgOf(key)   that chart's <svg> now; default root's only one
  //   move(key, t) t = 0..1 along the link under the pointer
  //   leave()      a mouse or pen left the plot (a touch keeps its reading) }
  // Returns { cancel() }, which drops a pending frame.
  function bindCrosshair(root, o) {
    var ptr = null, rafId = 0;
    function cancel() {
      if (rafId) caf(rafId);
      rafId = 0;
      ptr = null;
    }
    function frame() {
      rafId = 0;
      if (!ptr) return;
      var t = tAtClientX(o.svgOf ? o.svgOf(ptr.key) : root.querySelector('.mc-los-svg'), ptr.x);
      if (t != null) o.move(ptr.key, t);
    }
    function onPointer(e) {
      var hit = hitOf(e.target);
      if (!hit) return;
      var key = o.keyOf ? o.keyOf(hit) : 0;
      if (key == null) return;
      ptr = { key: key, x: e.clientX };
      if (!rafId) rafId = raf(frame);
    }
    function onOut(e) {
      if (e.pointerType === 'touch') return;
      if (hitOf(e.relatedTarget) || !hitOf(e.target)) return;
      cancel();
      o.leave();
    }
    root.addEventListener('pointermove', onPointer);
    root.addEventListener('pointerdown', onPointer);
    root.addEventListener('pointerout', onOut);
    return { cancel: cancel };
  }

  // The chart cursor of scope at t, with html in its readout.
  function showChartCursor(scope, t, html) {
    var cur = scope.querySelector('.mc-los-cursor');
    if (cur) {
      var vx = String(Math.round(LOS.viewXAtT(t) * 100) / 100);
      cur.setAttribute('x1', vx);
      cur.setAttribute('x2', vx);
      cur.setAttribute('visibility', 'visible');
    }
    var ro = scope.querySelector('.mc-los-readout');
    if (ro) ro.innerHTML = html;
  }

  // Every chart cursor in scope hidden, every readout back to hint.
  function hideChartCursor(scope, hint) {
    Array.prototype.forEach.call(scope.querySelectorAll('.mc-los-cursor'), function (c) { c.setAttribute('visibility', 'hidden'); });
    Array.prototype.forEach.call(scope.querySelectorAll('.mc-los-readout'), function (r) { r.textContent = hint; });
  }

  // ─── Map pieces ─────────────────────────────────────────────────────────

  // Dashed highlight of a leg the panel shows; the caller adds it to a layer.
  function legLine(a, b) {
    return L.polyline([[a.lat, a.lon], [b.lat, b.lon]], {
      className: 'mc-los-leg-active', weight: 4, opacity: 1, dashArray: '10 8', interactive: false
    });
  }

  // The map end of the crosshair at ll: moves marker (or makes one) and puts
  // it on layer. Returns the marker.
  function placeCursorMarker(marker, layer, ll) {
    if (!marker) {
      marker = L.marker([ll.lat, ll.lon], {
        icon: L.divIcon({ className: 'mc-los-cursor-marker', html: '', iconSize: [16, 16], iconAnchor: [8, 8] }),
        interactive: false, keyboard: false, zIndexOffset: 1000
      });
    } else {
      marker.setLatLng([ll.lat, ll.lon]);
    }
    if (layer && !layer.hasLayer(marker)) layer.addLayer(marker);
    return marker;
  }

  // Warning marker at an obstruction, tone 'warn' or 'bad'; the caller adds
  // it to a layer. text is its tooltip (escaped here) and, each time it is
  // added, its aria-label. o: { tipClass, onClick(e) }.
  function obstructionMarker(ll, tone, text, o) {
    o = o || {};
    var mk = L.marker([ll.lat, ll.lon], {
      icon: L.divIcon({
        className: 'mc-los-obstruction mc-los-tone-' + tone,
        html: icon('ph-warning'),
        iconSize: [28, 28],
        iconAnchor: [14, 14]
      }),
      keyboard: true, riseOnHover: true, zIndexOffset: 400
    });
    var tip = { direction: 'top', offset: [0, -16] };
    if (o.tipClass) tip.className = o.tipClass;
    mk.bindTooltip(esc(text), tip);
    if (o.onClick) mk.on('click', o.onClick);
    mk.on('add', function () {
      var el = mk.getElement && mk.getElement();
      if (el) el.setAttribute('aria-label', text + '. Show terrain profile');
    });
    return mk;
  }

  window.MeshLOSPanel = {
    esc: esc,
    fmtKm: fmtKm,
    fmtM: fmtM,
    fmtObstruction: fmtObstruction,
    createLRU: createLRU,
    raf: raf,
    caf: caf,
    isMobile: isMobile,
    reducedMotion: reducedMotion,
    icon: icon,
    profileHtml: profileHtml,
    chartErrorHtml: chartErrorHtml,
    readoutHtml: readoutHtml,
    buildPanel: buildPanel,
    setNav: setNav,
    mountPanel: mountPanel,
    unmountPanel: unmountPanel,
    fitOptions: fitOptions,
    snap: snap,
    bindCrosshair: bindCrosshair,
    showChartCursor: showChartCursor,
    hideChartCursor: hideChartCursor,
    legLine: legLine,
    placeCursorMarker: placeCursorMarker,
    obstructionMarker: obstructionMarker
  };
})();
