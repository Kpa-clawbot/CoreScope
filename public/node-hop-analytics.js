/* === CoreScope: node-hop-analytics.js === */
'use strict';
// Hop count at this node (issue #1812): for every flood packet the node
// forwarded, the number of hops the packet already had when the node's
// flood.max check ran. Data: GET /api/nodes/{pubkey}/hop_analytics?days=N.
(function () {
  // One filter per repeater CLI limit (firmware src/helpers/RoutingPolicy.h):
  // flood.max applies to every flood, flood.max.unscoped to un-scoped floods,
  // flood.max.advert to adverts.
  const FILTERS = [
    { id: 'flood', label: 'flood.max', tag: null, noun: 'flood packets' },
    { id: 'flood_unscoped', label: 'flood.max.unscoped', tag: 'unscoped', noun: 'un-scoped flood packets' },
    { id: 'flood_adverts', label: 'flood.max.advert', tag: 'advert', noun: 'flood adverts' },
  ];

  let chart = null;

  function filterById(id) {
    return FILTERS.find(f => f.id === id) || FILTERS[0];
  }

  function filterHops(packets, filterId) {
    const tag = filterById(filterId).tag;
    const out = [];
    for (const p of packets || []) {
      if (!tag || (p.tags && p.tags.indexOf(tag) >= 0)) out.push(Number(p.hops));
    }
    return out;
  }

  function hopHistogram(values) {
    let max = -1;
    for (const v of values) if (v > max) max = v;
    const counts = new Array(max + 1).fill(0);
    for (const v of values) counts[v]++;
    return counts;
  }

  // Quartiles by linear interpolation between closest ranks, Tukey whiskers at
  // 1.5 IQR. Hop counts are small integers, so sorting a copy stays cheap.
  function hopBoxStats(values) {
    if (!values.length) return null;
    const s = values.slice().sort((a, b) => a - b);
    const q = p => {
      const h = (s.length - 1) * p;
      const lo = Math.floor(h);
      return lo + 1 < s.length ? s[lo] + (h - lo) * (s[lo + 1] - s[lo]) : s[lo];
    };
    const q1 = q(0.25), median = q(0.5), q3 = q(0.75);
    const lowFence = q1 - 1.5 * (q3 - q1), highFence = q3 + 1.5 * (q3 - q1);
    let whiskerLow = q1, whiskerHigh = q3, outliers = 0;
    for (const v of s) {
      if (v < lowFence || v > highFence) { outliers++; continue; }
      if (v < whiskerLow) whiskerLow = v;
      if (v > whiskerHigh) whiskerHigh = v;
    }
    return { n: s.length, min: s[0], q1, median, q3, max: s[s.length - 1], whiskerLow, whiskerHigh, outliers };
  }

  function plural(n, word) {
    return n + ' ' + word + (n === 1 ? '' : 's');
  }

  function renderHopSection(data, filterId) {
    const filter = filterById(filterId);
    const values = filterHops(data.packets, filter.id);
    const stats = hopBoxStats(values);
    const ambiguous = Number(data.ambiguous) || 0;
    const chips = FILTERS.map(f =>
      `<button type="button" data-hop-filter="${f.id}" aria-pressed="${f.id === filter.id}"${f.id === filter.id ? ' class="active"' : ''}>${f.label}</button>`
    ).join('');
    const summary = stats
      ? `${plural(stats.n, 'packet')} (${filter.noun}) · median ${stats.median} · middle half ${stats.q1} to ${stats.q3} · max ${stats.max} hops`
      : '';
    const body = stats
      ? `<canvas id="hopCountChart" role="img" aria-label="Histogram and box plot of hop counts at this node for ${filter.noun}"></canvas>`
      : `<div style="padding:20px;text-align:center;color:var(--text-muted);font-size:12px">No forwarded ${filter.noun} attributed to this node in this window.</div>`;
    const note = ambiguous
      ? `<div class="analytics-chart-desc">${plural(ambiguous, 'packet')} left out: the path prefix of this node is shared with another node there, so its hop position is unknown.</div>`
      : '';
    return `
      <div class="analytics-time-range" role="group" aria-label="Hop limit to inspect">${chips}</div>
      <div style="font-size:12px;margin-bottom:6px">${summary}</div>
      ${body}
      ${note}`;
  }

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  // Box plot drawn in the chart's top padding, on the histogram's own x axis:
  // the category scale puts hop count h at index h, so fractional quartiles are
  // interpolated between neighbouring category centres.
  function boxPlotPlugin(stats) {
    return {
      id: 'hopBoxPlot',
      afterDatasetsDraw(c) {
        const x = c.scales.x;
        const px = v => {
          const lo = Math.floor(v);
          const a = x.getPixelForValue(lo);
          return v === lo ? a : a + (v - lo) * (x.getPixelForValue(lo + 1) - a);
        };
        const ctx = c.ctx;
        const mid = c.chartArea.top - 16;
        ctx.save();
        ctx.strokeStyle = cssVar('--text');
        ctx.fillStyle = cssVar('--accent-bg');
        ctx.lineWidth = 1.5;
        ctx.beginPath();
        ctx.moveTo(px(stats.whiskerLow), mid); ctx.lineTo(px(stats.q1), mid);
        ctx.moveTo(px(stats.q3), mid); ctx.lineTo(px(stats.whiskerHigh), mid);
        ctx.moveTo(px(stats.whiskerLow), mid - 5); ctx.lineTo(px(stats.whiskerLow), mid + 5);
        ctx.moveTo(px(stats.whiskerHigh), mid - 5); ctx.lineTo(px(stats.whiskerHigh), mid + 5);
        ctx.stroke();
        ctx.fillRect(px(stats.q1), mid - 8, px(stats.q3) - px(stats.q1), 16);
        ctx.strokeRect(px(stats.q1), mid - 8, px(stats.q3) - px(stats.q1), 16);
        ctx.beginPath();
        ctx.moveTo(px(stats.median), mid - 8); ctx.lineTo(px(stats.median), mid + 8);
        ctx.stroke();
        ctx.restore();
      }
    };
  }

  function drawChart(data, filterId) {
    if (chart) { chart.destroy(); chart = null; }
    const canvas = document.getElementById('hopCountChart');
    if (!canvas) return;
    const values = filterHops(data.packets, filterId);
    const counts = hopHistogram(values);
    chart = new Chart(canvas, {
      type: 'bar',
      data: {
        labels: counts.map((_, h) => String(h)),
        datasets: [{ label: 'Packets', data: counts, backgroundColor: cssVar('--accent'), borderWidth: 0 }]
      },
      options: {
        responsive: true,
        layout: { padding: { top: 32 } },
        plugins: { legend: { display: false } },
        scales: {
          x: { title: { display: true, text: 'Hops already in the path when this node forwarded' } },
          y: { beginAtZero: true, title: { display: true, text: 'Packets' } }
        }
      },
      plugins: [boxPlotPlugin(hopBoxStats(values))]
    });
  }

  function render(container, data, filterId) {
    container.innerHTML = renderHopSection(data, filterId);
    container.querySelectorAll('[data-hop-filter]').forEach(btn => {
      btn.addEventListener('click', () => render(container, data, btn.dataset.hopFilter));
    });
    drawChart(data, filterId);
  }

  async function load(container, pubkey, days) {
    destroy();
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/hop_analytics?days=' + days, { ttl: CLIENT_TTL.nodeAnalytics });
    } catch (e) {
      container.innerHTML = '<div style="padding:20px;text-align:center;color:var(--text-muted);font-size:12px">Hop counts unavailable: ' + escapeHtml(e.message) + '</div>';
      return;
    }
    if (!container.isConnected) return;
    render(container, data, 'flood');
  }

  function destroy() {
    if (chart) { chart.destroy(); chart = null; }
  }

  window.NodeHopAnalytics = { FILTERS, filterHops, hopHistogram, hopBoxStats, renderHopSection, load, destroy };
})();
