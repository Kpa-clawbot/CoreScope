#!/usr/bin/env node
/* report.js — HTML report for map-perf results (#2428).
 *
 *   node scripts/map-perf/report.js --baseline <dir> [--candidate <dir>] --out <file.html>
 *
 * <dir> is a run.js output directory (<out>/<label>) holding results.json and
 * shots/. Produces one self-contained HTML file: inline CSS, no external
 * requests, light + dark via prefers-color-scheme, readable at phone width.
 * Screenshots are referenced relative to the HTML file, so keep the result
 * directories next to it (the default layout under test-results/map-perf/).
 */
'use strict';
const fs = require('fs');
const path = require('path');
const { RULES, REGRESSION_PCT, evaluate } = require('./budgets');

function parseArgs(argv) {
  const a = {};
  for (let i = 2; i < argv.length; i++) {
    const k = argv[i], v = argv[i + 1];
    if (k === '--baseline') { a.baseline = v; i++; }
    else if (k === '--candidate') { a.candidate = v; i++; }
    else if (k === '--out') { a.out = v; i++; }
    else if (k === '-h' || k === '--help') a.help = true;
    else throw new Error('unknown argument: ' + k);
  }
  return a;
}
const USAGE = 'usage: node scripts/map-perf/report.js --baseline <dir> [--candidate <dir>] --out <file.html>';

function load(dir) {
  const f = path.join(dir, 'results.json');
  if (!fs.existsSync(f)) throw new Error('missing ' + f);
  return { dir: path.resolve(dir), data: JSON.parse(fs.readFileSync(f, 'utf8')) };
}

// Metric display table. better: 'lower' | 'higher' | null (informational).
const METRICS = [
  ['allTilesMs', 'Time to all visible tiles', 'ms', 'lower'],
  ['firstTileMs', 'Time to first tile', 'ms', 'lower'],
  ['firstMarkerMs', 'Time to first marker', 'ms', 'lower'],
  ['mapCreatedMs', 'First L.Map created', 'ms', 'lower'],
  ['allTilesSinceMapMs', 'All tiles, from map creation', 'ms', 'lower'],
  ['firstMarkerSinceMapMs', 'First marker, from map creation', 'ms', 'lower'],
  ['domContentLoadedMs', 'DOMContentLoaded', 'ms', 'lower'],
  ['loadEventMs', 'Load event', 'ms', 'lower'],
  ['loadWallMs', 'Navigate → map on screen (wall)', 'ms', 'lower'],
  ['longTaskTotalMs', 'Long tasks: total', 'ms', 'lower'],
  ['longTaskCount', 'Long tasks: count', '', 'lower'],
  ['longTaskMaxMs', 'Long tasks: max', 'ms', 'lower'],
  ['heapUsedMB', 'JS heap after GC', 'MB', 'lower'],
  ['hillshadeTileMsMedian', 'Hillshade per-tile', 'ms', 'lower'],
  ['panzoomMs', 'Pan/zoom path duration', 'ms', 'lower'],
  ['frameP50Ms', 'Frame interval p50', 'ms', 'lower'],
  ['frameP95Ms', 'Frame interval p95', 'ms', 'lower'],
  ['frameMaxMs', 'Frame interval max', 'ms', 'lower'],
  ['framesDropped', 'Frames > 50 ms', '', 'lower'],
  ['panzoomLongTaskTotalMs', 'Pan/zoom long tasks: total', 'ms', 'lower'],
  ['switchMsMedian', 'Provider switch (median)', 'ms', 'lower'],
  ['switchMsP95', 'Provider switch (p95)', 'ms', 'lower'],
  ['heapBeforeMB', 'Heap before switches', 'MB', 'lower'],
  ['heapAfterMB', 'Heap after switches', 'MB', 'lower'],
  ['heapGrowthPct', 'Heap growth', '%', 'lower'],
  ['layersBefore', 'Leaflet layers before', '', null],
  ['layersAfter', 'Leaflet layers after', '', 'lower'],
  ['tileElsAfter', 'Tile elements after', '', 'lower'],
  ['vcrFrameP50Ms', 'VCR frame interval p50', 'ms', 'lower'],
  ['vcrFrameP95Ms', 'VCR frame interval p95', 'ms', 'lower'],
  ['vcrFrameMaxMs', 'VCR frame interval max', 'ms', 'lower'],
  ['vcrFramesDropped', 'VCR frames > 50 ms', '', 'lower'],
  ['vcrLongTaskTotalMs', 'VCR long tasks: total', 'ms', 'lower'],
  ['tilesVisible', 'Visible tiles', '', null],
  ['tilesFailed', 'Failed tiles', '', 'lower'],
  ['requestsIntercepted', 'Stubbed requests', '', null],
  ['requestsExternal', 'Leaked external requests', '', 'lower'],
  ['maplibreRequests', 'MapLibre requests', '', 'lower'],
];
const METRIC_INFO = new Map(METRICS.map(m => [m[0], { label: m[1], unit: m[2], better: m[3] }]));
const HEADLINE = ['allTilesMs', 'firstMarkerMs', 'longTaskTotalMs'];

