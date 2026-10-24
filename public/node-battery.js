/* === CoreScope — node-battery.js ===
 * The Battery line on a node's page and side panel: the latest voltage from
 * /api/nodes/{pk}/battery, its status, and who reported it (the node itself or
 * the observer that polled it directly), with a link to the history chart.
 * Hidden when the node has no battery readings.
 */
'use strict';

(function () {
  const STATUS = {
    ok: { text: 'OK', color: 'var(--status-green-text, #51cf66)' },
    low: { text: 'Low', color: 'var(--status-yellow, #fcc419)' },
    critical: { text: 'Critical', color: 'var(--status-red, #ff6b6b)' },
    no_battery: { text: 'No battery fitted', color: 'var(--text-muted)' },
  };

  function route(s) {
    if (s.source !== 'neighbor' || s.reply_hops == null) return '';
    return s.reply_hops === 0 ? 'direct' : 'via ' + s.reply_hops + ' hop' + (s.reply_hops === 1 ? '' : 's');
  }

  function reporter(s) {
    if (s.source !== 'neighbor') return 'own report';
    const name = escapeHtml(s.observer_name || String(s.observer_id || '').slice(0, 8));
    return 'by <a href="#/observers/' + encodeURIComponent(String(s.observer_id || '').toUpperCase()) + '">' + name + '</a>';
  }

  // html returns the line for a battery response, or '' when there is none.
  function html(pubkey, data) {
    const sel = data && data.selected;
    if (!sel) return '';
    const st = STATUS[data.status] || { text: '', color: 'inherit' };
    const parts = [];
    if (data.status === 'no_battery') {
      parts.push('<span style="color:' + st.color + '">' + st.text + '</span>');
    } else {
      parts.push('<strong style="color:' + st.color + '">' + (Number(data.latest_mv) / 1000).toFixed(2) + ' V</strong> ' + st.text);
    }
    parts.push(reporter(sel));
    const r = route(sel);
    if (r) parts.push(r);
    if (sel.latest_ts) parts.push(escapeHtml(timeAgo(sel.latest_ts)));
    const others = (data.sources || []).length - 1;
    if (others > 0) parts.push('+' + others + ' other' + (others === 1 ? '' : 's'));
    parts.push('<a href="#/nodes/' + encodeURIComponent(pubkey) + '/analytics">history</a>');
    return parts.join(' · ');
  }

  // mount fills el (hidden until there is something to show). el holds a
  // .nb-val element for the value; a <div> inside a <dl> is shown with
  // display:contents so it doesn't break the dt/dd grid.
  async function mount(el, pubkey) {
    if (!el || !pubkey) return;
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/battery?days=7', { ttl: 60000 });
    } catch (_e) {
      return; // no line rather than an error on the node page
    }
    if (!el.isConnected) return; // the page moved on
    const line = html(pubkey, data);
    if (!line) return;
    const val = el.querySelector('.nb-val');
    if (!val) return;
    val.innerHTML = line;
    el.hidden = false;
    if (el.tagName === 'DIV') el.style.display = 'contents';
  }

  window.NodeBattery = { mount: mount, html: html };
})();
