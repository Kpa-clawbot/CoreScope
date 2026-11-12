/* Radio line-of-sight (LOS) maths and terrain-profile SVG for one link.
 * Pure and dependency-free. Exposes window.MeshLOS.
 * The SVG carries no colours; styling comes from the mc-los-* CSS classes. */
(function () {
  'use strict';

  var R_KM = 6371;
  var PLOT = { W: 720, H: 260, ML: 48, MR: 12, MT: 10, MB: 26 };

  var VERDICTS = {
    clear:    { label: 'Clear',    detail: 'Full first Fresnel zone clear', tone: 'good' },
    clear60:  { label: 'Clear',    detail: '60% of the first Fresnel zone clear', tone: 'good' },
    marginal: { label: 'Marginal', detail: 'Fresnel zone partly obstructed by terrain', tone: 'warn' },
    blocked:  { label: 'Blocked',  detail: 'Terrain crosses the line of sight', tone: 'bad' }
  };

  function rad(d) { return d * Math.PI / 180; }
  function deg(r) { return r * 180 / Math.PI; }

  function haversineKm(a, b) {
    var dLat = rad(b.lat - a.lat), dLon = rad(b.lon - a.lon);
    var h = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
      Math.cos(rad(a.lat)) * Math.cos(rad(b.lat)) * Math.sin(dLon / 2) * Math.sin(dLon / 2);
    return 2 * R_KM * Math.asin(Math.min(1, Math.sqrt(h)));
  }

  function bearingDeg(a, b) {
    var p1 = rad(a.lat), p2 = rad(b.lat), dl = rad(b.lon - a.lon);
    var y = Math.sin(dl) * Math.cos(p2);
    var x = Math.cos(p1) * Math.sin(p2) - Math.sin(p1) * Math.cos(p2) * Math.cos(dl);
    return (deg(Math.atan2(y, x)) + 360) % 360;
  }

  function intermediatePoint(a, b, t) {
    var p1 = rad(a.lat), l1 = rad(a.lon), p2 = rad(b.lat), l2 = rad(b.lon);
    var h = Math.sin((p2 - p1) / 2) * Math.sin((p2 - p1) / 2) +
      Math.cos(p1) * Math.cos(p2) * Math.sin((l2 - l1) / 2) * Math.sin((l2 - l1) / 2);
    var d = 2 * Math.asin(Math.min(1, Math.sqrt(h)));
    if (d < 1e-12) return { lat: a.lat, lon: a.lon };
    var A = Math.sin((1 - t) * d) / Math.sin(d), B = Math.sin(t * d) / Math.sin(d);
    var x = A * Math.cos(p1) * Math.cos(l1) + B * Math.cos(p2) * Math.cos(l2);
    var y = A * Math.cos(p1) * Math.sin(l1) + B * Math.cos(p2) * Math.sin(l2);
    var z = A * Math.sin(p1) + B * Math.sin(p2);
    return { lat: deg(Math.atan2(z, Math.sqrt(x * x + y * y))), lon: deg(Math.atan2(y, x)) };
  }

  function earthBulgeM(dKm, totalKm, k) {
    return dKm * (totalKm - dKm) / (2 * k * R_KM) * 1000;
  }

  function fresnelRadiusM(dKm, totalKm, freqMHz) {
    return 17.32 * Math.sqrt(Math.max(dKm * (totalKm - dKm), 1e-9) / (totalKm * freqMHz / 1000));
  }

  function fsplDb(distKm, freqMHz) {
    return 32.45 + 20 * Math.log10(distKm) + 20 * Math.log10(freqMHz);
  }

  function verdictFor(c) {
    if (c >= 1) return 'clear';
    if (c >= 0.6) return 'clear60';
    if (c >= 0) return 'marginal';
    return 'blocked';
  }

  function analyze(inp) {
    inp = inp || {};
    var elev = inp.elevM;
    if (!Array.isArray(elev) || elev.length < 2) return null;
    var e = [], i;
    for (i = 0; i < elev.length; i++) {
      var v = Number(elev[i]);
      if (elev[i] === null || elev[i] === '' || !isFinite(v)) return null;
      e.push(v);
    }
    var freq = Number(inp.freqMHz);
    if (!(freq > 0) || !isFinite(freq)) return null;
    var k = inp.kFactor == null ? 4 / 3 : Number(inp.kFactor);
    if (!(k > 0) || !isFinite(k)) return null;
    var D = Number(inp.distanceKm);
    if (!isFinite(D) || D < 0.001) D = 0.001;
    var aA = Number(inp.antennaAM); if (!isFinite(aA)) aA = 0;
    var aB = Number(inp.antennaBM); if (!isFinite(aB)) aB = 0;

    var n = e.length;
    var terrainM = [], losM = [], fresnelM = [];
    for (i = 0; i < n; i++) terrainM.push(e[i] + earthBulgeM((i / (n - 1)) * D, D, k));
    var yA = terrainM[0] + aA, yB = terrainM[n - 1] + aB;
    var clearance = Infinity, worstIdx = -1, obstructionM = -Infinity;
    for (i = 0; i < n; i++) {
      var t = i / (n - 1);
      losM.push(yA + (yB - yA) * t);
      fresnelM.push(fresnelRadiusM(t * D, D, freq));
      if (i >= 1 && i <= n - 2) {
        var c = (losM[i] - terrainM[i]) / fresnelM[i];
        if (c < clearance) { clearance = c; worstIdx = i; }
        var ob = terrainM[i] - losM[i];
        if (ob > obstructionM) obstructionM = ob;
      }
    }
    if (worstIdx < 0) obstructionM = -Infinity;
    var worstT = worstIdx >= 0 ? worstIdx / (n - 1) : null;
    return {
      n: n, distanceKm: D, freqMHz: freq, kFactor: k, antennaAM: aA, antennaBM: aB,
      elevM: e, terrainM: terrainM, losM: losM, fresnelM: fresnelM,
      clearance: clearance, worstIdx: worstIdx, worstT: worstT,
      worstKm: worstT == null ? null : worstT * D,
      obstructionM: obstructionM, verdict: verdictFor(clearance),
      fsplDb: fsplDb(D, freq)
    };
  }

  // HTML-escapes text and attribute values. The only copy: MeshLOSPanel.esc
  // (los-panel.js) re-exports it for route-los.js and planner.js.
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function niceStep(range, target) {
    var raw = range / target;
    if (!(raw > 0) || !isFinite(raw)) return 1;
    var p = Math.pow(10, Math.floor(Math.log10(raw)));
    var f = raw / p;
    return (f < 1.5 ? 1 : f < 3.5 ? 2 : f < 7.5 ? 5 : 10) * p;
  }

  function num(x) { return Math.round(x * 100) / 100; }

  function viewXAtT(t) { return PLOT.ML + t * (PLOT.W - PLOT.ML - PLOT.MR); }
  function tAtViewX(x) {
    var t = (x - PLOT.ML) / (PLOT.W - PLOT.ML - PLOT.MR);
    return t < 0 ? 0 : t > 1 ? 1 : t;
  }

  function renderProfileSvg(an, opts) {
    if (!an) return '';
    opts = opts || {};
    var P = PLOT, n = an.n, D = an.distanceKm, i;
    var x0 = P.ML, x1 = P.W - P.MR, yTop = P.MT, yBot = P.H - P.MB;
    var lo = Infinity, hi = -Infinity;
    for (i = 0; i < n; i++) {
      lo = Math.min(lo, an.terrainM[i], an.losM[i] - an.fresnelM[i]);
      hi = Math.max(hi, an.terrainM[i], an.losM[i] + an.fresnelM[i]);
    }
    lo = Math.min(lo, an.terrainM[0] + an.antennaAM, an.terrainM[n - 1] + an.antennaBM);
    hi = Math.max(hi, an.terrainM[0] + an.antennaAM, an.terrainM[n - 1] + an.antennaBM);
    var pad = (hi - lo) * 0.06 + 2;
    lo -= pad; hi += pad;
    function X(t) { return viewXAtT(t); }
    function Y(m) { return yBot - (m - lo) / (hi - lo) * (yBot - yTop); }
    function pt(t, m) { return num(X(t)) + ',' + num(Y(m)); }

    var out = [];
    var pct = an.clearance === Infinity ? 'no interior terrain samples' :
      'worst clearance ' + Math.round(an.clearance * 100) + '% of the first Fresnel zone';
    var label = 'Terrain profile, ' + num(D) + ' km link between ' + (opts.nameA || 'A') + ' and ' +
      (opts.nameB || 'B') + '. ' + VERDICTS[an.verdict].label + ': ' + pct + '.';
    out.push('<svg class="mc-los-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + P.W + ' ' + P.H +
      '" role="img" aria-label="' + esc(label) + '">');

    var grid = [], ticks = [];
    var ys = niceStep(hi - lo, 5);
    for (var v = Math.ceil(lo / ys) * ys; v <= hi; v += ys) {
      var yy = num(Y(v));
      grid.push('<line class="mc-los-grid" x1="' + x0 + '" x2="' + x1 + '" y1="' + yy + '" y2="' + yy + '"/>');
      ticks.push('<text class="mc-los-tick" x="' + (x0 - 6) + '" y="' + yy + '" text-anchor="end" dominant-baseline="middle">' +
        esc(Math.round(v * 100) / 100) + ' m</text>');
    }
    var xs = niceStep(D, 8);
    for (var d = 0; d <= D + 1e-9; d += xs) {
      var xx = num(X(d / D));
      ticks.push('<text class="mc-los-tick" x="' + xx + '" y="' + (yBot + 16) + '" text-anchor="middle">' +
        esc(Math.round(d * 100) / 100) + ' km</text>');
    }
    out.push(grid.join(''), ticks.join(''));

    var up = [], dn = [], up6 = [], dn6 = [], gr = [], ln = [];
    for (i = 0; i < n; i++) {
      var t = i / (n - 1), l = an.losM[i], f = an.fresnelM[i];
      up.push(pt(t, l + f)); dn.unshift(pt(t, l - f));
      up6.push(pt(t, l + 0.6 * f)); dn6.unshift(pt(t, l - 0.6 * f));
      gr.push(pt(t, an.terrainM[i]));
      ln.push(pt(t, l));
    }
    out.push('<polygon class="mc-los-f1" points="' + up.concat(dn).join(' ') + '"/>');
    out.push('<polygon class="mc-los-f60" points="' + up6.concat(dn6).join(' ') + '"/>');
    out.push('<polygon class="mc-los-ground" points="' + gr.join(' ') + ' ' + num(x1) + ',' + yBot + ' ' + num(x0) + ',' + yBot + '"/>');
    out.push('<polyline class="mc-los-line" fill="none" points="' + ln.join(' ') + '"/>');
    out.push('<circle class="mc-los-ant mc-los-ant-a" r="4" cx="' + num(X(0)) + '" cy="' + num(Y(an.terrainM[0] + an.antennaAM)) + '"/>');
    out.push('<circle class="mc-los-ant mc-los-ant-b" r="4" cx="' + num(X(1)) + '" cy="' + num(Y(an.terrainM[n - 1] + an.antennaBM)) + '"/>');
    if (an.worstIdx >= 0) {
      out.push('<circle class="mc-los-worst mc-los-tone-' + VERDICTS[an.verdict].tone + '" r="5" cx="' +
        num(X(an.worstT)) + '" cy="' + num(Y(an.terrainM[an.worstIdx])) + '"/>');
    }
    out.push('<text class="mc-los-endlabel" x="' + (x0 + 4) + '" y="' + (yTop + 10) + '" text-anchor="start">' + esc(opts.nameA || 'A') + '</text>');
    out.push('<text class="mc-los-endlabel" x="' + (x1 - 4) + '" y="' + (yTop + 10) + '" text-anchor="end">' + esc(opts.nameB || 'B') + '</text>');
    out.push('<line class="mc-los-cursor" x1="' + x0 + '" x2="' + x0 + '" y1="' + yTop + '" y2="' + yBot + '" visibility="hidden"/>');
    out.push('<rect class="mc-los-hit" x="' + x0 + '" y="' + yTop + '" width="' + (x1 - x0) + '" height="' + (yBot - yTop) + '" fill="transparent"/>');
    out.push('</svg>');
    return out.join('');
  }

  window.MeshLOS = {
    R_KM: R_KM,
    PLOT: PLOT,
    VERDICTS: VERDICTS,
    haversineKm: haversineKm,
    bearingDeg: bearingDeg,
    intermediatePoint: intermediatePoint,
    earthBulgeM: earthBulgeM,
    fresnelRadiusM: fresnelRadiusM,
    fsplDb: fsplDb,
    verdictFor: verdictFor,
    analyze: analyze,
    esc: esc,
    renderProfileSvg: renderProfileSvg,
    viewXAtT: viewXAtT,
    tAtViewX: tAtViewX
  };
})();
