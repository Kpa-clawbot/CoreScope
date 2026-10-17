/**
 * Inferred hop paths in the packet detail pane and the map route view.
 *
 * The detail pane shows one observation's path. Its hops are inferred as a
 * whole for the observer that heard it (HopResolver.inferPath), and rendered
 * by position: a prefix seen twice in one path can be two different nodes.
 * Every node here is synthetic.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const vm = require('vm');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const pad = s => s + '0'.repeat(64 - s.length);
const OBS = 'OBSERVER0001';
const OTHER = 'OBSERVER0002';
const NODES = [
  { public_key: pad('aa01'), name: 'A-START', role: 'repeater', lat: 40.00, lon: -100 },
  { public_key: pad('b1b1'), name: 'B-MID', role: 'repeater', lat: 40.25, lon: -100 },
  { public_key: pad('aa02'), name: 'A-END', role: 'repeater', lat: 40.50, lon: -100 },
  // Two equidistant candidates for "f3", either side of the B-MID → A-END line.
  { public_key: pad('f300'), name: 'F-WEST', role: 'repeater', lat: 40.375, lon: -100.3 },
  { public_key: pad('f311'), name: 'F-EAST', role: 'repeater', lat: 40.375, lon: -99.7 },
  // The only "56" node is ~1000 km away.
  { public_key: pad('5600'), name: 'FAR-56', role: 'repeater', lat: 49.5, lon: -100 },
  { public_key: pad('6600'), name: 'NO-GPS-66', role: 'repeater', lat: null, lon: null },
];
const pk = name => NODES.find(n => n.name === name).public_key;
const OBSERVERS = [
  { id: OBS, name: 'Obs One', iata: 'TST', lat: 40.6, lon: -100 },
  { id: OTHER, name: 'Obs Two', iata: 'TST', lat: 40.0, lon: -100.1 },
];

function loadPage() {
  const appended = [];
  const ctx = {
    window: { addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {}, innerWidth: 1200, PacketFilter: null, scrollX: 0, scrollY: 0 },
    document: {
      readyState: 'complete',
      createElement: () => ({ id: '', textContent: '', innerHTML: '', className: '', style: {},
        appendChild: () => {}, setAttribute: () => {}, addEventListener: () => {}, remove: () => {},
        querySelectorAll: () => [], querySelector: () => null,
        classList: { add: () => {}, remove: () => {}, contains: () => false } }),
      head: { appendChild: () => {} }, getElementById: () => null,
      addEventListener: () => {}, removeEventListener: () => {},
      querySelectorAll: () => [], querySelector: () => null, body: { appendChild: el => appended.push(el) },
    },
    console, Date, Infinity, Math, Array, Object, String, Number, JSON, RegExp,
    Error, TypeError, RangeError, parseInt, parseFloat, isNaN, isFinite,
    encodeURIComponent, decodeURIComponent,
    setTimeout: () => {}, clearTimeout: () => {}, setInterval: () => {}, clearInterval: () => {},
    fetch: () => Promise.resolve({ ok: true, json: () => Promise.resolve({}) }),
    performance: { now: () => Date.now() },
    localStorage: (() => { const st = {}; return {
      getItem: k => st[k] || null, setItem: (k, v) => { st[k] = String(v); }, removeItem: k => { delete st[k]; },
    }; })(),
    location: { hash: '' }, history: { replaceState: () => {} },
    CustomEvent: class CustomEvent {}, Map, Set, Promise, URLSearchParams,
    addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => {},
    requestAnimationFrame: () => {},
    registerPage: () => {},
  };
  vm.createContext(ctx);
  const loadFile = f => {
    vm.runInContext(fs.readFileSync(REPO_ROOT + '/public/' + f, 'utf8'), ctx, { filename: f });
    for (const k of Object.keys(ctx.window)) ctx[k] = ctx.window[k];
  };
  ['payload-labels.js', 'roles.js', 'app.js', 'packet-helpers.js', 'hop-resolver.js', 'hop-filter.js',
    'hop-display.js', 'packets.js'].forEach(loadFile);
  ctx.apiCalls = [];
  ctx.fetchAllNodes = () => Promise.resolve({ nodes: NODES });
  ctx.api = path => {
    ctx.apiCalls.push(path);
    return Promise.resolve(
      path === '/observers' ? { observers: OBSERVERS }
      : path === '/iata-coords' ? { coords: { TST: { lat: 40.5, lon: -100 } } }
      : path === '/analytics/neighbor-graph' ? { edges: [] }
      : {});
  };
  ctx.appended = appended;
  return ctx;
}

const hopParts = html => html.split('<span class="arrow">→</span>');
const text = html => html.replace(/<[^>]+>/g, '').trim();
const hasBadge = part => /hop-conflict-btn/.test(part);
const panelStub = () => ({ innerHTML: '', querySelectorAll: () => [], querySelector: () => null, addEventListener: () => {} });
const rowHtml = (html, dt) => {
  const m = html.match(new RegExp('<dt>' + dt + '</dt><dd>([\\s\\S]*?)</dd>'));
  return m ? m[1] : '';
};

(async () => {
  console.log('\n=== HopDisplay renders inferred entries ===');
  {
    const ctx = loadPage();
    const H = ctx.HopResolver, HD = ctx.HopDisplay;
    H.init(NODES, { observers: OBSERVERS, iataCoords: { TST: { lat: 40.5, lon: -100 } } });
    const obsPos = { lat: 40.6, lon: -100 };

    const sure = H.inferPath(['aa', 'b1b1', 'aa'], { observer: obsPos });
    const last = HD.renderHop('aa', sure[2]);
    assert(/A-END/.test(last) && !hasBadge(last), 'a confident hop shows its name without a badge');
    assert(/title="aa — \d+% likely/.test(last), 'its title gives the likelihood (' + (last.match(/title="([^"]*)"/) || [])[1] + ')');

    const tie = H.inferPath(['b1b1', 'f3', 'aa'], { observer: obsPos });
    const unsure = HD.renderHop('f3', tie[1]);
    assert(tie[1].confidence < 0.7, 'fixture: the f3 tie is not confident (' + tie[1].confidence.toFixed(2) + ')');
    assert(hasBadge(unsure) && /hop-ambiguous/.test(unsure), 'an unsure hop gets the candidate badge and the dashed underline');

    const far = H.inferPath(['aa', '56', 'aa'], { observer: obsPos });
    const unlisted = HD.renderHop('56', far[1]);
    assert(far[1].unlisted && far[1].unreliable, 'fixture: "56" is unlisted, its one candidate rejected');
    assert(text(unlisted).startsWith('56'), 'an unlisted hop shows its prefix, not the rejected name (' + text(unlisted) + ')');
    assert(!/hop-link/.test(unlisted) && /hop-unlisted/.test(unlisted), 'it links to no node and is styled as unlisted');
    assert(hasBadge(unlisted), 'its badge opens the rejected candidates');

    const lone = HD.renderHop('bb', { hop: 'bb', name: 'ONLY-ONE', pubkey: pk('B-MID'), located: true, unlisted: false,
      confidence: 0.5, candidates: [{ name: 'ONLY-ONE', pubkey: pk('B-MID'), located: true, confidence: 0.5 }] });
    assert(/hop-ambiguous/.test(lone) && !hasBadge(lone), 'a guess with no alternative is dashed, without a badge');

    const noGps = H.inferPath(['aa', '66', 'aa'], { observer: obsPos });
    const unplaced = HD.renderHop('66', noGps[1]);
    assert(/NO-GPS-66/.test(unplaced) && /hop-link/.test(unplaced), 'a sole candidate without GPS keeps its name and link');
    assert(/no GPS/.test(unplaced), 'and says it could not be placed');

    // The popover reads the badge's data attribute, as a click would.
    const attr = (unsure.match(/data-conflict='([^']*)'/) || [])[1] || '';
    const data = attr.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
    ctx.appended.length = 0;
    HD._showFromBtn({ dataset: { conflict: data }, getBoundingClientRect: () => ({ bottom: 0, left: 0 }) });
    const pop = ctx.appended.length ? ctx.appended[0].innerHTML : '';
    assert(/F-WEST/.test(pop) && /F-EAST/.test(pop), 'the popover lists both candidates');
    assert(/\d+%/.test(pop), 'with how likely each is');
    assert(!/global fallback|in region/.test(pop), 'and none of the region wording of resolve() entries');
  }

  console.log('\n=== packets.js renderPath renders entries by position ===');
  {
    const ctx = loadPage();
    const t = ctx._packetsTestAPI;
    ctx.HopResolver.init(NODES);
    const hops = ['aa', 'b1b1', 'aa'];
    const entries = ctx.HopResolver.inferPath(hops, { observer: { lat: 40.6, lon: -100 } });
    const parts = hopParts(t.renderPath(hops, OBS, { entries })).map(text);
    assert(parts[0] === 'A-START' && parts[2] === 'A-END', 'the two "aa" hops show two nodes (' + parts.join(' → ') + ')');
    ctx.MC_setHide1ByteHops(true);
    const filtered = hopParts(t.renderPath(hops, OBS, { entries })).map(text);
    assert(filtered.length === 1 && filtered[0] === 'B-MID', 'with 1-byte hops hidden, entries stay aligned (' + filtered.join(' → ') + ')');
    ctx.MC_setHide1ByteHops(false);
  }

  console.log('\n=== renderDetail infers the shown observation, for its own observer ===');
  {
    const ctx = loadPage();
    const t = ctx._packetsTestAPI;
    const panel = panelStub();
    // The packet row came from another observer, with a resolved_path of the
    // same length that names other nodes. The detail must not borrow it.
    const pkt = { id: 7, hash: 'h7', observer_id: OTHER, route_type: 1, payload_type: 5,
      raw_hex: '1503AAB1AA' + 'CC'.repeat(24), path_json: '["aa","b1b1","aa"]',
      resolved_path: JSON.stringify([pk('A-END'), pk('B-MID'), pk('A-START')]),
      decoded_json: JSON.stringify({ type: 'CHAN', channel: '#test', sender: 'S', text: 'S: hi' }) };
    const observations = [
      { id: 71, observer_id: OBS, path_json: '["aa","b1b1","aa"]', resolved_path: null, raw_hex: '1503AAB1AA' + 'CC'.repeat(24) },
      { id: 72, observer_id: OTHER, path_json: '["aa","b1b1","aa"]', resolved_path: pkt.resolved_path, raw_hex: '1503AAB1AA' + 'CC'.repeat(24) },
    ];
    await t.renderDetail(panel, { packet: pkt, observations }, 71);
    const path = hopParts(rowHtml(panel.innerHTML, 'Path')).map(text);
    assert(path.length === 3, 'the Path row shows the observation\'s 3 hops');
    assert(/A-START$/.test(path[0]) && path[1] === 'B-MID' && path[2] === 'A-END',
      'its hops are inferred toward the observer that heard it (' + path.join(' → ') + ')');
    assert(/Hop 0 — [\s\S]*A-START/.test(panel.innerHTML) && /Hop 2 — [\s\S]*A-END/.test(panel.innerHTML),
      'the byte breakdown names each hop by position');
    assert(ctx.apiCalls.filter(p => p === '/analytics/neighbor-graph').length === 1, 'the neighbour graph is fetched once');

    const agg = (panel.innerHTML.match(/Longest observed path:([\s\S]*?)<\/div>/) || [])[1] || '';
    const aggHops = hopParts(agg).map(text);
    assert(/A-END/.test(aggHops[0] || '') && /A-START/.test(aggHops[2] || ''),
      'the aggregate row keeps the packet\'s own server-resolved path (' + aggHops.join(' → ') + ')');

    // Nothing the detail inferred leaks into the shared prefix cache.
    const bare = text(t.renderPath(['aa'], undefined));
    assert(bare === 'aa', 'the list cache stays untouched by the detail (' + bare + ')');

    await t.renderDetail(panel, { packet: pkt, observations }, 71);
    assert(ctx.apiCalls.filter(p => p === '/analytics/neighbor-graph').length === 1, 'a second render reuses it');
  }

  console.log('\n=== shared helpers for the map route view ===');
  {
    const ctx = loadPage();
    ctx.HopResolver.init(NODES, { observers: OBSERVERS, iataCoords: {} });
    const obs = { observer_id: OBS, path_json: '["aa","56","66","aa"]', resolved_path: null };
    const entries = ctx.inferObservationPath(obs, { lat: 40.6, lon: -100 }, null);
    assert(entries.length === 4 && entries[3].name === 'A-END', 'inferObservationPath infers the observation\'s hops');
    const hinted = ctx.inferObservationPath({ observer_id: OBS, path_json: '["aa"]', resolved_path: JSON.stringify([pk('A-START')]) },
      { lat: 40.6, lon: -100 }, null);
    assert(hinted[0].name === 'A-START', 'and takes the observation\'s own resolved_path as a hint');

    const route = entries.map((e, i) => ctx.routeHopFromInferred(['aa', '56', '66', 'aa'][i], e));
    assert(route[0].resolved === true && route[0].lat === 40.0 && route[0].name === 'A-START', 'a located hop becomes a positioned route hop');
    assert(route[1].resolved === false && route[1].unlisted === true && route[1].pubkey === '56',
      'an unlisted hop becomes a placeholder, not the far node sharing its prefix');
    assert(route[2].resolved === false && route[2].gpsless === true && route[2].name === 'NO-GPS-66', 'an unplaced node stays named, without GPS');
  }

  console.log('\n=== map.js draws the inferred hops of the chosen observation ===');
  {
    const src = fs.readFileSync(REPO_ROOT + '/public/map.js', 'utf8');
    assert(/inferObservationPath\(chosen/.test(src), 'the deep link infers the chosen observation');
    assert((src.match(/inferredHops:/g) || []).length >= 2, 'and hands the inferred hops to both route renderers');
    assert((src.match(/inferredRouteHops\(/g) || []).length >= 3, 'both renderers build route hops from them');
    assert(/routeHopFromInferred\(/.test(src), 'through the shared routeHopFromInferred()');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
})();
