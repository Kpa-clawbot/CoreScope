/* === CoreScope — app.js === */
'use strict';

(function () {
  const _k = 'ArrowUp ArrowUp ArrowDown ArrowDown ArrowLeft ArrowRight ArrowLeft ArrowRight b a'.split(' ');
  let _kp = 0;
  document.addEventListener('keydown', function (e) {
    _kp = e.key === _k[_kp] ? _kp + 1 : e.key === _k[0] ? 1 : 0;
    if (_kp === _k.length) { _kp = 0; _su(); }
  });

  let _tc = 0, _tt = null;
  document.addEventListener('click', function (e) {
    if (!e.target.closest('.nav-brand')) { _tc = 0; return; }
    clearTimeout(_tt);
    if (++_tc >= 7) { _tc = 0; _su(); return; }
    _tt = setTimeout(() => { _tc = 0; }, 2000);
  });
  function _su() {
    if (document.getElementById('_so')) return;
    const overlay = document.createElement('div');
    overlay.id = '_so';
    overlay.style.cssText = 'position:fixed;inset:0;z-index:99999;background:rgba(0,0,0,0.92);display:flex;flex-direction:column;align-items:center;justify-content:center;cursor:pointer;animation:_sf 0.4s ease';
    overlay.innerHTML = `
      <style>
        @keyframes _sf { from { opacity:0; transform:scale(0.92); } to { opacity:1; transform:scale(1); } }
        @keyframes _ss { 0% { transform:rotate(0deg) scale(1); } 50% { transform:rotate(180deg) scale(1.08); } 100% { transform:rotate(360deg) scale(1); } }
        #_sl { animation: _ss 8s linear infinite; filter:brightness(0) drop-shadow(0 0 2px #fff) drop-shadow(0 0 2px #fff) drop-shadow(0 0 2px #fff) drop-shadow(0 0 28px rgba(74,158,255,0.7)); }
        #_so h2 { color:#fff; font-size:1.6rem; font-weight:700; margin:28px 0 8px; letter-spacing:0.04em; }
        #_so p { color:#64748b; font-size:0.9rem; margin:0; }
        #_sc { color:#4a9eff; font-size:0.85rem; margin-top:10px; min-height:1.2em; transition:opacity 0.4s; }
        #_so small { color:#334155; font-size:0.75rem; margin-top:28px; }
        #_sv { position:absolute; inset:0; pointer-events:none; }
      </style>
      <canvas id="_sv"></canvas>
      <img id="_sl" src="salishmesh-logo.svg" width="220" height="220" alt="Salish Mesh">
      <h2>Salish Mesh</h2>
      <p>You found the secret. Welcome to the mesh.</p>
      <div id="_sc"></div>
      <small>click or press Esc to close</small>`;
    document.body.appendChild(overlay);

    fetch('/api/v0/metrics/hit', { method: 'POST' })
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (!data) return;
        const el = document.getElementById('_sc');
        if (el) el.textContent = 'Discovery #' + data.count + ' — you\'re one of us now.';
      })
      .catch(() => {});

    // Burst confetti from the center of the screen
    const canvas = document.getElementById('_sv');
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
    const ctx = canvas.getContext('2d');
    const COLORS = ['#4a9eff','#22c55e','#f59e0b','#a78bfa','#f472b6','#fb923c','#fff','#e11d48'];
    const particles = Array.from({ length: 160 }, () => {
      const angle = Math.random() * Math.PI * 2;
      const speed = 8 + Math.random() * 14;
      return {
        x: canvas.width / 2, y: canvas.height / 2,
        vx: Math.cos(angle) * speed, vy: Math.sin(angle) * speed - 4,
        r: 6 + Math.random() * 8,
        color: COLORS[Math.floor(Math.random() * COLORS.length)],
        alpha: 1, decay: 0.007 + Math.random() * 0.006,
        shape: Math.random() > 0.4 ? 'rect' : 'circle',
        rot: Math.random() * Math.PI * 2, spin: (Math.random() - 0.5) * 0.18,
      };
    });
    let rafId;
    function animateConfetti() {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      let alive = 0;
      for (const p of particles) {
        if (p.alpha <= 0) continue;
        alive++;
        p.x += p.vx; p.y += p.vy; p.vy += 0.18; // gravity
        p.vx *= 0.99; // slight air drag
        p.alpha -= p.decay; p.rot += p.spin;
        ctx.save();
        ctx.globalAlpha = Math.max(0, p.alpha);
        ctx.fillStyle = p.color;
        ctx.translate(p.x, p.y); ctx.rotate(p.rot);
        if (p.shape === 'circle') {
          ctx.beginPath(); ctx.arc(0, 0, p.r, 0, Math.PI * 2); ctx.fill();
        } else {
          ctx.fillRect(-p.r, -p.r * 0.5, p.r * 2, p.r);
        }
        ctx.restore();
      }
      if (alive > 0) rafId = requestAnimationFrame(animateConfetti);
    }
    animateConfetti();

    const close = () => { overlay.remove(); cancelAnimationFrame(rafId); };
    overlay.addEventListener('click', close);
    document.addEventListener('keydown', function esc(e) { if (e.key === 'Escape') { close(); document.removeEventListener('keydown', esc); } });
  }
})();

// --- Route/Payload name maps ---
const ROUTE_TYPES = { 0: 'TRANSPORT_FLOOD', 1: 'FLOOD', 2: 'DIRECT', 3: 'TRANSPORT_DIRECT' };
const PAYLOAD_TYPES = { 0: 'Request', 1: 'Response', 2: 'Dir Msg', 3: 'ACK', 4: 'Advert', 5: 'Ch Msg', 6: 'Group Data', 7: 'Anon Req', 8: 'Path', 9: 'Trace', 10: 'Multipart', 11: 'Control', 15: 'Raw Custom' };
const PAYLOAD_COLORS = { 0: 'req', 1: 'response', 2: 'txt-msg', 3: 'ack', 4: 'advert', 5: 'grp-txt', 7: 'anon-req', 8: 'path', 9: 'trace' };

// Central layout breakpoints — mirrors the CSS @media breakpoints exactly.
// Use Layout.isMobile() / isTablet() instead of window.innerWidth comparisons:
//   - matchMedia evaluates the same engine as CSS (correct on orientation change)
//   - .matches is never stale (iOS can return wrong innerWidth during transition)
//   - onMobileChange / onTabletChange fire only when a breakpoint crosses, not every pixel
const Layout = (function() {
  const _mq640  = window.matchMedia('(max-width: 640px)');
  const _mq1023 = window.matchMedia('(max-width: 1023px)');
  return {
    isMobile:        () => _mq640.matches,   // ≤640px
    isTabletOrBelow: () => _mq1023.matches,  // ≤1023px (includes mobile)
    isDesktop:       () => !_mq1023.matches, // ≥1024px
    onMobileChange:  fn => _mq640.addEventListener('change', fn),
    offMobileChange: fn => _mq640.removeEventListener('change', fn),
    onTabletChange:  fn => _mq1023.addEventListener('change', fn),
    offTabletChange: fn => _mq1023.removeEventListener('change', fn),
  };
})();

function routeTypeName(n) { return ROUTE_TYPES[n] || 'UNKNOWN'; }
function payloadTypeName(n) { return PAYLOAD_TYPES[n] || 'UNKNOWN'; }
function payloadTypeColor(n) { return PAYLOAD_COLORS[n] || 'unknown'; }
function isTransportRoute(rt) { return rt === 0 || rt === 3; }
function transportBadge(rt) { return isTransportRoute(rt) ? ' <span class="badge badge-transport" title="' + routeTypeName(rt) + '">T</span>' : ''; }

