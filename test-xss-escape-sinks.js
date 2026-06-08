/**
 * test-xss-escape-sinks.js
 *
 * Sink-level XSS regression tests for #1536 (CVE-class adv_name escaping).
 *
 * Strategy: for each render sink that interpolates untrusted node / observer /
 * channel data into HTML, locate the exact template literal in the production
 * source via regex, evaluate it with malicious bindings, and assert the
 * rendered HTML contains the *escaped* form (not the raw payload).
 *
 * This is "DOM-grep" output-string testing — equivalent to driving the render
 * function and inspecting container.innerHTML, but without a real DOM. The
 * captured template is the literal one from source, so reverting the
 * escapeHtml() wrapper in production code makes the regex still match (now
 * the unsafe template), the eval produces unsafe HTML, and the assertions
 * flip red. This is what gates the per-sink fixes, not just the helper.
 *
 * Each test below MUST fail on revert of the corresponding sink fix. We
 * verify this manually by neutering escapeHtml to identity and re-running:
 *   every sink test flips red.
 */
'use strict';
const fs = require('fs');
const assert = require('assert');
const vm = require('vm');

let passed = 0, failed = 0;
function test(name, fn) {
  try { fn(); passed++; console.log(`  ✅ ${name}`); }
  catch (e) { failed++; console.log(`  ❌ ${name}: ${e.message}`); }
}

// --- Malicious payloads exercised at every sink --------------------------
// TAG_PAYLOAD = inline <script>-style injection (must escape angle brackets)
// ATTR_PAYLOAD = single-quote attribute breakout (must escape ')
const TAG_PAYLOAD = '<img src=x onerror=alert(1)>';
const ATTR_PAYLOAD = "' onfocus=alert(1) '";

