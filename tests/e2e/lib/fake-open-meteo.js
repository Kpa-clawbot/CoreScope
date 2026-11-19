/* fake-open-meteo.js — deterministic stand-in for the Open-Meteo forecast API,
 * for testing the tropospheric-ducting overlay without the real service.
 * CommonJS, no dependencies. Used by tests/e2e/test-map-ducting-e2e.js and
 * runnable on its own to feed a CoreScope server:
 *
 *   node tests/e2e/lib/fake-open-meteo.js --port 13597 --scenario mixed
 *
 * and in the server's config.json:  "ducting": { "enabled": true,
 *   "baseUrl": "http://127.0.0.1:13597", "gridStepDeg": 0.5, ... }
 *
 * It answers GET /v1/forecast?latitude=a,b&longitude=x,y&hourly=<vars>
 *   &models=gfs_global&forecast_hours=N&timeformat=unixtime&timezone=GMT
 * exactly as cmd/server/ducting_source.go asks, and rejects (HTTP 400, and an
 * entry in `violations`) a request that does not match: wrong model, a
 * missing hourly variable, bad forecast_hours / timeformat, mismatched or
 * more than 50 locations.
 *
 * Introspection, for a test that did not start this process:
 *   GET /__state                  -> { scenario, requests: [...], violations: [...] }
 *   GET /__scenario?name=<name>   -> switches the scenario, answers { scenario }
 * (neither counts as a forecast request).
 *
 * Scenarios (start({scenario}) or setScenario()):
 *   mixed      about one 3-degree block in three is normal throughout (calm
 *              patches); elsewhere columns by a hash of the cell: normal,
 *              super-refractive, surface duct (traps 868/915 MHz), elevated
 *              duct. About one cell in three of those changes kind every 12
 *              forecast hours, so the time slider changes colours.
 *   pending    never answers (the socket is held until close())
 *   error500   HTTP 500
 *   ratelimit  HTTP 429
 *   nulls      half the cells have no pressure-level data at all (class 0),
 *              the rest are normal columns with some levels missing (null)
 *
 * Columns are physically consistent: temperature and humidity are defined as
 * a profile in height above ground; the geopotential heights follow from the
 * hypsometric equation. analyzeColumn() is an independent JS port of the
 * server's analysis (cmd/server/ducting_calc.go), so a test can check both
 * the intended kind (intendedKind) and the server's answer against it.
 */
'use strict';

const http = require('http');
const { URL } = require('url');

const LEVELS = [];
for (let p = 1000; p >= 700; p -= 25) LEVELS.push(p);
const BATCH_MAX = 50;
const KINDS = ['normal', 'super', 'surface', 'elevated'];
const CLASS_OF_KIND = { normal: 1, super: 2, elevated: 3, surface: 4 };
const SCENARIOS = ['mixed', 'pending', 'error500', 'ratelimit', 'nulls'];

function hash32(str) {
  let h = 2166136261;
  for (let i = 0; i < str.length; i++) { h ^= str.charCodeAt(i); h = Math.imul(h, 16777619); }
  h ^= h >>> 15; h = Math.imul(h, 2246822507); h ^= h >>> 13;
  return h >>> 0;
}
const cellKey = (lat, lon) => lat.toFixed(4) + ',' + lon.toFixed(4);

// ── Profiles: height above ground (m) -> temperature (°C) and RH (%) ────────
function lerp(a, b, f) { return a + (b - a) * Math.min(1, Math.max(0, f)); }

const PROFILES = {
  // Standard atmosphere lapse, moderate humidity.
  normal: (h, t0) => ({ t: t0 - 0.0065 * h, rh: lerp(60, 40, h / 3000) }),
  // Humidity falling quickly with height: N gradient about -100 N/km, no trap.
  super: (h, t0) => ({ t: t0 - 0.0065 * h, rh: lerp(85, 30, h / 600) }),
  // Strong, shallow surface inversion in the lowest 80 m with dry air above.
  surface: (h, t0) => {
    const f = h / 80;
    return { t: (t0 - 10) + 16 * Math.min(1, f) - 0.0065 * Math.max(0, h - 80), rh: lerp(96, 15, f) };
  },
  // Inversion with a dry layer between 700 and 950 m, normal below.
  elevated: (h, t0) => {
    const f = (h - 700) / 250;
    return { t: t0 - 0.0065 * h + 7 * lerp(0, 1, f), rh: lerp(80, 8, f) };
  },
};