// --- Utilities ---
const _apiPerf = { calls: 0, totalMs: 0, log: [], cacheHits: 0 };
const _apiCache = new Map();
const _inflight = new Map();
// Client-side TTLs (ms) — loaded from server config, with defaults
const CLIENT_TTL = {
  stats: 10000, nodeDetail: 240000, nodeHealth: 240000, nodeList: 90000,
  bulkHealth: 300000, networkStatus: 300000, observers: 120000,
  channels: 15000, channelMessages: 10000, analyticsRF: 300000,
  analyticsTopology: 300000, analyticsChannels: 300000, analyticsHashSizes: 300000,
  analyticsSubpaths: 300000, analyticsSubpathDetail: 300000,
  nodeAnalytics: 60000, nodeSearch: 10000
};
// Fetch server cache config and use as client TTLs (server values are in seconds)
fetch('/api/config/cache').then(r => r.json()).then(cfg => {
  for (const [k, v] of Object.entries(cfg)) {
    if (k in CLIENT_TTL && typeof v === 'number') CLIENT_TTL[k] = v * 1000;
  }
}).catch(() => {});
async function api(path, { ttl = 0, bust = false } = {}) {
  const t0 = performance.now();
  if (!bust && ttl > 0) {
    const cached = _apiCache.get(path);
    if (cached && Date.now() < cached.expires) {
      _apiPerf.calls++;
      _apiPerf.cacheHits++;
      _apiPerf.log.push({ path, ms: 0, time: Date.now(), cached: true });
      if (_apiPerf.log.length > 200) _apiPerf.log.shift();
      return cached.data;
    }
  }
  // Deduplicate in-flight requests
  if (_inflight.has(path)) return _inflight.get(path);
  const promise = (async () => {
    const res = await fetch('/api' + path);
    if (!res.ok) throw new Error(`API ${res.status}: ${path}`);
    const data = await res.json();
    const ms = performance.now() - t0;
    _apiPerf.calls++;
    _apiPerf.totalMs += ms;
    _apiPerf.log.push({ path, ms: Math.round(ms), time: Date.now() });
    if (_apiPerf.log.length > 200) _apiPerf.log.shift();
    if (ms > 500) console.warn(`[SLOW API] ${path} took ${Math.round(ms)}ms`);
    if (ttl > 0) _apiCache.set(path, { data, expires: Date.now() + ttl });
    return data;
  })();
  _inflight.set(path, promise);
  promise.finally(() => _inflight.delete(path));
  return promise;
}

function invalidateApiCache(prefix) {
  for (const key of _apiCache.keys()) {
    if (key.startsWith(prefix || '')) _apiCache.delete(key);
  }
}
// Expose for console debugging: apiPerf()
window.apiPerf = function() {
  const byPath = {};
  _apiPerf.log.forEach(e => {
    if (!byPath[e.path]) byPath[e.path] = { count: 0, totalMs: 0, maxMs: 0 };
    byPath[e.path].count++;
    byPath[e.path].totalMs += e.ms;
    if (e.ms > byPath[e.path].maxMs) byPath[e.path].maxMs = e.ms;
  });
  const rows = Object.entries(byPath).map(([p, s]) => ({
    path: p, count: s.count, avgMs: Math.round(s.totalMs / s.count), maxMs: s.maxMs,
    totalMs: Math.round(s.totalMs)
  })).sort((a, b) => b.totalMs - a.totalMs);
  console.table(rows);
  const hitRate = _apiPerf.calls ? Math.round(_apiPerf.cacheHits / _apiPerf.calls * 100) : 0;
  const misses = _apiPerf.calls - _apiPerf.cacheHits;
  console.log(`Cache: ${_apiPerf.cacheHits} hits / ${misses} misses (${hitRate}% hit rate)`);
  return { calls: _apiPerf.calls, avgMs: Math.round(_apiPerf.totalMs / (misses || 1)), cacheHits: _apiPerf.cacheHits, cacheMisses: misses, cacheHitRate: hitRate, endpoints: rows };
};

function timeAgo(iso) {
  if (!iso) return '—';
  const ms = new Date(iso).getTime();
  if (!isFinite(ms)) return '—';
  const s = Math.floor((Date.now() - ms) / 1000);
  const abs = Math.abs(s);
  let value;
  let suffix;
  if (abs < 60) { value = abs; suffix = 's'; }
  else if (abs < 3600) { value = Math.floor(abs / 60); suffix = 'm'; }
  else if (abs < 86400) { value = Math.floor(abs / 3600); suffix = 'h'; }
  else { value = Math.floor(abs / 86400); suffix = 'd'; }
  if (s < 0) return 'in ' + value + suffix;
  return value + suffix + ' ago';
}

function getHashParams() {
  return new URLSearchParams(location.hash.split('?')[1] || '');
}

function getDistanceUnit() {
  var stored = localStorage.getItem('meshcore-distance-unit');
  if (stored === 'km') return 'km';
  if (stored === 'mi') return 'mi';
  // 'auto' or no value — locale detection
  var milesLocales = ['en-us', 'en-gb'];
  var lang = (typeof navigator !== 'undefined' && navigator.language || '').toLowerCase();
  for (var i = 0; i < milesLocales.length; i++) {
    if (lang === milesLocales[i] || lang.startsWith(milesLocales[i] + '-')) return 'mi';
  }
  return 'km';
}
window.getDistanceUnit = getDistanceUnit;

function formatDistance(km) {
  if (km == null || isNaN(+km)) return '—';
  var d = +km;
  var unit = getDistanceUnit();
  if (unit === 'mi') {
    var mi = d / 1.60934;
    if (mi < 0.1) return Math.round(mi * 5280) + ' ft';
    return mi.toFixed(1) + ' mi';
  }
  if (d < 1) return Math.round(d * 1000) + ' m';
  return d.toFixed(1) + ' km';
}
window.formatDistance = formatDistance;

function formatDistanceRound(km) {
  if (km == null || isNaN(+km)) return '—';
  var unit = getDistanceUnit();
  if (unit === 'mi') return Math.round(+km / 1.60934) + ' mi';
  return Math.round(+km) + ' km';
}
window.formatDistanceRound = formatDistanceRound;

function getTimestampMode() {
  const saved = localStorage.getItem('meshcore-timestamp-mode');
  if (saved === 'ago' || saved === 'absolute') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.defaultMode;
  return serverDefault === 'absolute' ? 'absolute' : 'ago';
}

function getTimestampTimezone() {
  const saved = localStorage.getItem('meshcore-timestamp-timezone');
  if (saved === 'utc' || saved === 'local') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.timezone;
  return serverDefault === 'utc' ? 'utc' : 'local';
}

function getTimestampFormatPreset() {
  const saved = localStorage.getItem('meshcore-timestamp-format');
  if (saved === 'iso' || saved === 'iso-seconds' || saved === 'locale') return saved;
  const serverDefault = window.SITE_CONFIG?.timestamps?.formatPreset;
  return (serverDefault === 'iso' || serverDefault === 'iso-seconds' || serverDefault === 'locale') ? serverDefault : 'iso';
}

function getTimestampCustomFormat() {
  if (window.SITE_CONFIG?.timestamps?.allowCustomFormat !== true) return '';
  const saved = localStorage.getItem('meshcore-timestamp-custom-format');
  if (saved != null) return String(saved);
  const serverDefault = window.SITE_CONFIG?.timestamps?.customFormat;
  return serverDefault == null ? '' : String(serverDefault);
}

function pad2(v) { return String(v).padStart(2, '0'); }
function pad3(v) { return String(v).padStart(3, '0'); }