function assertNoXss(html, label) {
  // Raw <img with payload chars surviving → angle-bracket escape failed.
  if (/<img\b/i.test(html))
    throw new Error(label + ': raw <img tag survived → angle-bracket escape broken: ' + html);
  // Raw single-quote-followed-by-event-handler (attr breakout) survived.
  if (/'\s*onfocus\s*=/i.test(html))
    throw new Error(label + ": raw ' onfocus= survived → single-quote attr breakout: " + html);
  // Positive markers: escaped forms must appear.
  if (!html.includes('&lt;img'))
    throw new Error(label + ': missing &lt;img marker (tag was not escaped): ' + html);
  if (!html.includes('&#39;'))
    throw new Error(label + ": missing &#39; marker (single quote was not escaped): " + html);
}

// --- Load global escapeHtml from public/app.js into a real vm context ----
// We don't use the giant test-frontend-helpers sandbox — just extract the
// escapeHtml function definition and evaluate it standalone.
function loadEscapeHtml() {
  const src = fs.readFileSync('public/app.js', 'utf8');
  const m = src.match(/\/\* Global escapeHtml [\s\S]*?\nfunction escapeHtml\(s\) \{[\s\S]*?\n\}/);
  if (!m) throw new Error('could not locate global escapeHtml in public/app.js');
  const ctx = { escapeHtml: null };
  vm.createContext(ctx);
  vm.runInContext(m[0] + '\nthis.escapeHtml = escapeHtml;', ctx);
  return ctx.escapeHtml;
}
const escapeHtml = loadEscapeHtml();

// --- Template extraction + eval ------------------------------------------
// Captures a template literal from a source file, given a regex with ONE
// group that contains the inner of the backtick-delimited template.
// Evaluates it with the supplied bindings + escapeHtml in scope.
function evalTemplate(srcFile, regex, bindings) {
  const src = fs.readFileSync(srcFile, 'utf8');
  const m = src.match(regex);
  if (!m) throw new Error(`template not found in ${srcFile} via ${regex}`);
  const tpl = m[1];
  const argNames = Object.keys(bindings);
  const args = Object.values(bindings);
  // truncate / encodeURIComponent / escapeHtml / safeEsc may appear inside
  // the captured template — provide harmless implementations.
  const truncate = (s, n) => (s == null ? '' : String(s).slice(0, n));
  const safeEsc = escapeHtml; // map.js fallback path → real escaper
  const fn = new Function(
    ...argNames,
    'escapeHtml', 'truncate', 'encodeURIComponent', 'safeEsc',
    'return `' + tpl + '`;'
  );
  return fn(...args, escapeHtml, truncate, encodeURIComponent, safeEsc);
}

// =========================================================================
// A. Helper contract — escapeHtml MUST escape all 5 OWASP chars
// =========================================================================
console.log('\n=== A. escapeHtml 5-char contract (gates app.js:795) ===');

test("escapeHtml(\"'\") === '&#39;' (single-quote attribute-breakout fix)", () => {
  assert.strictEqual(escapeHtml("'"), '&#39;');
});

test('escapeHtml covers the full 5-char OWASP set', () => {
  assert.strictEqual(escapeHtml('&'),  '&amp;');
  assert.strictEqual(escapeHtml('<'),  '&lt;');
  assert.strictEqual(escapeHtml('>'),  '&gt;');
  assert.strictEqual(escapeHtml('"'),  '&quot;');
  assert.strictEqual(escapeHtml("'"),  '&#39;');
});

test('escapeHtml escapes & first (no double-decoding)', () => {
  assert.strictEqual(escapeHtml('a & b'), 'a &amp; b');
});

test('escapeHtml(null/undefined) → empty string', () => {
  assert.strictEqual(escapeHtml(null), '');
  assert.strictEqual(escapeHtml(undefined), '');
});

// =========================================================================
// B. safeEsc defensive pin — must NOT be identity-passthrough (regression
//    pin for #1536: the original bug was `safeEsc = function(s){return s;}`)
// =========================================================================
console.log('\n=== B. safeEsc identity-passthrough pin (gates public/map.js:30) ===');

test('public/map.js safeEsc fallback is NOT identity (returns s)', () => {
  const src = fs.readFileSync('public/map.js', 'utf8');
  // Look up the safeEsc declaration block and the inline fallback function body.
  const m = src.match(/const safeEsc[\s\S]{0,400}?function\s*\(s\)\s*\{([\s\S]*?)\}/);
  assert.ok(m, 'safeEsc fallback function not found in map.js');
  const body = m[1];
  // The original bug: `return s;` with no escaping. Pin that this string
  // does NOT appear as the fallback body.
  assert.ok(
    !/^[\s]*return\s+s\s*;?\s*$/m.test(body.trim()),
    'safeEsc fallback is identity passthrough — XSS regression: ' + body
  );
  // And positively: the body must reference an escaper or the angle brackets.
  assert.ok(
    /&lt;|&amp;|escapeHtml/.test(body),
    'safeEsc fallback does not escape angle brackets / amp / call escapeHtml: ' + body
  );
});

// =========================================================================
// C. Sink-level DOM-grep tests
// =========================================================================
console.log('\n=== C. per-sink XSS containment ===');

// --- 1. public/app.js search dropdown — Node result row -------------------
test('app.js search dropdown: Node row escapes n.name', () => {
  const html = evalTemplate(
    'public/app.js',
    /(<div class="search-result-item"[^`]*?<span class="search-result-type">Node<\/span>[^`]*?)`/,
    { n: { name: TAG_PAYLOAD + ATTR_PAYLOAD, public_key: 'abcdef0123456789' } }
  );
  assertNoXss(html, 'app.js search Node row');
});

// --- 2. public/app.js search dropdown — Channel row -----------------------
test('app.js search dropdown: Channel row escapes c.name', () => {
  const html = evalTemplate(
    'public/app.js',
    /(<div class="search-result-item"[^`]*?<span class="search-result-type">Channel<\/span>[^`]*?)`/,
    { c: { name: TAG_PAYLOAD + ATTR_PAYLOAD, channel_hash: 'deadbeef' } }
  );
  assertNoXss(html, 'app.js search Channel row');
});

// --- 3. public/nodes.js nodes-table row — name <strong> cell --------------
test('nodes.js renderRow: <strong>name</strong> cell escapes node name', () => {
  // Match the <strong>${...n.name...}</strong> token specifically.
  const html = evalTemplate(
    'public/nodes.js',
    /(<strong>\$\{[^}]*n\.name[^}]*\}<\/strong>)/,
    { n: { name: TAG_PAYLOAD + ATTR_PAYLOAD, public_key: 'abc' } }
  );
  assertNoXss(html, 'nodes.js renderRow strong cell');
});