function kindFor(scenario, lat, lon, hour) {
  // Calm regions: about one 3-degree block in three is normal throughout, so
  // (like real weather) calm air comes in patches and the overlay's field has
  // cells whose neighbours are calm too.
  if (hash32('calm:' + Math.floor(lat / 3) + ',' + Math.floor(lon / 3)) % 3 === 0) return 'normal';
  const h = hash32(cellKey(lat, lon));
  const varies = (h >>> 8) % 3 === 0;
  const base = h % KINDS.length;
  return KINDS[(base + (varies ? Math.floor(hour / 12) : 0)) % KINDS.length];
}

// A column for one cell: { elevation, hours: [{ t2m, rh2m, ps, temp[], rh[], geo[] }] }.
function buildColumn(kind, lat, lon, hours, scenario) {
  const h = hash32(cellKey(lat, lon));
  const elevation = 5 + (h % 20);                    // m; the 1000 hPa level stays ~100 m above ground
  const out = { elevation, hours: [] };
  for (let i = 0; i < hours; i++) {
    const k = typeof kind === 'function' ? kind(i) : kind;
    const prof = PROFILES[k];
    const t0 = 13 + ((h >>> 4) % 6) + 0.05 * i;      // 13..20 °C at ground, drifting
    const ps = 1013.25 * Math.exp(-elevation / 8434);
    const s = prof(2, t0);
    const hour = { t2m: round(s.t, 2), rh2m: round(s.rh, 1), ps: round(ps, 2), temp: [], rh: [], geo: [] };
    // Hypsometric integration upward from the surface point.
    let pPrev = ps, zPrev = elevation, tPrev = s.t;
    for (const p of LEVELS) {
      let z = zPrev + 29.27 * (tPrev + 273.15) * Math.log(pPrev / p);
      let pr = prof(z - elevation, t0);
      for (let it = 0; it < 2; it++) {              // refine with the layer-mean temperature
        z = zPrev + 29.27 * ((tPrev + pr.t) / 2 + 273.15) * Math.log(pPrev / p);
        pr = prof(z - elevation, t0);
      }
      hour.temp.push(round(pr.t, 2));
      hour.rh.push(round(pr.rh, 1));
      hour.geo.push(round(z, 1));
      pPrev = p; zPrev = z; tPrev = pr.t;
    }
    if (scenario === 'nulls') {
      if (h % 2 === 0) {                             // no pressure-level data at all
        hour.temp = hour.temp.map(() => null);
        hour.rh = hour.rh.map(() => null);
        hour.geo = hour.geo.map(() => null);
      } else {                                       // three levels missing
        for (const idx of [2, 5, 9]) { hour.temp[idx] = null; hour.rh[idx] = null; }
      }
    }
    out.hours.push(hour);
  }
  return out;
}
function round(v, d) { const m = Math.pow(10, d); return Math.round(v * m) / m; }

// ── Independent port of the server's analysis (ducting_calc.go) ─────────────
function refractivity(p, tC, rh) {
  const t = tC + 273.15;
  const e = rh / 100 * 6.1121 * Math.exp(17.502 * tC / (240.97 + tC));
  return 77.6 / t * (p + 4810 * e / t);
}
function analyzeColumnHour(elevation, hr) {
  const lv = [];
  const sane = (p, t, rh) => [p, t, rh].every(Number.isFinite) && p >= 100 && p <= 1100 && t >= -90 && t <= 60;
  if (sane(hr.ps, hr.t2m, hr.rh2m)) lv.push({ h: 2, p: hr.ps, t: hr.t2m, rh: Math.min(100, Math.max(0, hr.rh2m)) });
  LEVELS.forEach((p, k) => {
    const t = hr.temp[k], rh = hr.rh[k], z = hr.geo[k];
    if (t == null || rh == null || z == null) return;
    if (!sane(p, t, rh) || p >= hr.ps) return;
    const h = z - elevation;
    if (h <= 2 || h > 3000) return;
    lv.push({ h, p, t, rh: Math.min(100, Math.max(0, rh)) });
  });
  lv.sort((a, b) => a.h - b.h);
  const hh = [], mm = [], nn = [];
  lv.forEach((l, i) => {
    if (i > 0 && l.h === hh[hh.length - 1]) return;
    const n = refractivity(l.p, l.t, l.rh);
    hh.push(l.h); nn.push(n); mm.push(n + 0.157 * l.h);
  });
  const cnt = hh.length;
  if (cnt < 2) return { cls: 0, fmin: 0 };
  let minGrad = Infinity;
  for (let i = 0; i + 1 < cnt; i++) {
    const dh = hh[i + 1] - hh[i];
    if (dh > 0) minGrad = Math.min(minGrad, (nn[i + 1] - nn[i]) / (dh / 1000));
  }
  let found = false, surface = false, bestF = 0;
  for (let a = 0; a + 1 < cnt;) {
    if (!(mm[a + 1] < mm[a])) { a++; continue; }
    let b = a + 1;
    while (b + 1 < cnt && mm[b + 1] < mm[b]) b++;
    const mTop = mm[b], top = hh[b];
    let j = -1;
    for (let k = a - 1; k >= 0; k--) if (mm[k] <= mTop) { j = k; break; }
    let base = 0, from = 0;
    if (j >= 0) { base = hh[j] + (hh[j + 1] - hh[j]) * (mTop - mm[j]) / (mm[j + 1] - mm[j]); from = j; }
    let mMax = mm[from];
    for (let k = from + 1; k <= b; k++) mMax = Math.max(mMax, mm[k]);
    const d = top - base, dm = mMax - mTop;
    if (d > 0 && dm > 0) {
      const f = 120000 / (d * Math.sqrt(dm));
      if (!found || f < bestF) { found = true; surface = j < 0; bestF = f; }
    }
    a = b;
  }
  if (found) return { cls: surface ? 4 : 3, fmin: bestF };
  return { cls: minGrad <= -79 ? 2 : 1, fmin: 0 };
}