function formatIsoLike(d, timezone, includeMs) {
  const useUtc = timezone === 'utc';
  const year = useUtc ? d.getUTCFullYear() : d.getFullYear();
  const month = useUtc ? d.getUTCMonth() + 1 : d.getMonth() + 1;
  const day = useUtc ? d.getUTCDate() : d.getDate();
  const hour = useUtc ? d.getUTCHours() : d.getHours();
  const minute = useUtc ? d.getUTCMinutes() : d.getMinutes();
  const second = useUtc ? d.getUTCSeconds() : d.getSeconds();
  const ms = useUtc ? d.getUTCMilliseconds() : d.getMilliseconds();
  let out = year + '-' + pad2(month) + '-' + pad2(day) + ' ' + pad2(hour) + ':' + pad2(minute) + ':' + pad2(second);
  if (includeMs) out += '.' + pad3(ms);
  return out;
}

function formatTimestampCustom(d, formatString, timezone) {
  if (!/YYYY|MM|DD|HH|mm|ss|SSS|Z/.test(String(formatString))) return '';
  const useUtc = timezone === 'utc';
  const replacements = {
    YYYY: String(useUtc ? d.getUTCFullYear() : d.getFullYear()),
    MM: pad2((useUtc ? d.getUTCMonth() : d.getMonth()) + 1),
    DD: pad2(useUtc ? d.getUTCDate() : d.getDate()),
    HH: pad2(useUtc ? d.getUTCHours() : d.getHours()),
    mm: pad2(useUtc ? d.getUTCMinutes() : d.getMinutes()),
    ss: pad2(useUtc ? d.getUTCSeconds() : d.getSeconds()),
    SSS: pad3(useUtc ? d.getUTCMilliseconds() : d.getMilliseconds()),
    Z: (timezone === 'utc' ? 'UTC' : 'local')
  };
  return String(formatString).replace(/YYYY|MM|DD|HH|mm|ss|SSS|Z/g, token => replacements[token] || token);
}

function formatAbsoluteTimestamp(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (!isFinite(d.getTime())) return '—';
  const timezone = getTimestampTimezone();
  const preset = getTimestampFormatPreset();
  const customFormat = getTimestampCustomFormat().trim();
  if (customFormat) {
    const customOut = formatTimestampCustom(d, customFormat, timezone);
    if (customOut && !/Invalid Date|NaN|undefined|null/.test(customOut)) return customOut;
  }
  if (preset === 'iso-seconds') return formatIsoLike(d, timezone, true);
  if (preset === 'locale') {
    if (timezone === 'utc') return d.toLocaleString([], { timeZone: 'UTC' });
    return d.toLocaleString();
  }
  return formatIsoLike(d, timezone, false);
}

function formatTimestamp(isoString, mode) {
  return formatTimestampWithTooltip(isoString, mode).text;
}

function formatTimestampWithTooltip(isoString, mode) {
  if (!isoString) return { text: '—', tooltip: '—', isFuture: false };
  const d = new Date(isoString);
  if (!isFinite(d.getTime())) return { text: '—', tooltip: '—', isFuture: false };
  const activeMode = mode === 'absolute' || mode === 'ago' ? mode : getTimestampMode();
  const isFuture = d.getTime() > Date.now();
  const absolute = formatAbsoluteTimestamp(isoString);
  const relative = timeAgo(isoString);
  const text = isFuture ? absolute : (activeMode === 'absolute' ? absolute : relative);
  const tooltip = isFuture ? relative : (activeMode === 'absolute' ? relative : absolute);
  return { text, tooltip, isFuture };
}

function truncate(str, len) {
  if (!str) return '';
  return str.length > len ? str.slice(0, len) + '…' : str;
}
function midTruncate(str, n) {
  if (!str) return '';
  if (str.length <= n * 2) return str;
  return str.slice(0, n) + '…' + str.slice(-n);
}

function formatPubKey(pubkey, hashSize, truncLen) {
  if (!pubkey) return '';
  const pk = pubkey.toUpperCase();
  const prefixChars = (hashSize || 0) * 2;
  let full = truncLen && pk.length > truncLen ? pk.slice(0, truncLen) + '…' : pk;
  if (!prefixChars || prefixChars > full.length) return escapeHtml(full);
  const prefix = full.slice(0, prefixChars);
  const rest   = full.slice(prefixChars);
  return `<span class="pubkey-hash-prefix" title="${prefixChars/2}-byte hash ID">${escapeHtml(prefix)}</span>${escapeHtml(rest)}`;
}

function formatEngineBadge(engine) {
  if (!engine) return '';
  return ` <span class="engine-badge">${engine}</span>`;
}

function formatVersionBadge(version, commit, engine, buildTime) {
  if (!version && !commit && !engine) return '';
  var buildAge = '';
  if (buildTime && buildTime !== 'unknown') {
    var age = timeAgo(buildTime);
    if (age && age !== '—') buildAge = ' <span class="build-age">(' + age + ')</span>';
  }
  var port = (typeof location !== 'undefined' && location.port) || '';
  var isProd = !port || port === '80' || port === '443';
  var GH = 'https://github.com/Kpa-clawbot/corescope';
  var parts = [];
  if (version && isProd) {
    var vTag = version.charAt(0) === 'v' ? version : 'v' + version;
    parts.push('<a href="' + GH + '/releases/tag/' + vTag + '" target="_blank" rel="noopener">' + vTag + '</a>');
  }
  if (commit && commit !== 'unknown') {
    var short = commit.length > 7 ? commit.slice(0, 7) : commit;
    parts.push('<a href="' + GH + '/commit/' + commit + '" target="_blank" rel="noopener">' + short + '</a>' + buildAge);
  }
  if (engine) parts.push('<span class="engine-badge">' + engine + '</span>');
  if (parts.length === 0) return '';
  return ' <span class="version-badge">' + parts.join(' · ') + '</span>';
}

// --- Favorites ---
const FAV_KEY = 'meshcore-favorites';
function getFavorites() {
  try { return JSON.parse(localStorage.getItem(FAV_KEY) || '[]'); } catch { return []; }
}
function isFavorite(pubkey) { return getFavorites().includes(pubkey); }
function toggleFavorite(pubkey) {
  const favs = getFavorites();
  const idx = favs.indexOf(pubkey);
  if (idx >= 0) favs.splice(idx, 1); else favs.push(pubkey);
  localStorage.setItem(FAV_KEY, JSON.stringify(favs));
  return idx < 0; // true if now favorited
}
function favStar(pubkey, cls, name) {
  const on = isFavorite(pubkey);
  return '<button class="fav-star ' + (cls || '') + (on ? ' on' : '') + '" data-fav="' + pubkey + '" data-name="' + escapeHtml(name || '') + '" data-tooltip="' + (on ? 'Remove from Favorites' : 'Add to Favorites') + '">' + (on ? '★' : '☆') + '</button>';
}
function bindFavStars(container, onToggle) {
  container.querySelectorAll('.fav-star').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const pk = btn.dataset.fav;
      const nodeName = btn.dataset.name || '';
      const nowOn = toggleFavorite(pk, nodeName);
      btn.textContent = nowOn ? '★' : '☆';
      btn.classList.toggle('on', nowOn);
      btn.title = nowOn ? 'Remove from My Nodes' : 'Add to My Nodes';
      if (onToggle) onToggle(pk, nowOn);
    });
  });
}

function formatHex(hex) {
  if (!hex) return '';
  return hex.match(/.{1,2}/g).join(' ');
}