// --- 4. public/observers.js observers-table row — name cell ---------------
test('observers.js renderRow: observer name cell escapes o.name', () => {
  // Capture just the <td class="mono">${ ... o.name || o.id ... }${chip}</td>.
  const html = evalTemplate(
    'public/observers.js',
    // Allow optional trailing content (e.g. listener/repeater badge added by #1290)
    // between the naive chip and the closing </td>.
    /(<td class="mono">\$\{[^}]*o\.name[^}]*\}\$\{window\.ObserversNaiveChip\.render\(o\)\}[^`]*?<\/td>)/,
    { o: { name: TAG_PAYLOAD + ATTR_PAYLOAD, id: 'obs-1' },
      window: { ObserversNaiveChip: { render: () => '' } } }
  );
  assertNoXss(html, 'observers.js obs name cell');
});

// --- 5. public/packets.js observer cell (grouped header — isSingle path) --
test('packets.js: grouped observer cell escapes observer name', () => {
  // Capture the isSingle ternary on the grouped header row.
  // Master form: `${isSingle ? truncate(obsNameOnly(headerObserverId), 16) + obsIataBadge(p) : ...}`
  // Fixed form:  `${isSingle ? escapeHtml(truncate(obsNameOnly(headerObserverId), 16)) + obsIataBadge(p) : ...}`
  const src = fs.readFileSync('public/packets.js', 'utf8');
  const m = src.match(
    /(<td class="col-observer"[^`]*?headerObserverId[^`]*?groupedObserverIataBadgesHtml\(p\)\}<\/td>)/
  );
  assert.ok(m, 'packets.js grouped observer cell template not found');
  const tpl = '`' + m[1] + '`';
  const fn = new Function(
    'isSingle','headerObserverId','escapeHtml','truncate','obsNameOnly',
    'obsIataBadge','groupedObserverIataBadgesHtml','p',
    'return ' + tpl + ';'
  );
  const html = fn(
    true, 'obs-1', escapeHtml,
    (s, n) => String(s).slice(0, n),
    () => TAG_PAYLOAD + ATTR_PAYLOAD,
    () => '', () => '', {}
  );
  assertNoXss(html, 'packets.js grouped observer cell');
});

// --- 6. public/packets.js child / flat observer cells --------------------
test('packets.js: flat observer cell escapes observer name', () => {
  // Capture the flat observer cell — the OUTER form, with or without
  // escapeHtml: `${truncate(obsNameOnly(p.observer_id), 16)}${obsIataBadge(p)}`
  // or            `${escapeHtml(truncate(obsNameOnly(p.observer_id), 16))}${obsIataBadge(p)}`.
  const src = fs.readFileSync('public/packets.js', 'utf8');
  // Find the FLAT row (the third occurrence with p.observer_id, not c.observer_id).
  const re = /\$\{(?:escapeHtml\()?truncate\(obsNameOnly\(p\.observer_id\),\s*16\)\)?\}\$\{obsIataBadge\(p\)\}/;
  const m = src.match(re);
  assert.ok(m, 'packets.js flat observer cell template not found');
  const tpl = '`<td>' + m[0] + '</td>`';
  const fn = new Function(
    'p','escapeHtml','truncate','obsNameOnly','obsIataBadge',
    'return ' + tpl + ';'
  );
  // Use short payloads — truncate(_, 16) would lop off the trailing single
  // quote of a longer string. Both must survive in the first 16 chars.
  const html = fn(
    { observer_id: 'obs-1' }, escapeHtml,
    (s, n) => String(s).slice(0, n),
    () => "'<img onerror=", // 14 chars — both ' and <img present
    () => ''
  );
  // Custom assertions (assertNoXss expects full payloads).
  assert.ok(!/<img/.test(html),
    'packets.js flat cell: raw <img survived: ' + html);
  assert.ok(!/^[^&]*'/.test(html.replace('<td>', '').replace('</td>','')),
    "packets.js flat cell: raw ' survived: " + html);
  assert.ok(html.includes('&lt;img'),
    'packets.js flat cell: &lt;img marker missing: ' + html);
  assert.ok(html.includes('&#39;'),
    'packets.js flat cell: &#39; marker missing: ' + html);
});

// --- 7. public/map.js Leaflet popup — observer popup ---------------------
test('map.js buildObserverPopup: observer name escaped (via real safeEsc)', () => {
  // Drive only the relevant template fragment. The full pipeline:
  //   name = safeEsc(obs.name || obs.id || 'Unknown')
  //   then `<h3>${name}</h3>`.
  // On master safeEsc is identity → name is raw → XSS.
  // On the fixed branch safeEsc falls back to a real escaper → name is safe.
  const src = fs.readFileSync('public/map.js', 'utf8');
  // Extract safeEsc fallback body.
  const m = src.match(/const safeEsc\s*=[\s\S]*?function\s*\(s\)\s*\{([\s\S]*?)\};?/);
  assert.ok(m, 'safeEsc declaration not found in map.js');
  const body = m[1];
  // Build a callable from the fallback body alone.
  const fallback = new Function('s', body);
  const name = fallback(TAG_PAYLOAD + ATTR_PAYLOAD);
  const html = '<h3>' + name + '</h3>';
  assertNoXss(html, 'map.js observer popup (safeEsc fallback)');
});

// --- 8. public/live.js hop popup (buildClickablePathPopupHtml) ----------
test('live.js buildClickablePathPopupHtml: hopNames join escapes each hop', () => {
  // Extract the function body and evaluate with a malicious hopNames array.
  const src = fs.readFileSync('public/live.js', 'utf8');
  const m = src.match(
    /function buildClickablePathPopupHtml\(typeName, color, hopNames, tsMs, hash\)\s*\{([\s\S]*?)\n\s*\}/
  );
  assert.ok(m, 'buildClickablePathPopupHtml not found');
  const body = m[1];
  const html = new Function(
    'typeName','color','hopNames','tsMs','hash','escapeHtml',
    body + '\n;' // body returns
  )(
    'CHAN', '#fff',
    [TAG_PAYLOAD, ATTR_PAYLOAD],
    Date.now(), 'abc123',
    escapeHtml
  );
  assertNoXss(html, 'live.js hop popup');
});

// --- 9. public/area-map.html node popup ----------------------------------
test('area-map.html node popup: node name escaped (local helper handles 5 chars)', () => {
  const src = fs.readFileSync('public/area-map.html', 'utf8');
  // Local escapeHtml must exist AND escape single quotes.
  const localM = src.match(/function escapeHtml\(s\)\s*\{[\s\S]*?\n\}/);
  assert.ok(localM, 'area-map.html local escapeHtml not found');
  assert.ok(/&#39;/.test(localM[0]),
    'area-map.html local escapeHtml missing &#39; (single-quote) escape: ' + localM[0]);
  // Eval the local helper.
  const localEsc = new Function('s',
    localM[0] + '\nreturn escapeHtml(s);');
  assert.strictEqual(localEsc("'"), '&#39;');
  // Drive the buildNodeLayer popup template (which uses ${escapeHtml(n.name||...)}
  // on the fixed branch; on master it's raw ${n.name || ...}).
  const popM = src.match(
    /(<div class="node-popup"><strong>\$\{(?:escapeHtml\()?n\.name[^`]*?<\/div>)/
  );
  assert.ok(popM, 'area-map node popup template not found');
  const tpl = popM[1];
  const html = new Function('n','areaKey','escapeHtml',
    'return `' + tpl + '`;'
  )(
    { name: TAG_PAYLOAD + ATTR_PAYLOAD,
      public_key: TAG_PAYLOAD + ATTR_PAYLOAD,
      lat: 0, lon: 0 },
    TAG_PAYLOAD + ATTR_PAYLOAD,
    localEsc
  );
  assertNoXss(html, 'area-map.html node popup');
});