const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
function fmt(v, unit) {
  if (v == null || !isFinite(v)) return '—';
  const a = Math.abs(v);
  const s = a >= 100 ? v.toFixed(0) : a >= 10 ? v.toFixed(1) : v.toFixed(2);
  return unit ? `${s} ${unit}` : s;
}
function pct(v) { return (v == null || !isFinite(v)) ? '—' : (v > 0 ? '+' : '') + v.toFixed(1) + '%'; }
// Classify a candidate-vs-baseline move. Medians are compared; a move past
// REGRESSION_PCT is only 'bad' (red) when the candidate median is also outside
// the baseline's run-to-run range (above its p95 / below its min), otherwise
// 'noise' (amber).
function classify(better, bs, cs) {
  if (!bs || !cs) return { d: null, cls: '' };
  const d = bs.median !== 0 ? (cs.median - bs.median) / Math.abs(bs.median) * 100 : (cs.median === 0 ? 0 : null);
  if (d == null || !better) return { d, cls: '' };
  const worse = better === 'lower' ? d > REGRESSION_PCT : d < -REGRESSION_PCT;
  const better_ = better === 'lower' ? d < -REGRESSION_PCT : d > REGRESSION_PCT;
  if (worse) return { d, cls: (better === 'lower' ? cs.median > bs.p95 : cs.median < bs.min) ? 'bad' : 'noise' };
  return { d, cls: better_ ? 'good' : '' };
}
function spread(s) { return (s && s.median > 0) ? s.p95 / s.median : null; }

function relImg(outFile, runDir, rel) {
  return path.relative(path.dirname(path.resolve(outFile)), path.join(runDir, rel)).split(path.sep).join('/');
}