function createColoredHexDump(hex, ranges) {
  if (!hex || !ranges || !ranges.length) return `<span class="hex-byte">${formatHex(hex)}</span>`;
  const bytes = hex.match(/.{1,2}/g) || [];
  // Build per-byte class map; later ranges override earlier
  const classMap = new Array(bytes.length).fill('');
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  for (const r of ranges) {
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    for (let i = r.start; i <= Math.min(r.end, bytes.length - 1); i++) classMap[i] = cls;
  }
  let html = '', prevCls = null;
  for (let i = 0; i < bytes.length; i++) {
    const cls = classMap[i];
    if (cls !== prevCls) {
      if (prevCls !== null) html += '</span>';
      html += `<span class="hex-byte ${cls}">`;
      prevCls = cls;
    } else {
      html += ' ';
    }
    html += bytes[i];
  }
  if (prevCls !== null) html += '</span>';
  return html;
}

function buildHexLegend(ranges) {
  if (!ranges || !ranges.length) return '';
  const LABEL_CLASS = {
    'Header': 'hex-header', 'Path Length': 'hex-pathlen', 'Transport Codes': 'hex-transport',
    'Path': 'hex-path', 'Payload': 'hex-payload', 'PubKey': 'hex-pubkey',
    'Timestamp': 'hex-timestamp', 'Signature': 'hex-signature', 'Flags': 'hex-flags',
    'Latitude': 'hex-location', 'Longitude': 'hex-location', 'Name': 'hex-name',
  };
  const BG_COLORS = {
    'hex-header': '#f38ba8', 'hex-pathlen': '#fab387', 'hex-transport': '#89b4fa',
    'hex-path': '#a6e3a1', 'hex-payload': '#f9e2af', 'hex-pubkey': '#f9e2af',
    'hex-timestamp': '#fab387', 'hex-signature': '#f38ba8', 'hex-flags': '#94e2d5',
    'hex-location': '#89b4fa', 'hex-name': '#cba6f7',
  };
  const seen = new Set();
  let html = '';
  for (const r of ranges) {
    if (seen.has(r.label)) continue;
    seen.add(r.label);
    const cls = LABEL_CLASS[r.label] || 'hex-payload';
    const bg = BG_COLORS[cls] || '#f9e2af';
    html += `<span><span class="swatch" style="background:${bg}"></span>${r.label}</span>`;
  }
  return html;
}

// --- WebSocket ---
let ws = null;
let wsListeners = [];

function setTraceColor(color) {
  const stop0 = document.getElementById('traceGradStop0');
  const stop1 = document.getElementById('traceGradStop1');
  const ripple = document.getElementById('brandTraceEndRipple');
  if (stop0) stop0.setAttribute('stop-color', color);
  if (stop1) stop1.setAttribute('stop-color', color);
  if (ripple) ripple.setAttribute('stroke', color);
}

function fireBrandPulse() {
  const pulse = document.getElementById('brandTracePulse');
  if (!pulse) return;
  pulse.classList.remove('trace-pulsing');
  void pulse.getBoundingClientRect();
  pulse.classList.add('trace-pulsing');
  clearTimeout(fireBrandPulse._endTimer);
  fireBrandPulse._endTimer = setTimeout(() => {
    const ripple = document.getElementById('brandTraceEndRipple');
    if (!ripple) return;
    ripple.classList.remove('trace-end-flash');
    void ripple.getBoundingClientRect();
    ripple.classList.add('trace-end-flash');
  }, 3200);
}

function connectWS() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  ws = new WebSocket(`${proto}//${location.host}`);
  ws.onopen = () => {
    document.getElementById('liveDot')?.classList.add('connected');
    setTraceColor('#22c55e');
    fireBrandPulse();
    clearInterval(connectWS._pulseInterval);
    connectWS._pulseInterval = setInterval(fireBrandPulse, 15000);
  };
  ws.onclose = () => {
    document.getElementById('liveDot')?.classList.remove('connected');
    setTraceColor('#94a3b8');
    clearInterval(connectWS._pulseInterval);
    setTimeout(connectWS, 3000);
  };
  ws.onerror = () => ws.close();
  ws.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data);
      // Debounce cache invalidation — don't nuke on every packet
      if (!api._invalidateTimer) {
        api._invalidateTimer = setTimeout(() => {
          api._invalidateTimer = null;
          invalidateApiCache('/stats');
          invalidateApiCache('/nodes');
        }, 5000);
      }
      wsListeners.forEach(fn => fn(msg));
    } catch {}
  };
}

function onWS(fn) { wsListeners.push(fn); }
function offWS(fn) { wsListeners = wsListeners.filter(f => f !== fn); }

/* Global escapeHtml — used by multiple pages */
function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

/* Global debounce */
function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

/* Debounced WS helper — batches rapid messages, calls fn with array of msgs */
function debouncedOnWS(fn, ms) {
  if (typeof ms === 'undefined') ms = 250;
  let pending = [];
  let timer = null;
  function handler(msg) {
    pending.push(msg);
    if (!timer) {
      timer = setTimeout(function () {
        const batch = pending;
        pending = [];
        timer = null;
        fn(batch);
      }, ms);
    }
  }
  onWS(handler);
  return handler; // caller stores this to pass to offWS() in destroy
}

// --- Router ---
const pages = {};

function registerPage(name, mod) { pages[name] = mod; }

let currentPage = null;

function closeNav() {
  document.querySelector('.nav-links')?.classList.remove('open');
  document.getElementById('navSheet')?.classList.remove('open');
  document.getElementById('navSheetBackdrop')?.classList.remove('open');
  document.body.classList.remove('nav-open');
  var bottomBtn = document.getElementById('bottomHamburger');
  if (bottomBtn) bottomBtn.setAttribute('aria-expanded', 'false');
  closeMoreMenu();
}

function closeMoreMenu() {
  var menu = document.getElementById('navMoreMenu');
  var btn = document.getElementById('navMoreBtn');
  if (menu) menu.classList.remove('open');
  if (btn) btn.setAttribute('aria-expanded', 'false');
}

function navigate() {
  closeNav();

  const hash = location.hash.replace('#/', '') || 'packets';
  const route = hash.split('?')[0];

  // Handle parameterized routes: nodes/<pubkey> → nodes page + select
  let basePage = route;
  let routeParam = null;
  const slashIdx = route.indexOf('/');
  if (slashIdx > 0) {
    basePage = route.substring(0, slashIdx);
    routeParam = decodeURIComponent(route.substring(slashIdx + 1));
  }

  // Special route: nodes/PUBKEY/analytics → node-analytics page
  if (basePage === 'nodes' && routeParam && routeParam.endsWith('/analytics')) {
    basePage = 'node-analytics';
  }

  // Special route: packet/123 → standalone packet detail page
  if (basePage === 'packet' && routeParam) {
    basePage = 'packet-detail';
  }

  // Special route: observers/ID → observer detail page
  if (basePage === 'observers' && routeParam) {
    basePage = 'observer-detail';
  }

  // Update nav active state
  document.querySelectorAll('.nav-link[data-route]').forEach(el => {
    el.classList.toggle('active', el.dataset.route === basePage);
  });
  document.querySelectorAll('.bottom-nav-item[data-route]').forEach(el => {
    el.classList.toggle('active', el.dataset.route === basePage);
  });
  document.querySelectorAll('#navSheet .nav-link[data-route]').forEach(el => {
    el.classList.toggle('active', el.dataset.route === basePage);
  });
  // Update "More" button to show active state if a low-priority page is selected
  var moreBtn = document.getElementById('navMoreBtn');
  if (moreBtn) {
    var moreMenu = document.getElementById('navMoreMenu');
    var hasActiveMore = moreMenu && moreMenu.querySelector('.nav-link.active');
    moreBtn.classList.toggle('active', !!hasActiveMore);
  }

  if (currentPage && pages[currentPage]?.destroy) {
    pages[currentPage].destroy();
  }
  currentPage = basePage;

  const app = document.getElementById('app');
  // Pages with fixed-height containers (maps, virtual-scroll, split-panels)
  const fixedPages = { packets: 1, nodes: 1, map: 1, live: 1, channels: 1, 'audio-lab': 1 };
  app.classList.toggle('app-fixed', basePage in fixedPages);
  window.scrollTo(0, 0);
  if (pages[basePage]?.init) {
    const t0 = performance.now();
    pages[basePage].init(app, routeParam);
    const ms = performance.now() - t0;
    if (ms > 100) console.warn(`[SLOW PAGE] ${basePage} init took ${Math.round(ms)}ms`);
    app.classList.remove('page-enter'); void app.offsetWidth; app.classList.add('page-enter');
    // #630-7: SPA focus management — move focus to first heading or main content
    requestAnimationFrame(function() {
      var heading = app.querySelector('h1, h2, h3, [role="heading"]');
      if (heading) { heading.setAttribute('tabindex', '-1'); heading.focus({ preventScroll: true }); }
      else { app.setAttribute('tabindex', '-1'); app.focus({ preventScroll: true }); }
      // iOS Safari: force fixed elements to reposition after content renders
      window.scrollTo(0, window.scrollY + 1);
      window.scrollTo(0, window.scrollY - 1);
    });
  } else {
    app.innerHTML = `<div style="padding:40px;text-align:center;color:#6b7280"><h2>${route}</h2><p>Page not yet implemented.</p></div>`;
  }

  if (typeof umami !== 'undefined') umami.track('view', { page: basePage });
}