// --- 10. public/hop-display.js — data-conflict single-quoted attribute --
test('hop-display.js data-conflict attribute: single-quote payload escaped', () => {
  // The sink: data-conflict='${escapeHtml(JSON.stringify({...}))}'.
  // Without ' in escapeHtml, a node name containing ' breaks out of the attr.
  // The fix is in escapeHtml itself; this asserts the contract holds end-to-end.
  const payload = JSON.stringify({ name: ATTR_PAYLOAD });
  const escaped = escapeHtml(payload);
  assert.ok(!/'/.test(escaped),
    "escapeHtml(JSON containing ') still contains raw ': " + escaped);
  assert.ok(/&#39;/.test(escaped),
    'escapeHtml output missing &#39; marker: ' + escaped);
  // And the source file still uses this attribute pattern.
  const src = fs.readFileSync('public/hop-display.js', 'utf8');
  assert.ok(/data-conflict='\$\{(escapeHtml\(JSON\.stringify|conflictData)/.test(src),
    'hop-display.js data-conflict attribute pattern changed unexpectedly');
});

// =========================================================================
// D. XSS sweep R2 — TRACE-1 / OBS-1 / ANL-1 (post-#1537 audit findings)
// =========================================================================
console.log('\n=== D. XSS R2: TRACE-1 / OBS-1 / ANL-1 ===');