function build(base, cand, outFile) {
  const B = base.data, C = cand ? cand.data : null;
  const names = Object.keys(B.scenarios);
  if (C) for (const n of Object.keys(C.scenarios)) if (names.indexOf(n) < 0) names.push(n);

  // Budgets
  const budgetRows = [];
  for (const rule of RULES) {
    for (const n of names) {
      const r = evaluate(rule, B.scenarios[n], C ? C.scenarios[n] : null);
      if (r) budgetRows.push({ rule, scenario: n, r });
    }
  }
  const fails = budgetRows.filter(x => x.r.status === 'fail');
  const passes = budgetRows.filter(x => x.r.status === 'pass');
  const nas = budgetRows.filter(x => x.r.status === 'n/a');
  const noisy = budgetRows.filter(x => x.r.status === 'noise');
  const overall = fails.length ? 'fail' : noisy.length ? 'noise' : (passes.length ? 'pass' : 'na');

  const ruleSummary = RULES.map(rule => {
    const rows = budgetRows.filter(x => x.rule === rule);
    const f = rows.filter(x => x.r.status === 'fail');
    const p = rows.filter(x => x.r.status === 'pass');
    const z = rows.filter(x => x.r.status === 'noise');
    const st = f.length ? 'fail' : z.length ? 'noise' : p.length ? 'pass' : 'na';
    const item = (x) => `<code>${esc(x.scenario)}</code> ${esc(fmt(x.r.value))}${x.r.deltaPct != null ? ' (' + esc(pct(x.r.deltaPct)) + ')' : ''}`;
    return `<tr><td><span class="pill ${st}">${st === 'na' ? 'n/a' : st === 'noise' ? 'within noise' : st}</span></td><td>${esc(rule.description)}</td><td class="num">${p.length}</td><td class="num">${z.length}</td><td class="num">${f.length}</td><td class="num">${rows.length - p.length - f.length - z.length}</td>` +
      `<td>${f.map(item).concat(z.map(x => item(x) + ' <span class="noise">within noise</span>')).join('<br>')}</td></tr>`;
  }).join('');

  // Headline table: map/live/planner cold, light+dark
  const headRows = [];
  for (const surface of ['map', 'live', 'planner']) {
    for (const theme of ['light', 'dark']) {
      const n = `${surface}-cold-${theme}`;
      const b = B.scenarios[n], c = C && C.scenarios[n];
      const cells = HEADLINE.map(k => {
        const bs = b && b.summary && b.summary[k], cs = c && c.summary && c.summary[k];
        const u = METRIC_INFO.get(k).unit;
        let txt = bs ? fmt(bs.median, u) : (b && b.status !== 'ok' ? esc(b.status) : '—');
        if (C) {
          const k2 = classify(METRIC_INFO.get(k).better, bs, cs);
          txt += ` → ${cs ? fmt(cs.median, u) : '—'} <span class="${k2.cls || 'muted'}">${pct(k2.d)}</span>`;
        } else if (bs) {
          txt += ` <span class="muted">p95/med ${fmt(spread(bs))}</span>`;
        }
        return `<td class="num">${txt}</td>`;
      }).join('');
      headRows.push(`<tr><th scope="row">${esc(surface)} · ${theme}</th>${cells}</tr>`);
    }
  }

  // Per-scenario sections
  const sections = names.map(n => {
    const b = B.scenarios[n], c = C && C.scenarios[n];
    const sc = (c || b).scenario;
    const status = [b && b.status, c && c.status].filter(Boolean);
    const reasons = [b && b.reason && `baseline: ${b.reason}`, c && c.reason && `candidate: ${c.reason}`].filter(Boolean);
    const errs = [].concat(b ? (b.errors || []).map(e => 'baseline run ' + e.run + ': ' + e.error) : [], c ? (c.errors || []).map(e => 'candidate run ' + e.run + ': ' + e.error) : []);
    const keys = new Set([].concat(Object.keys((b && b.summary) || {}), Object.keys((c && c.summary) || {})));
    const ordered = METRICS.map(m => m[0]).filter(k => keys.has(k)).concat([...keys].filter(k => !METRIC_INFO.has(k)).sort());
    const rows = ordered.map(k => {
      const info = METRIC_INFO.get(k) || { label: k, unit: '', better: null };
      const bs = b && b.summary && b.summary[k], cs = c && c.summary && c.summary[k];
      let delta = '', cls = '';
      if (C && bs && cs) {
        const k2 = classify(info.better, bs, cs);
        delta = pct(k2.d) + (k2.cls === 'noise' ? ' <span class="noise">noise</span>' : '');
        cls = k2.cls;
      }
      const side = (s) => s ? `<td class="num">${fmt(s.median, info.unit)}</td><td class="num">${fmt(s.p95, info.unit)}</td><td class="num muted">${fmt(spread(s))}</td>` : '<td class="num">—</td><td class="num">—</td><td class="num">—</td>';
      return `<tr class="${cls}"><th scope="row">${esc(info.label)}<span class="key">${esc(k)}</span></th>${side(bs)}${C ? side(cs) + `<td class="num delta">${delta}</td>` : ''}</tr>`;
    }).join('');
    const n0 = (b && b.runs && b.runs.length) || 0, n1 = (c && c.runs && c.runs.length) || 0;
    const head = `<tr><th scope="col">Metric</th><th scope="col" class="num">${esc(B.meta.label)} median</th><th scope="col" class="num">p95</th><th scope="col" class="num" title="p95 ÷ median: run-to-run spread">p95/med</th>` +
      (C ? `<th scope="col" class="num">${esc(C.meta.label)} median</th><th scope="col" class="num">p95</th><th scope="col" class="num">p95/med</th><th scope="col" class="num">Δ median</th>` : '') + '</tr>';
    const prov = (c && c.resolvedProvider) || (b && b.resolvedProvider) || sc.provider;
    return `<section class="scenario" id="sc-${esc(n)}"><h3>${esc(n)} <span class="pill ${status.indexOf('ok') >= 0 && status.every(s => s === 'ok') ? 'pass' : status.indexOf('error') >= 0 ? 'fail' : 'na'}">${esc(status.join(' / '))}</span></h3>` +
      `<p class="muted">${esc(sc.surface)} · ${esc(sc.theme)} · provider ${esc(prov)} · ${esc(sc.format || 'raster')} · runs ${n0}${C ? ' / ' + n1 : ''}${(b && b.route) ? ' · <code>' + esc(b.route) + '</code>' : ''}</p>` +
      (reasons.length ? `<p class="note">Skipped: ${reasons.map(esc).join('; ')}</p>` : '') +
      (errs.length ? `<details class="errs"><summary>${errs.length} run error(s)</summary><ul>${errs.map(e => `<li><code>${esc(e)}</code></li>`).join('')}</ul></details>` : '') +
      (rows ? `<div class="scroll"><table>${head}${rows}</table></div>` : '') + '</section>';
  }).join('');

  // Gallery grouped by surface × theme × provider
  const groups = new Map();
  for (const n of names) {
    const b = B.scenarios[n], c = C && C.scenarios[n];
    const sc = (c || b).scenario;
    const prov = (c && c.resolvedProvider) || (b && b.resolvedProvider) || sc.provider;
    const g = `${sc.surface} · ${sc.theme} · ${prov}`;
    const imgs = [];
    if (b && b.screenshot) imgs.push({ who: B.meta.label, src: relImg(outFile, base.dir, b.screenshot) });
    if (c && c.screenshot) imgs.push({ who: C.meta.label, src: relImg(outFile, cand.dir, c.screenshot) });
    if (!imgs.length) continue;
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push({ name: n, imgs });
  }
  const gallery = [...groups.keys()].sort().map(g => `<div class="group"><h3>${esc(g)}</h3><div class="shots">` +
    groups.get(g).map(item => item.imgs.map(im => `<figure><a href="${esc(im.src)}"><img loading="lazy" src="${esc(im.src)}" alt="${esc(item.name + ' — ' + im.who)}"></a><figcaption>${esc(item.name)} <span class="muted">${esc(im.who)}</span></figcaption></figure>`).join('')).join('') +
    '</div></div>').join('');

  const metaRow = (d) => {
    const m = d.meta, s = m.server || {};
    return `<tr><th scope="row">${esc(m.label)}</th><td>${esc(m.baseUrl)}</td><td>${esc(m.mode)}</td><td class="num">${esc(m.runs)} (+${esc(m.warmup)} warm-up)</td>` +
      `<td>${esc(s.totalNodes != null ? s.totalNodes + ' nodes · ' + s.totalTransmissions + ' tx · ' + s.totalObservations + ' obs' : '—')}</td><td>${esc((m.startedAt || '').replace('T', ' ').slice(0, 19))}</td><td>${esc(m.browser || '')}</td></tr>`;
  };
  const skipped = [];
  for (const [lbl, d] of [[B.meta.label, B]].concat(C ? [[C.meta.label, C]] : [])) {
    for (const n of Object.keys(d.scenarios)) if (d.scenarios[n].status !== 'ok') skipped.push(`<li><code>${esc(n)}</code> (${esc(lbl)}): ${esc(d.scenarios[n].status)}${d.scenarios[n].reason ? ' — ' + esc(d.scenarios[n].reason) : ''}</li>`);
  }

  const title = C ? `Map perf: ${B.meta.label} vs ${C.meta.label}` : `Map perf: ${B.meta.label}`;
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(title)}</title>
<style>
:root{--bg:#f7f8fa;--fg:#1b1f24;--muted:#5d6470;--card:#fff;--line:#d9dde3;--pass:#1a7f37;--pass-bg:#dcf5e3;--fail:#b42318;--fail-bg:#fde4e1;--na:#5d6470;--na-bg:#eceff3;--accent:#0b62d6;--bad-row:#fff1f0;--warn:#9a6700;--warn-bg:#fff4d6}
@media (prefers-color-scheme: dark){:root{--bg:#0f1216;--fg:#e6e9ee;--muted:#9aa3ae;--card:#171b21;--line:#2b313a;--pass:#4ac26b;--pass-bg:#11301c;--fail:#ff7b72;--fail-bg:#3b1715;--na:#9aa3ae;--na-bg:#232830;--accent:#6cb6ff;--bad-row:#2a1514;--warn:#e3b341;--warn-bg:#3a2c0b}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{max-width:1200px;margin:0 auto;padding:16px}
h1{font-size:22px;margin:8px 0 4px}h2{font-size:17px;margin:28px 0 8px}h3{font-size:15px;margin:0 0 4px}
.muted{color:var(--muted)}.key{display:block;font:11px ui-monospace,monospace;color:var(--muted);font-weight:400}
code{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;overflow-wrap:anywhere}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px;margin:8px 0}
.scroll{overflow-x:auto;-webkit-overflow-scrolling:touch}
table{border-collapse:collapse;width:100%;background:var(--card)}
th,td{padding:6px 8px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
th[scope=col]{font-weight:600;color:var(--muted);font-size:12px;white-space:nowrap}
th[scope=row]{font-weight:500}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
tr.bad{background:var(--bad-row)}tr.bad td.delta,.bad{color:var(--fail);font-weight:600}.good{color:var(--pass)}tr.noise td.delta,.noise{color:var(--warn);font-weight:600}
.pill{display:inline-block;padding:1px 8px;border-radius:999px;font-size:12px;font-weight:600;text-transform:uppercase;letter-spacing:.02em}
.pill.pass{color:var(--pass);background:var(--pass-bg)}.pill.fail{color:var(--fail);background:var(--fail-bg)}.pill.na{color:var(--na);background:var(--na-bg)}.pill.noise{color:var(--warn);background:var(--warn-bg)}
.verdict{font-size:16px;font-weight:600}
.scenario{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px;margin:12px 0}
.scenario table{background:transparent}
.note{color:var(--muted);font-style:italic}
.errs summary{cursor:pointer;color:var(--fail)}
.group{margin:16px 0}.shots{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:12px}
figure{margin:0;background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden}
figure img{display:block;width:100%;height:auto;aspect-ratio:16/10;object-fit:cover;background:var(--na-bg)}
figcaption{padding:6px 8px;font-size:12px;word-break:break-all}
a{color:var(--accent)}
nav.toc{font-size:13px}nav.toc a{margin-right:12px;white-space:nowrap}
@media (max-width:600px){main{padding:12px 16px}th,td{padding:5px 6px}h1{font-size:19px}}
</style></head><body><main>
<h1>${esc(title)}</h1>
<p class="muted">Generated ${esc(new Date().toISOString().replace('T', ' ').slice(0, 19))} UTC. Medians across runs; p95/med is run-to-run spread (1.00 = no noise). Δ is candidate vs baseline median. Red: worse by more than ${REGRESSION_PCT}% <em>and</em> outside the baseline's run-to-run range (candidate median above baseline p95). Amber: worse by more than ${REGRESSION_PCT}% but within that range, so indistinguishable from noise.</p>
<nav class="toc"><a href="#summary">Summary</a><a href="#headline">Headline</a><a href="#scenarios">Scenarios</a><a href="#gallery">Screenshots</a></nav>
<div class="scroll"><table><tr><th scope="col">Run</th><th scope="col">Server</th><th scope="col">Mode</th><th scope="col" class="num">Runs</th><th scope="col">Dataset</th><th scope="col">Started</th><th scope="col">Browser</th></tr>${metaRow(B)}${C ? metaRow(C) : ''}</table></div>

<h2 id="summary">Budget summary</h2>
<div class="card"><span class="verdict"><span class="pill ${overall}">${overall === 'na' ? 'n/a' : overall === 'noise' ? 'within noise' : overall}</span> ${passes.length} pass · ${noisy.length} within noise · ${fails.length} fail · ${nas.length} n/a</span>${C ? '' : '<p class="muted">Baseline only: relative budgets need a candidate and show n/a; absolute budgets are checked against the baseline.</p>'}</div>
<div class="scroll"><table><tr><th scope="col">Status</th><th scope="col">Budget</th><th scope="col" class="num">Pass</th><th scope="col" class="num">Noise</th><th scope="col" class="num">Fail</th><th scope="col" class="num">n/a</th><th scope="col">Flagged</th></tr>${ruleSummary}</table></div>
${skipped.length ? `<h3 style="margin-top:16px">Skipped or failed scenarios</h3><ul>${skipped.join('')}</ul>` : ''}

<h2 id="headline">Headline: cold load</h2>
<div class="scroll"><table><tr><th scope="col">Surface</th><th scope="col" class="num">Time to all tiles</th><th scope="col" class="num">Time to markers</th><th scope="col" class="num">Long-task total</th></tr>${headRows.join('')}</table></div>

<h2 id="scenarios">Scenarios</h2>
${sections}

<h2 id="gallery">Screenshots</h2>
${gallery || '<p class="muted">No screenshots.</p>'}
</main></body></html>
`;
}

function main() {
  const a = parseArgs(process.argv);
  if (a.help) { console.log(USAGE); return; }
  if (!a.baseline || !a.out) throw new Error(USAGE);
  const base = load(a.baseline);
  const cand = a.candidate ? load(a.candidate) : null;
  const html = build(base, cand, a.out);
  fs.mkdirSync(path.dirname(path.resolve(a.out)), { recursive: true });
  fs.writeFileSync(a.out, html);
  console.log('[map-perf] wrote ' + path.resolve(a.out));
}

if (require.main === module) {
  try { main(); } catch (e) { console.error(e && e.message || e); process.exit(1); }
}

module.exports = { build };