window.addEventListener('hashchange', navigate);
let _themeRefreshTimer = null;
window.addEventListener('theme-changed', () => {
  if (_themeRefreshTimer) clearTimeout(_themeRefreshTimer);
  _themeRefreshTimer = setTimeout(() => {
    _themeRefreshTimer = null;
    window.dispatchEvent(new CustomEvent('theme-refresh'));
  }, 300);
});
window.addEventListener('timestamp-mode-changed', () => {
  window.dispatchEvent(new CustomEvent('theme-refresh'));
});
window.addEventListener('DOMContentLoaded', () => {
  // ── Custom tooltip engine ──
  (function () {
    const tip = document.createElement('div');
    tip.id = 'uiTooltip';
    document.body.appendChild(tip);
    let hideTimer;
    let tipPos = 'top';
    function pos(e) {
      const tw = tip.offsetWidth, th = tip.offsetHeight;
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      let x, y;
      if (tipPos === 'left') {
        x = e.clientX - tw - 12;
        y = e.clientY - th / 2;
        if (x < 6) x = e.clientX + 12;
        if (y < 6) y = 6;
        if (y + th > vh - 6) y = vh - th - 6;
      } else {
        x = e.clientX - tw / 2;
        y = e.clientY - th - 10;
        if (x < 6) x = 6;
        if (x + tw > vw - 6) x = vw - tw - 6;
        if (y < 6) y = e.clientY + 22;
      }
      tip.style.left = x + 'px';
      tip.style.top  = y + 'px';
    }
    document.addEventListener('mouseover', e => {
      const t = e.target.closest('[data-tooltip]');
      if (!t) return;
      clearTimeout(hideTimer);
      tip.textContent = t.dataset.tooltip;
      tipPos = t.dataset.tooltipPos || 'top';
      tip.style.opacity = '1';
      pos(e);
    });
    document.addEventListener('mousemove', e => {
      if (tip.style.opacity === '1') pos(e);
    });
    document.addEventListener('mouseout', e => {
      if (!e.target.closest('[data-tooltip]')) return;
      hideTimer = setTimeout(() => { tip.style.opacity = '0'; }, 60);
    });
    document.addEventListener('click', () => { tip.style.opacity = '0'; });
  })();

  // ── Copy-to-clipboard — global handler for [data-copy] elements ──
  (function () {
    let _toastEl = null;
    function showCopyToast(x, y) {
      if (_toastEl) { clearTimeout(_toastEl._timer); _toastEl.remove(); }
      const t = document.createElement('div');
      t.className = 'copy-toast';
      t.textContent = 'Copied!';
      t.style.left = (x + 10) + 'px';
      t.style.top = (y - 28) + 'px';
      document.body.appendChild(t);
      _toastEl = t;
      t._timer = setTimeout(() => {
        t.style.opacity = '0';
        setTimeout(() => { if (t.parentNode) t.remove(); if (_toastEl === t) _toastEl = null; }, 250);
      }, 900);
    }
    document.addEventListener('click', function (e) {
      const el = e.target.closest('[data-copy]');
      if (!el || !el.dataset.copy) return;
      window.copyToClipboard(el.dataset.copy, function () {
        showCopyToast(e.clientX, e.clientY);
      });
    });
    window.showCopyToast = showCopyToast;
  })();

  // Reposition trace nodes for mobile: dot 1 at start, dot 2 at midpoint
  if (window.matchMedia('(max-width: 767px)').matches) {
    const nodes = document.querySelectorAll('#brandTraceNodes circle');
    if (nodes[0]) nodes[0].setAttribute('cx', '4');
    if (nodes[1]) nodes[1].setAttribute('cx', '115');
  }
  fireBrandPulse();
  connectWS();

  // --- Dark Mode ---
  const darkToggle = document.getElementById('darkModeToggle');
  const savedTheme = localStorage.getItem('meshcore-theme');
  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    darkToggle.textContent = theme === 'dark' ? '🌙' : '☀️';
    localStorage.setItem('meshcore-theme', theme);
    // Re-apply user theme CSS vars for the correct mode (light/dark)
    reapplyUserThemeVars(theme === 'dark');
  }
  function reapplyUserThemeVars(dark) {
    try {
      var userTheme = JSON.parse(localStorage.getItem('meshcore-user-theme') || '{}');
      if (!userTheme.theme && !userTheme.themeDark) {
        // Fall back to server config
        var cfg = window.SITE_CONFIG || {};
        if (!cfg.theme && !cfg.themeDark) return;
        userTheme = cfg;
      }
      var themeData = dark ? Object.assign({}, userTheme.theme || {}, userTheme.themeDark || {}) : (userTheme.theme || {});
      if (!Object.keys(themeData).length) return;
      var varMap = {
        accent: '--accent', accentHover: '--accent-hover',
        navBg: '--nav-bg', navBg2: '--nav-bg2', navText: '--nav-text', navTextMuted: '--nav-text-muted',
        background: '--surface-0', text: '--text', textMuted: '--text-muted', border: '--border',
        statusGreen: '--status-green', statusYellow: '--status-yellow', statusRed: '--status-red',
        surface1: '--surface-1', surface2: '--surface-2', surface3: '--surface-3',
        cardBg: '--card-bg', contentBg: '--content-bg', inputBg: '--input-bg',
        rowStripe: '--row-stripe', rowHover: '--row-hover', detailBg: '--detail-bg',
        selectedBg: '--selected-bg', sectionBg: '--section-bg',
        font: '--font', mono: '--mono'
      };
      var root = document.documentElement.style;
      for (var key in varMap) {
        if (themeData[key]) root.setProperty(varMap[key], themeData[key]);
      }
      if (themeData.background) root.setProperty('--content-bg', themeData.contentBg || themeData.background);
      if (themeData.surface1) root.setProperty('--card-bg', themeData.cardBg || themeData.surface1);
      // Nav gradient
      if (themeData.navBg) {
        var nav = document.querySelector('.top-nav');
        if (nav) { nav.style.background = ''; void nav.offsetHeight; }
      }
    } catch (e) { console.error('[theme] reapply error:', e); }
  }
  // On load: respect saved pref, else OS pref, else light
  if (savedTheme) {
    applyTheme(savedTheme);
  } else if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
    applyTheme('dark');
  } else {
    applyTheme('light');
  }
  darkToggle.addEventListener('click', () => {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
    applyTheme(isDark ? 'light' : 'dark');
  });

  // --- Navigation Menu ---
  const bottomHamburger = document.getElementById('bottomHamburger');
  const navLinks = document.querySelector('.nav-links');
  const navSheet = document.getElementById('navSheet');
  const navSheetBackdrop = document.getElementById('navSheetBackdrop');
  function isBottomNavActive() { return Layout.isTabletOrBelow(); }
  function toggleNav() {
    if (isBottomNavActive() && navSheet) {
      const opening = !navSheet.classList.contains('open');
      navSheet.classList.toggle('open');
      if (navSheetBackdrop) navSheetBackdrop.classList.toggle('open');
      document.body.classList.toggle('nav-open', opening);
      if (bottomHamburger) bottomHamburger.setAttribute('aria-expanded', String(opening));
    } else {
      const opening = !navLinks.classList.contains('open');
      navLinks.classList.toggle('open');
      document.body.classList.toggle('nav-open');
    }
  }
  if (bottomHamburger) bottomHamburger.addEventListener('click', toggleNav);
  if (navSheetBackdrop) navSheetBackdrop.addEventListener('click', closeNav);
  // Close sheet on nav link click
  if (navSheet) navSheet.querySelectorAll('.nav-link').forEach(link => link.addEventListener('click', closeNav));
  navLinks.querySelectorAll('.nav-link').forEach(link => {
    link.addEventListener('click', closeNav);
  });

  // --- "More" dropdown (tablet Priority+ nav) ---
  const navMoreBtn = document.getElementById('navMoreBtn');
  const navMoreMenu = document.getElementById('navMoreMenu');
  if (navMoreBtn && navMoreMenu) {
    // Build More menu dynamically from non-priority nav links (DRY)
    navMoreMenu.innerHTML = '';
    document.querySelectorAll('.nav-links a:not([data-priority="high"])').forEach(function(link) {
      var clone = link.cloneNode(true);
      clone.setAttribute('role', 'menuitem');
      clone.addEventListener('click', closeMoreMenu);
      navMoreMenu.appendChild(clone);
    });
    navMoreBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      const opening = !navMoreMenu.classList.contains('open');
      navMoreMenu.classList.toggle('open');
      navMoreBtn.setAttribute('aria-expanded', String(opening));
      if (opening) {
        var firstLink = navMoreMenu.querySelector('.nav-link');
        if (firstLink) firstLink.focus();
      }
    });
  }

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      if (navMoreMenu && navMoreMenu.classList.contains('open')) closeMoreMenu();
      if (navLinks.classList.contains('open')) closeNav();
    }
  });
  document.addEventListener('click', (e) => {
    if (navLinks.classList.contains('open') &&
        !navLinks.contains(e.target) &&
        !(bottomHamburger && bottomHamburger.contains(e.target))) {
      closeNav();
    }
    if (navMoreMenu && navMoreMenu.classList.contains('open') &&
        !navMoreMenu.contains(e.target) &&
        !navMoreBtn.contains(e.target)) {
      closeMoreMenu();
    }
  });

  // --- Favorites dropdown ---
  const favToggle = document.getElementById('favToggle');
  const favDropdown = document.getElementById('favDropdown');
  let favOpen = false;

  favToggle.addEventListener('click', (e) => {
    e.stopPropagation();
    favOpen = !favOpen;
    if (favOpen) {
      renderFavDropdown();
      favDropdown.classList.add('open');
    } else {
      favDropdown.classList.remove('open');
    }
  });

  document.addEventListener('click', (e) => {
    if (favOpen && !e.target.closest('.nav-fav-wrap')) {
      favOpen = false;
      favDropdown.classList.remove('open');
    }
  });

  async function renderFavDropdown() {
    const favs = getFavorites();
    if (!favs.length) {
      favDropdown.innerHTML = '<div class="fav-dd-empty">No favorites yet.<br><small>Click ☆ on any node to add it.</small></div>';
      return;
    }
    favDropdown.innerHTML = '<div class="fav-dd-loading">Loading...</div>';
    const items = await Promise.all(favs.map(async (pk) => {
      try {
        const h = await api('/nodes/' + pk + '/health', { ttl: CLIENT_TTL.nodeHealth });
        const age = h.stats.lastHeard ? Date.now() - new Date(h.stats.lastHeard).getTime() : null;
        const _fth = getHealthThresholds(h.node && h.node.role);
        const status = age === null ? '🔴' : age < _fth.degradedMs ? '🟢' : age < _fth.silentMs ? '🟡' : '🔴';
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status">' + status + '</span>'
          + '<span class="fav-dd-name">' + (h.node.name || truncate(pk, 12)) + '</span>'
          + '<span class="fav-dd-meta">' + (h.stats.lastHeard ? timeAgo(h.stats.lastHeard) : 'never') + '</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      } catch {
        return '<a href="#/nodes/' + pk + '" class="fav-dd-item" data-key="' + pk + '">'
          + '<span class="fav-dd-status">❓</span>'
          + '<span class="fav-dd-name">' + truncate(pk, 16) + '</span>'
          + '<span class="fav-dd-meta">not found</span>'
          + favStar(pk, 'fav-dd-star')
          + '</a>';
      }
    }));
    favDropdown.innerHTML = items.join('');
    bindFavStars(favDropdown, () => renderFavDropdown());
    // Close dropdown on link click
    favDropdown.querySelectorAll('.fav-dd-item').forEach(a => {
      a.addEventListener('click', (e) => {
        if (e.target.closest('.fav-star')) { e.preventDefault(); return; }
        favOpen = false;
        favDropdown.classList.remove('open');
      });
    });
  }

  // --- Search ---
  const searchToggle = document.getElementById('searchToggle');
  const searchOverlay = document.getElementById('searchOverlay');
  const searchInput = document.getElementById('searchInput');
  const searchResults = document.getElementById('searchResults');
  let searchTimeout = null;

  searchToggle.addEventListener('click', () => {
    searchOverlay.classList.toggle('hidden');
    if (!searchOverlay.classList.contains('hidden')) {
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
  });
  searchOverlay.addEventListener('click', (e) => {
    if (e.target === searchOverlay) searchOverlay.classList.add('hidden');
  });
  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
      e.preventDefault();
      searchOverlay.classList.remove('hidden');
      searchInput.value = '';
      searchResults.innerHTML = '';
      searchInput.focus();
    }
    if (e.key === 'Escape') searchOverlay.classList.add('hidden');
  });

  searchInput.addEventListener('input', () => {
    clearTimeout(searchTimeout);
    const q = searchInput.value.trim();
    if (!q) { searchResults.innerHTML = ''; return; }
    searchTimeout = setTimeout(async () => {
      try {
        const [packets, nodes, channels] = await Promise.all([
          fetch('/api/packets?limit=5&hash=' + encodeURIComponent(q)).then(r => r.json()).catch(() => ({ packets: [] })),
          fetch('/api/nodes?search=' + encodeURIComponent(q)).then(r => r.json()).catch(() => []),
          fetch('/api/channels').then(r => r.json()).catch(() => [])
        ]);
        let html = '';
        const pktList = packets.packets || packets;
        if (Array.isArray(pktList)) {
          for (const p of pktList.slice(0, 5)) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/packets/${p.packet_hash || p.hash || p.id}">
              <span class="search-result-type">Packet</span>${truncate(p.packet_hash || '', 16)} — ${payloadTypeName(p.payload_type)}</div>`;
          }
        }
        const nodeList = Array.isArray(nodes) ? nodes : (nodes.nodes || []);
        for (const n of nodeList.slice(0, 5)) {
          if (n.name && n.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/nodes/${n.public_key}">
              <span class="search-result-type">Node</span>${escapeHtml(n.name)} — ${formatPubKey(n.public_key || '', n.hash_size, 16)}</div>`;
          }
        }
        const chList = Array.isArray(channels) ? channels : [];
        for (const c of chList) {
          if (c.name && c.name.toLowerCase().includes(q.toLowerCase())) {
            html += `<div class="search-result-item" tabindex="0" role="option" data-href="#/channels/${c.channel_hash}">
              <span class="search-result-type">Channel</span>${escapeHtml(c.name)}</div>`;
          }
        }
        if (!html) html = '<div class="search-no-results">No results found</div>';
        searchResults.innerHTML = html;
      } catch { searchResults.innerHTML = '<div class="search-no-results">Search error</div>'; }
    }, 300);
  });

  // #208 — Search results keyboard: click, Enter/Space, arrow-key navigation
  function activateSearchItem(item) {
    if (!item || !item.dataset.href) return;
    location.hash = item.dataset.href;
    searchOverlay.classList.add('hidden');
  }
  searchResults.addEventListener('click', (e) => {
    activateSearchItem(e.target.closest('.search-result-item'));
  });
  searchResults.addEventListener('keydown', (e) => {
    const item = e.target.closest('.search-result-item');
    if (!item) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      activateSearchItem(item);
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      const next = item.nextElementSibling;
      if (next && next.classList.contains('search-result-item')) next.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      const prev = item.previousElementSibling;
      if (prev && prev.classList.contains('search-result-item')) prev.focus();
      else searchInput.focus();
    }
  });
  searchInput.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      const first = searchResults.querySelector('.search-result-item');
      if (first) first.focus();
    }
  });

  // --- Login ---
  // (removed — no auth yet)

  // --- Nav Stats ---
  async function updateNavStats() {
    try {
      const stats = await api('/stats', { ttl: CLIENT_TTL.stats });
      const el = document.getElementById('navStats');
      if (el) {
        el.innerHTML = `<span class="stat-val">${stats.totalPackets}</span> pkts · <span class="stat-val">${stats.totalNodes}</span> nodes · <span class="stat-val">${stats.totalObservers}</span> obs${formatVersionBadge(stats.version, stats.commit, stats.engine, stats.buildTime)}`;
      }
    } catch {}
  }
  updateNavStats();
  setInterval(updateNavStats, 15000);
  debouncedOnWS(function () { updateNavStats(); });

  // --- Theme Customization ---
  // Fetch theme config and apply via customizer v2 pipeline
  fetch('/api/config/theme', { cache: 'no-store' }).then(r => r.json()).then(cfg => {
    // Normalize timestamp defaults
    cfg = cfg || {};
    if (!cfg.timestamps) cfg.timestamps = {};
    const tsCfg = cfg.timestamps;
    if (tsCfg.defaultMode !== 'absolute' && tsCfg.defaultMode !== 'ago') tsCfg.defaultMode = 'ago';
    if (tsCfg.timezone !== 'utc' && tsCfg.timezone !== 'local') tsCfg.timezone = 'local';
    if (tsCfg.formatPreset !== 'iso' && tsCfg.formatPreset !== 'iso-seconds' && tsCfg.formatPreset !== 'locale') tsCfg.formatPreset = 'iso';
    if (typeof tsCfg.customFormat !== 'string') tsCfg.customFormat = '';
    tsCfg.allowCustomFormat = tsCfg.allowCustomFormat === true;

    // Customizer v2: set server defaults and run full pipeline
    // (reads localStorage overrides → merges → sets SITE_CONFIG → applies CSS → dispatches theme-changed)
    if (window._customizerV2) {
      window._customizerV2.init(cfg);
    } else {
      // Fallback if customize-v2.js didn't load
      window.SITE_CONFIG = cfg;
    }
  }).catch(() => {
    window.SITE_CONFIG = { timestamps: { defaultMode: 'ago', timezone: 'local', formatPreset: 'iso', customFormat: '', allowCustomFormat: false } };
    if (window._customizerV2) window._customizerV2.init(window.SITE_CONFIG);
  }).finally(() => {
    if (!location.hash || location.hash === '#/') location.hash = '#/home';
    else navigate();
  });
});