// ----- TRACE-1: traces.js <input value="${urlHash}"> -----
// urlHash comes from URL fragment, interpolated raw → URL-fragment XSS.
test('TRACE-1: traces.js urlHash interpolation escapes URL-fragment payload', () => {
  const src = fs.readFileSync('public/traces.js', 'utf8');
  const m = src.match(/(<input type="text" id="traceHashInput"[^`]*?value="\$\{[^}]+\}"[^`]*?>)/);
  assert.ok(m, 'TRACE-1: <input value="${urlHash}"> template not found in traces.js');
  const tpl = m[1];
  const fn = new Function('urlHash', 'escapeHtml',
    'return `' + tpl + '`;');
  const html = fn(TAG_PAYLOAD + ATTR_PAYLOAD, escapeHtml);
  assertNoXss(html, 'TRACE-1 traces.js urlHash input');
});

// ----- OBS-1: observer-detail.js obs.* fields → renderDetail innerHTML -----
function extractObsStatValue(field) {
  const src = fs.readFileSync('public/observer-detail.js', 'utf8');
  const labelMap = { model: 'Model', firmware: 'Firmware', client_version: 'Client', iata: 'Region' };
  const label = labelMap[field];
  const re = new RegExp(
    '<div class="stat-label">' + label + '<\\/div>[\\s\\S]{0,250}?' +
    '<div class="stat-value"[^>]*>(\\$\\{[^}]*obs\\.' + field + '[^}]*\\})<\\/div>'
  );
  const m = src.match(re);
  if (!m) throw new Error('OBS-1: stat-value for ' + field + ' not found');
  return m[1];
}

test('OBS-1: observer-detail.js obs.model is escaped at render sink', () => {
  const expr = extractObsStatValue('model');
  const tpl = '<div>' + expr + '</div>';
  const fn = new Function('obs', 'escapeHtml', 'return `' + tpl + '`;');
  const html = fn({ model: TAG_PAYLOAD + ATTR_PAYLOAD }, escapeHtml);
  assertNoXss(html, 'OBS-1 obs.model');
});