/**
 * What the scenario intends for a cell at a forecast hour: { kind, cls }.
 * cls is the class a correct server must report (0 = no data).
 */
function intendedKind(scenario, lat, lon, hour) {
  if (scenario === 'nulls') {
    const h = hash32(cellKey(lat, lon));
    return { kind: 'normal', cls: h % 2 === 0 ? 0 : 1 };
  }
  const kind = kindFor(scenario, lat, lon, hour);
  return { kind, cls: CLASS_OF_KIND[kind] };
}
/** The class and fmin the JS port computes from the generated column. */
function analyzeCell(scenario, lat, lon, hour, hours) {
  const n = hours || hour + 1;
  const col = buildColumn((i) => kindFor(scenario, lat, lon, i), lat, lon, n, scenario);
  return analyzeColumnHour(col.elevation, col.hours[hour]);
}

function hourlyVariables() {
  const v = ['temperature_2m', 'relative_humidity_2m', 'surface_pressure'];
  for (const p of LEVELS) v.push('temperature_' + p + 'hPa', 'relative_humidity_' + p + 'hPa', 'geopotential_height_' + p + 'hPa');
  return v;
}

function start(opts) {
  opts = opts || {};
  const model = opts.model || 'gfs_global';
  const state = { scenario: opts.scenario || 'mixed' };
  if (!SCENARIOS.includes(state.scenario)) throw new Error('unknown scenario ' + state.scenario);
  const requests = [];
  const violations = [];
  const held = new Set();
  const sockets = new Set();
  const expectVars = hourlyVariables();

  function reply(res, status, obj) {
    const body = JSON.stringify(obj);
    res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Content-Length': Buffer.byteLength(body) });
    res.end(body);
  }
  function bad(res, entry, reason) {
    entry.valid = false;
    entry.reason = reason;
    violations.push(reason);
    reply(res, 400, { error: true, reason });
  }

  const server = http.createServer((req, res) => {
    let u;
    try { u = new URL(req.url, 'http://fake.invalid'); } catch (_) { res.writeHead(400); res.end(); return; }
    if (u.pathname === '/__state') return reply(res, 200, { scenario: state.scenario, requests, violations });
    if (u.pathname === '/__scenario') {
      const name = u.searchParams.get('name');
      if (!SCENARIOS.includes(name)) return reply(res, 400, { error: true, reason: 'unknown scenario' });
      state.scenario = name;
      return reply(res, 200, { scenario: name });
    }
    const entry = { t: Date.now(), path: u.pathname, locations: 0, scenario: state.scenario, valid: true, reason: '' };
    requests.push(entry);
    if (u.pathname !== '/v1/forecast') { entry.valid = false; entry.reason = 'path ' + u.pathname; violations.push(entry.reason); return reply(res, 404, { error: true, reason: 'not found' }); }
    const q = u.searchParams;
    const lats = (q.get('latitude') || '').split(',').filter(Boolean).map(Number);
    const lons = (q.get('longitude') || '').split(',').filter(Boolean).map(Number);
    entry.locations = lats.length;
    if (state.scenario === 'pending') { held.add(res); res.on('close', () => held.delete(res)); return; }
    if (state.scenario === 'error500') return reply(res, 500, { error: true, reason: 'fake internal error' });
    if (state.scenario === 'ratelimit') return reply(res, 429, { error: true, reason: 'Too many concurrent requests' });

    if (!lats.length || lats.length !== lons.length) return bad(res, entry, 'latitude/longitude counts differ or are empty');
    if (lats.length > BATCH_MAX) return bad(res, entry, 'more than ' + BATCH_MAX + ' locations: ' + lats.length);
    if (lats.some((v) => !Number.isFinite(v) || Math.abs(v) > 90) || lons.some((v) => !Number.isFinite(v) || Math.abs(v) > 180)) return bad(res, entry, 'coordinate out of range');
    if (q.get('models') !== model) return bad(res, entry, 'models=' + q.get('models') + ' want ' + model);
    if (q.get('timeformat') !== 'unixtime') return bad(res, entry, 'timeformat=' + q.get('timeformat'));
    const fh = Number(q.get('forecast_hours'));
    if (!Number.isInteger(fh) || fh < 1 || fh > 384) return bad(res, entry, 'forecast_hours=' + q.get('forecast_hours'));
    const vars = (q.get('hourly') || '').split(',');
    const missing = expectVars.filter((v) => !vars.includes(v));
    if (missing.length) return bad(res, entry, 'hourly missing ' + missing.slice(0, 3).join(',') + (missing.length > 3 ? ' (+' + (missing.length - 3) + ')' : ''));
    entry.hours = fh;

    const t0 = Math.floor(Date.now() / 3600000) * 3600;
    const times = Array.from({ length: fh }, (_, i) => t0 + 3600 * i);
    const out = lats.map((lat, i) => {
      const lon = lons[i];
      const col = buildColumn((hr) => kindFor(state.scenario, lat, lon, hr), lat, lon, fh, state.scenario);
      const hourly = { time: times };
      hourly.temperature_2m = col.hours.map((x) => x.t2m);
      hourly.relative_humidity_2m = col.hours.map((x) => x.rh2m);
      hourly.surface_pressure = col.hours.map((x) => x.ps);
      LEVELS.forEach((p, k) => {
        hourly['temperature_' + p + 'hPa'] = col.hours.map((x) => x.temp[k]);
        hourly['relative_humidity_' + p + 'hPa'] = col.hours.map((x) => x.rh[k]);
        hourly['geopotential_height_' + p + 'hPa'] = col.hours.map((x) => x.geo[k]);
      });
      return { latitude: lat, longitude: lon, elevation: col.elevation, utc_offset_seconds: 0, timezone: 'GMT', hourly };
    });
    reply(res, 200, out.length === 1 ? out[0] : out);
  });
  server.on('connection', (s) => { sockets.add(s); s.on('close', () => sockets.delete(s)); });

  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port || 0, opts.host || '127.0.0.1', () => {
      const port = server.address().port;
      resolve({
        url: 'http://' + (opts.host || '127.0.0.1') + ':' + port,
        port,
        requests,
        violations,
        get scenario() { return state.scenario; },
        setScenario(s) { if (!SCENARIOS.includes(s)) throw new Error('unknown scenario ' + s); state.scenario = s; },
        close() {
          return new Promise((done) => {
            held.forEach((r) => { try { r.destroy(); } catch (_) {} });
            sockets.forEach((s) => { try { s.destroy(); } catch (_) {} });
            server.close(() => done());
          });
        },
      });
    });
  });
}

module.exports = { start, LEVELS, KINDS, CLASS_OF_KIND, SCENARIOS, BATCH_MAX, hourlyVariables, buildColumn, kindFor, intendedKind, analyzeCell, analyzeColumnHour, cellKey };

if (require.main === module) {
  const argv = process.argv.slice(2);
  const arg = (n, d) => { const i = argv.indexOf('--' + n); return i >= 0 ? argv[i + 1] : d; };
  start({ port: parseInt(arg('port', '13597'), 10), scenario: arg('scenario', 'mixed') }).then((f) => {
    console.log('fake Open-Meteo on ' + f.url + ' scenario=' + f.scenario);
    const stop = () => f.close().then(() => process.exit(0));
    process.on('SIGINT', stop);
    process.on('SIGTERM', stop);
  }, (e) => { console.error(e.message); process.exit(1); });
}