/**
 * Reusable ARIA tab-bar initialiser.
 * Adds role="tablist" to container, role="tab" + aria-selected to each button,
 * and arrow-key navigation between tabs.
 * @param {HTMLElement} container - the tab bar element
 * @param {Function} [onChange] - optional callback(activeBtn) on tab change
 */
function initTabBar(container, onChange) {
  if (!container || container.getAttribute('role') === 'tablist') return;
  container.setAttribute('role', 'tablist');
  const tabs = Array.from(container.querySelectorAll('button, [data-tab], [data-obs]'));
  tabs.forEach(btn => {
    btn.setAttribute('role', 'tab');
    const isActive = btn.classList.contains('active');
    btn.setAttribute('aria-selected', String(isActive));
    btn.setAttribute('tabindex', isActive ? '0' : '-1');
    // Link to panel if aria-controls target exists
    const panelId = btn.dataset.tab || btn.dataset.obs;
    if (panelId && document.getElementById(panelId)) {
      btn.setAttribute('aria-controls', panelId);
    }
  });
  container.addEventListener('click', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn || !container.contains(btn)) return;
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    btn.setAttribute('aria-selected', 'true');
    btn.setAttribute('tabindex', '0');
    if (onChange) onChange(btn);
  });
  container.addEventListener('keydown', (e) => {
    const btn = e.target.closest('[role="tab"]');
    if (!btn) return;
    let idx = tabs.indexOf(btn), next = -1;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (idx + 1) % tabs.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (idx - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    tabs.forEach(b => { b.setAttribute('aria-selected', 'false'); b.setAttribute('tabindex', '-1'); });
    tabs[next].setAttribute('aria-selected', 'true');
    tabs[next].setAttribute('tabindex', '0');
    tabs[next].focus();
    tabs[next].click();
  });
}