test('OBS-1: observer-detail.js obs.firmware is escaped at render sink', () => {
  const expr = extractObsStatValue('firmware');
  const tpl = '<div>' + expr + '</div>';
  const fn = new Function('obs', 'escapeHtml', 'return `' + tpl + '`;');
  const html = fn({ firmware: TAG_PAYLOAD + ATTR_PAYLOAD }, escapeHtml);
  assertNoXss(html, 'OBS-1 obs.firmware');
});

test('OBS-1: observer-detail.js obs.client_version is escaped at render sink', () => {
  const expr = extractObsStatValue('client_version');
  const tpl = '<div>' + expr + '</div>';
  const fn = new Function('obs', 'escapeHtml', 'return `' + tpl + '`;');
  const html = fn({ client_version: TAG_PAYLOAD + ATTR_PAYLOAD }, escapeHtml);
  assertNoXss(html, 'OBS-1 obs.client_version');
});

// ----- ANL-1: analytics.js mutation-XSS via data-tip dataset round-trip -----
// `:1599` writes escapeHtml-escaped tipHtml into data-tip (attr storage
// entity-decodes), and `:1524` does `tip.innerHTML = td.dataset.tip` which
// reanimates the raw payload. Fix A: tip.textContent = td.dataset.tip.
test('ANL-1: analytics.js does NOT assign td.dataset.tip to tip.innerHTML', () => {
  const src = fs.readFileSync('public/analytics.js', 'utf8');
  const broken = /\btip\.innerHTML\s*=\s*td\.dataset\.tip\b/;
  assert.ok(!broken.test(src),
    'ANL-1: `tip.innerHTML = td.dataset.tip` still present (mutation-XSS). Use textContent.');
});

test('ANL-1: hashCellTd now emits per-field data-tip-* attrs (no HTML round-trip)', () => {
  // Post-#1539-round-2 contract (see test-anl1-tooltip-render.js for the
  // behavioral assertions). The XSS gate here: hashCellTd must NOT carry a
  // pre-rendered HTML string in a single data-tip attribute. Per-field
  // attrs (data-tip-hex / data-tip-status / data-tip-lines) are plain text
  // and are rebuilt into DOM via createElement + textContent.
  const src = fs.readFileSync('public/analytics.js', 'utf8');
  const m = src.match(/function hashCellTd\([^)]*\)\s*\{([\s\S]*?)\n\s{2}\}/);
  assert.ok(m, 'ANL-1: hashCellTd function not found');
  const body = m[1];
  assert.ok(!/data-tip\s*=\s*"\$\{tipHtml/.test(body),
    'ANL-1: hashCellTd still writes pre-rendered HTML to data-tip="${tipHtml...}"');
  assert.ok(/data-tip-hex/.test(body),
    'ANL-1: hashCellTd missing data-tip-hex attribute (spec-driven contract)');
  // Mouseover handler must use the new per-field reader, not innerHTML.
  assert.ok(/buildMatrixTipChildren\s*\(\s*tip\s*,\s*td\s*\)/.test(src),
    'ANL-1: mouseover handler no longer calls buildMatrixTipChildren(tip, td)');
  assert.ok(!/\btip\.innerHTML\s*=\s*td\.dataset\.tip\b/.test(src),
    'ANL-1: tip.innerHTML = td.dataset.tip is the mutation-XSS regression');
});

// =========================================================================
// E. OBS-1 expanded surface — iata, id, radio split parts (PR #1539 follow-up)
//     and OBS-2 Number() coercion at battery/uptime/noise_floor sinks.
// =========================================================================
console.log('\n=== E. OBS-1 expanded + OBS-2 Number() coercion ===');

// Capture the entire renderDetail template-literal block once. The block
// runs `el.innerHTML = \`<long template>\`` — we extract the template body
// between the first ${window.ObserverDetailNaiveBanner.render(obs)} marker
// and the closing `;`.
function loadRenderDetailTemplate() {
  const src = fs.readFileSync('public/observer-detail.js', 'utf8');
  const m = src.match(/el\.innerHTML\s*=\s*`([\s\S]*?)`;/);
  if (!m) throw new Error('renderDetail template literal not found');
  return m[1];
}

function renderObsDetail(obs) {
  const src = fs.readFileSync('public/observer-detail.js', 'utf8');
  const tpl = loadRenderDetailTemplate();
  // Extract the PRODUCTION radio-parsing block from source so reverting the
  // escapeHtml() calls inside that block flips red here. Don't duplicate it
  // in the test driver — that decouples the assertion from the fix.
  const radioBlockM = src.match(/let radioHtml[\s\S]*?if \(obs\.radio\) \{[\s\S]*?\n\s*\}/);
  if (!radioBlockM) throw new Error('renderObsDetail: radio-parsing block not found');
  const radioBlock = radioBlockM[0];
  // Stub out window globals and helpers used inside the template.
  const sandbox = {
    window: { ObserverDetailNaiveBanner: { render: () => '' } },
    escapeHtml,
    HEALTH_THRESHOLDS: { nodeDegradedMs: 1800000 },
    obsSkew: null,
    formatDuration: (s) => (s ? Math.floor(s / 60) + 'm' : '—'),
    timeAgo: () => '—',
    formatSkew: () => '',
    renderSkewBadge: () => '',
    observerSkewSeverity: () => 'ok',
  };
  const ago = obs.last_seen ? Date.now() - new Date(obs.last_seen).getTime() : Infinity;
  const statusCls = ago < 600000 ? 'health-green' : ago < sandbox.HEALTH_THRESHOLDS.nodeDegradedMs ? 'health-yellow' : 'health-red';
  const statusLabel = ago < 600000 ? 'Online' : ago < sandbox.HEALTH_THRESHOLDS.nodeDegradedMs ? 'Stale' : 'Offline';
  // Eval the radio block in a fn body, then return the template render.
  const fn = new Function(
    'obs', 'obsSkew', 'escapeHtml', 'window', 'HEALTH_THRESHOLDS',
    'formatDuration', 'timeAgo', 'formatSkew', 'renderSkewBadge', 'observerSkewSeverity',
    'statusCls', 'statusLabel',
    radioBlock + '\nreturn `' + tpl + '`;'
  );
  return fn(
    obs, sandbox.obsSkew, escapeHtml, sandbox.window, sandbox.HEALTH_THRESHOLDS,
    sandbox.formatDuration, sandbox.timeAgo, sandbox.formatSkew,
    sandbox.renderSkewBadge, sandbox.observerSkewSeverity,
    statusCls, statusLabel
  );
}

test('OBS-1 expanded: obs.iata payload is escaped', () => {
  const html = renderObsDetail({ id: 'obs-1', iata: TAG_PAYLOAD + ATTR_PAYLOAD });
  assertNoXss(html, 'OBS-1 obs.iata');
});

test('OBS-1 expanded: obs.id payload is escaped', () => {
  const html = renderObsDetail({ id: TAG_PAYLOAD + ATTR_PAYLOAD });
  assertNoXss(html, 'OBS-1 obs.id');
});

test('OBS-1 expanded: obs.radio[0] (frequency) payload is escaped', () => {
  const html = renderObsDetail({ id: 'obs-1', radio: TAG_PAYLOAD + ATTR_PAYLOAD + ',125,7,5' });
  assertNoXss(html, 'OBS-1 obs.radio[0]');
});

test('OBS-1 expanded: obs.radio[1] (BW) payload is escaped', () => {
  const html = renderObsDetail({ id: 'obs-1', radio: '868.5,' + TAG_PAYLOAD + ATTR_PAYLOAD + ',7,5' });
  assertNoXss(html, 'OBS-1 obs.radio[1]');
});

test('OBS-1 expanded: obs.radio[2] (SF) payload is escaped', () => {
  const html = renderObsDetail({ id: 'obs-1', radio: '868.5,125,' + TAG_PAYLOAD + ATTR_PAYLOAD + ',5' });
  assertNoXss(html, 'OBS-1 obs.radio[2]');
});