/**
 * Make table columns resizable with drag handles. Widths saved to localStorage.
 * Call after table is in DOM. Re-call safe (idempotent per table).
 * @param {string} tableSelector - CSS selector for the table
 * @param {string} storageKey - localStorage key for persisted widths
 */
function makeColumnsResizable(tableSelector, storageKey) {
  const table = document.querySelector(tableSelector);
  if (!table) return;
  const thead = table.querySelector('thead');
  if (!thead) return;
  const ths = Array.from(thead.querySelectorAll('tr:first-child th'));
  if (ths.length < 2) return;

  if (table.dataset.resizable) return;
  table.dataset.resizable = '1';
  table.style.tableLayout = 'fixed';

  const containerW = table.parentElement.clientWidth;
  const saved = localStorage.getItem(storageKey);
  let widths;

  if (saved) {
    try { widths = JSON.parse(saved); } catch { widths = null; }
    // Validate: must be array of correct length with values summing to ~100 (percentages)
    if (widths && Array.isArray(widths) && widths.length === ths.length) {
      const sum = widths.reduce((s, w) => s + w, 0);
      if (sum > 90 && sum < 110) {
        // Saved percentages — apply directly
        table.style.tableLayout = 'fixed';
        table.style.width = '100%';
        ths.forEach((th, i) => { th.style.width = widths[i] + '%'; });
        // Skip measurement, jump to adding handles
        addResizeHandles();
        return;
      }
    }
    widths = null; // Force remeasure
  }

  if (!widths) {
    // Measure actual max content width per column by scanning visible rows
    const tbody = table.querySelector('tbody');
    const rows = tbody ? Array.from(tbody.querySelectorAll('tr')).slice(0, 30) : [];

    // Temporarily set auto layout to measure
    table.style.tableLayout = 'auto';
    table.style.width = 'auto';
    // Remove nowrap temporarily so we get true content width
    const cells = table.querySelectorAll('td, th');
    cells.forEach(c => { c.dataset.origWs = c.style.whiteSpace || ''; c.style.whiteSpace = 'nowrap'; });

    // Measure each column's max content width across header + rows
    widths = ths.map((th, i) => {
      let maxW = th.scrollWidth;
      rows.forEach(row => {
        const td = row.children[i];
        if (td) maxW = Math.max(maxW, td.scrollWidth);
      });
      return maxW + 4; // small padding buffer
    });

    cells.forEach(c => { c.style.whiteSpace = c.dataset.origWs || ''; delete c.dataset.origWs; });
  }

  // Now fit to container: if total > container, squish widest first
  const totalNeeded = widths.reduce((s, w) => s + w, 0);
  const finalWidths = [...widths];

  if (totalNeeded > containerW) {
    let excess = totalNeeded - containerW;
    const MIN_COL = 28;
    // Iteratively shave from widest columns
    while (excess > 0) {
      // Find current max width
      const maxW = Math.max(...finalWidths);
      if (maxW <= MIN_COL) break;
      // Find second-max to know our target
      const sorted = [...new Set(finalWidths)].sort((a, b) => b - a);
      const target = sorted.length > 1 ? Math.max(sorted[1], MIN_COL) : MIN_COL;
      // How many columns are at maxW?
      const atMax = finalWidths.filter(w => w >= maxW).length;
      const canShavePerCol = maxW - target;
      const neededPerCol = Math.ceil(excess / atMax);
      const shavePerCol = Math.min(canShavePerCol, neededPerCol);

      for (let i = 0; i < finalWidths.length; i++) {
        if (finalWidths[i] >= maxW) {
          const shave = Math.min(shavePerCol, excess);
          finalWidths[i] -= shave;
          excess -= shave;
          if (excess <= 0) break;
        }
      }
    }
  } else if (totalNeeded < containerW) {
    // Give surplus to the 2 widest columns (content-heavy ones)
    const surplus = containerW - totalNeeded;
    const indexed = finalWidths.map((w, i) => ({ w, i })).sort((a, b) => b.w - a.w);
    const topN = indexed.slice(0, Math.min(2, indexed.length));
    const topTotal = topN.reduce((s, x) => s + x.w, 0);
    topN.forEach(x => { finalWidths[x.i] += Math.round(surplus * (x.w / topTotal)); });
  }

  table.style.width = '100%';
  const totalFinal = finalWidths.reduce((s, w) => s + w, 0);
  ths.forEach((th, i) => { th.style.width = (finalWidths[i] / totalFinal * 100) + '%'; });

  addResizeHandles();

  // Re-init columns when container is resized (orientation change, window resize).
  // ResizeObserver fires after CSS has settled, so new container width is accurate.
  if (window.ResizeObserver && table.parentElement) {
    const _container = table.parentElement;
    let _lastKnownW = _container.clientWidth;
    const _ro = new ResizeObserver(() => {
      const newW = _container.clientWidth;
      if (!newW || Math.abs(newW - _lastKnownW) < 10) return;
      _lastKnownW = newW;
      _ro.disconnect();
      // Clear handles and inline widths set by this function
      table.querySelectorAll('.col-resize-handle').forEach(h => h.remove());
      ths.forEach(th => { th.style.width = ''; });
      table.style.tableLayout = '';
      table.style.width = '';
      delete table.dataset.resizable;
      makeColumnsResizable(tableSelector, storageKey);
    });
    _ro.observe(_container);
  }

  function addResizeHandles() {
  // Add resize handles
  ths.forEach((th, i) => {
    if (i === ths.length - 1) return;
    const handle = document.createElement('div');
    handle.className = 'col-resize-handle';
    handle.addEventListener('mousedown', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const startX = e.clientX;
      const startW = th.offsetWidth;
      const startTableW = table.offsetWidth;
      handle.classList.add('active');
      document.body.style.cursor = 'col-resize';
      document.body.style.userSelect = 'none';

      function onMove(e2) {
        const dx = e2.clientX - startX;
        const newW = Math.max(50, startW + dx);
        const delta = newW - th.offsetWidth;
        if (delta === 0) return;
        // Steal/give space from columns to the right, proportionally
        const rightThs = ths.slice(i + 1);
        const rightWidths = rightThs.map(t => t.offsetWidth);
        const rightTotal = rightWidths.reduce((s, w) => s + w, 0);
        if (rightTotal - delta < rightThs.length * 50) return; // can't squeeze below 50px each
        th.style.width = newW + 'px';
        const scale = (rightTotal - delta) / rightTotal;
        rightThs.forEach(t => { t.style.width = Math.max(50, t.offsetWidth * scale) + 'px'; });
      }
      function onUp() {
        handle.classList.remove('active');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        // Save as percentages
        const tableW = table.offsetWidth;
        const ws = ths.map(t => (t.offsetWidth / tableW * 100));
        localStorage.setItem(storageKey, JSON.stringify(ws));
        // Re-apply as percentages
        ths.forEach((t, j) => { t.style.width = ws[j] + '%'; });
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
    th.appendChild(handle);
  });
  } // end addResizeHandles
}

// ── Site Announcement Modal ────────────────────────────────────────────────────
(function () {
  const ANNOUNCEMENT_KEY = 'meshcore-announcement-v2';

  function showAnnouncement() {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay announcement-overlay';
    overlay.innerHTML = `
      <div class="modal announcement-modal">
        <div class="announcement-header">
          <span class="announcement-icon">📢</span>
          <h3>Network Announcement</h3>
        </div>
        <div class="announcement-body">
          <p style="font-size:16px;font-weight:700;color:#f59e0b;margin-bottom:14px;">MeshCore Regions testing is complete.</p>
          <p>Please update your repeater configuration:</p>
          <ul>
            <li>Set <span style="white-space:nowrap"><strong>Allow Flood</strong> for <code class="regions-code">*</code></span> to ensure packets without a region scope are not dropped.</li>
            <li>It is highly recommended to keep region <code class="regions-code">bc</code> set to <span style="white-space:nowrap"><strong>Flood Allowed</strong></span>. If you haven't already done this please follow the how-to guide.</li>
          </ul>
          <p style="margin-top:14px;">We are proposing Aug 1st – 31st for the next test of <span style="white-space:nowrap"><strong>Flood Deny <code class="regions-code">*</code></strong></span>, in hopes this provides repeater operators enough time to update firmware and modify their region settings.</p>
        </div>
        <div class="announcement-actions">
          <a href="#/regions" class="announcement-howto">How-to Guide →</a>
          <button class="announcement-close">Got it</button>
        </div>
      </div>`;

    document.body.appendChild(overlay);

    function dismiss() {
      localStorage.setItem(ANNOUNCEMENT_KEY, '1');
      overlay.remove();
    }

    overlay.querySelector('.announcement-close').addEventListener('click', dismiss);
    overlay.querySelector('.announcement-howto').addEventListener('click', dismiss);
    overlay.addEventListener('click', e => { if (e.target === overlay) dismiss(); });
  }

  window.showSiteAnnouncement = showAnnouncement;

  if (!localStorage.getItem(ANNOUNCEMENT_KEY)) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', showAnnouncement);
    } else {
      showAnnouncement();
    }
  }
}());