test('OBS-1 expanded: obs.radio[3] (CR) payload is escaped', () => {
  const html = renderObsDetail({ id: 'obs-1', radio: '868.5,125,7,' + TAG_PAYLOAD + ATTR_PAYLOAD });
  assertNoXss(html, 'OBS-1 obs.radio[3]');
});

// ----- OBS-2: Number() coercion strips string XSS payloads at sinks -----
test('OBS-2: contaminated string obs.battery_mv is Number()-coerced (no payload in HTML)', () => {
  const html = renderObsDetail({ id: 'obs-1', battery_mv: TAG_PAYLOAD });
  // Number(TAG_PAYLOAD) is NaN → renders as '—'. Raw payload must NOT survive.
  if (/<img\b/i.test(html))
    throw new Error('OBS-2 battery_mv: Number() coercion missing — raw <img survived: ' + html);
  if (html.includes(TAG_PAYLOAD))
    throw new Error('OBS-2 battery_mv: raw payload survived: ' + html);
});

test('OBS-2: contaminated string obs.noise_floor is Number()-coerced (no payload in HTML)', () => {
  const html = renderObsDetail({ id: 'obs-1', noise_floor: TAG_PAYLOAD });
  if (/<img\b/i.test(html))
    throw new Error('OBS-2 noise_floor: Number() coercion missing — raw <img survived: ' + html);
  if (html.includes(TAG_PAYLOAD))
    throw new Error('OBS-2 noise_floor: raw payload survived: ' + html);
});

// =========================================================================
// F. PR #1539 follow-up — renderRecentPackets must not emit inline onclick=
// =========================================================================
console.log('\n=== F. renderRecentPackets: no inline onclick (CSP / XSS-amplifier) ===');

test('renderRecentPackets row template does NOT use inline onclick=', () => {
  const src = fs.readFileSync('public/observer-detail.js', 'utf8');
  // Locate the renderRecentPackets function body and grep for onclick=.
  const m = src.match(/function renderRecentPackets\([^)]*\)\s*\{([\s\S]*?)\n\s{2}\}/);
  assert.ok(m, 'renderRecentPackets function not found');
  const body = m[1];
  // Strip JS line comments before grepping — explanatory comments are allowed
  // to mention the word `onclick=`. The contract is on the rendered HTML.
  const code = body.replace(/\/\/[^\n]*/g, '').replace(/\/\*[\s\S]*?\*\//g, '');
  assert.ok(!/\bonclick\s*=\s*['"]/i.test(code),
    'renderRecentPackets still contains inline onclick= attribute');
  // And positive contract: a delegated click listener on `el` exists.
  assert.ok(/el\.addEventListener\(\s*['"]click['"]/.test(body),
    'renderRecentPackets missing delegated click listener (data-action=navigate dispatch)');
});

// =========================================================================
// G. PR #1539 follow-up — loadDetail error path uses textContent
// =========================================================================
console.log('\n=== G. loadDetail error path: textContent over innerHTML ===');

test('loadDetail catch block does NOT interpolate e.message into innerHTML', () => {
  const src = fs.readFileSync('public/observer-detail.js', 'utf8');
  const m = src.match(/catch\s*\(\s*e\s*\)\s*\{([\s\S]*?)\n\s{4}\}/);
  assert.ok(m, 'loadDetail catch block not found');
  const body = m[1];
  // The old form interpolated e.message directly into a string assigned to
  // innerHTML. New form uses textContent.
  assert.ok(!/innerHTML\s*=\s*[^;]*e\.message/.test(body),
    'loadDetail still interpolates e.message into innerHTML: ' + body);
  assert.ok(/textContent\s*=\s*[^;]*e\.message/.test(body),
    'loadDetail missing textContent assignment for e.message: ' + body);
});

// =========================================================================
// SUMMARY
// =========================================================================
console.log('\n' + '═'.repeat(48));
console.log(`  XSS escape sinks: ${passed} passed, ${failed} failed`);
console.log('═'.repeat(48));
if (failed > 0) process.exit(1);
