#!/usr/bin/env node
/* Issue #1297 — B1 audio-lab coverage.
 *
 * Exercises public/audio-lab.js (562 LOC, was 4.2% coverage) via /#/audio-lab.
 *
 * The page calls GET /api/audio-lab/buckets — we INTERCEPT this in the test
 * with a deterministic stub so coverage doesn't depend on whether the CI
 * fixture has the right packet mix. This still exercises the production
 * code path (no source patching) — just a network-level stub.
 *
 * Asserts:
 *   (a) page mounts, sidebar lists at least 2 type buckets
 *   (b) clicking a packet selects it (`.alab-pkt.selected` count = 1)
 *   (c) selection populates #alabDetail with hex (.alab-hex), note table
 *       (.alab-note-table), byte viz (.alab-byte-viz) — exercises
 *       renderDetail + computeMapping
 *   (d) clicking a type header toggles its packet list visibility
 *       (`[data-type-list="..."].style.display`)
 *   (e) BPM slider updates #alabBPMVal text
 *   (f) Volume slider updates #alabVolVal text and MeshAudio.getVolume()
 *   (g) Speed buttons toggle .active class on .alab-speed (covers speed switch handler)
 *   (h) Loop button toggles .active class on #alabLoop
 *   (i) Play button triggers MeshAudio.sonifyPacket (oscillator count increments
 *       with stubbed AudioContext)
 *   (j) destroy(): navigating away clears styles + timers
 *
 * Stable selectors: #alabSidebar, #alabPlay, #alabLoop, #alabBPM, #alabVol,
 * #alabBPMVal, #alabVolVal, #alabVoice, .alab-pkt, .alab-type-hdr,
 * .alab-speed, #alabDetail, .alab-hex, .alab-note-table, .alab-byte-viz
 *
 * CI gating: CHROMIUM_REQUIRE=1 → HARD FAIL on missing Chromium.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

// Deterministic stub for /api/audio-lab/buckets — two types, two packets each.
const FAKE_BUCKETS = {
  buckets: {
    ADVERT: [
      { raw_hex: 'a1b2c3' + '00112233445566778899aabbccddeeff' + '1020', observation_count: 3 },
      { raw_hex: 'a1b2c4' + 'ff'.repeat(20), observation_count: 1 },
    ],
    GRP_TXT: [
      { raw_hex: 'b1c2d3' + '01020304050607080910111213141516', observation_count: 5 },
    ],
    TXT_MSG: [
      { raw_hex: 'c1d2e3' + 'aa'.repeat(24), observation_count: 2 },
    ],
  },
};

async function main() {
  const requireChromium = process.env.CHROMIUM_REQUIRE === '1';
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      executablePath: process.env.CHROMIUM_PATH || undefined,
      args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
    });
  } catch (err) {
    if (requireChromium) {
      console.error(`test-audio-lab-1297-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-audio-lab-1297-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const fail = (msg) => { failures += 1; console.error(`  FAIL: ${msg}`); };
  const pass = (msg) => { passes += 1; console.log(`  PASS: ${msg}`); };

  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // Stub AudioContext so playback paths don't crash on headless chromium.
  await page.addInitScript(() => {
    window.__audioStub = { oscillators: 0, gainNodes: 0 };
    function makeNode() {
      return {
        connect() { return makeNode(); }, disconnect() {},
        start() {}, stop() {},
        gain: { value: 0, setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {} },
        frequency: { value: 0, setValueAtTime() {}, linearRampToValueAtTime() {} },
        Q: { value: 0 }, pan: { value: 0 },
        threshold: { value: 0 }, knee: { value: 0 }, ratio: { value: 0 },
        attack: { value: 0 }, release: { value: 0 },
        type: 'sine',
      };
    }
    class FakeAudioContext {
      constructor() { this.state = 'running'; this.currentTime = 0; this.destination = makeNode(); }
      createGain() { window.__audioStub.gainNodes += 1; return makeNode(); }
      createOscillator() { window.__audioStub.oscillators += 1; return makeNode(); }
      createBiquadFilter() { return makeNode(); }
      createDynamicsCompressor() { return makeNode(); }
      createStereoPanner() { return makeNode(); }
      createPanner() { return makeNode(); }
      resume() { this.state = 'running'; return Promise.resolve(); }
    }
    window.AudioContext = FakeAudioContext;
    window.webkitAudioContext = FakeAudioContext;
  });

  // Intercept the buckets API with deterministic data.
  await page.route('**/api/audio-lab/buckets', (route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(FAKE_BUCKETS),
    });
  });

  // Visit the live page first so MeshAudio + voice modules are loaded
  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.MeshAudio);

  // Now navigate to audio-lab
  await page.goto(`${BASE}/#/audio-lab`, { waitUntil: 'domcontentloaded' });
  // Wait for sidebar to populate (post-fetch)
  await page.waitForSelector('#alabSidebar .alab-type-hdr', { timeout: 10000 }).catch(() => {});

  // (a) sidebar populated
  const typeCount = await page.evaluate(() => document.querySelectorAll('#alabSidebar .alab-type-hdr').length);
  if (typeCount >= 2) pass(`sidebar shows ${typeCount} type headers`);
  else fail(`sidebar only shows ${typeCount} type headers (expected >=2)`);

  const pktCount = await page.evaluate(() => document.querySelectorAll('#alabSidebar .alab-pkt').length);
  if (pktCount >= 3) pass(`sidebar shows ${pktCount} packet entries`);
  else fail(`sidebar only shows ${pktCount} packets (expected >=3)`);

  // (b) clicking selects a packet
  await page.click('#alabSidebar .alab-pkt');
  const selected = await page.evaluate(() => document.querySelectorAll('#alabSidebar .alab-pkt.selected').length);
  if (selected === 1) pass('exactly one packet selected after click');
  else fail(`selected packet count = ${selected}`);

  // (c) detail populated
  await page.waitForSelector('#alabDetail .alab-hex', { timeout: 5000 }).catch(() => {});
  const detailSummary = await page.evaluate(() => ({
    hex: !!document.querySelector('#alabDetail .alab-hex'),
    notes: document.querySelectorAll('#alabDetail .alab-note-table tr').length,
    bars: document.querySelectorAll('#alabDetail .alab-byte-bar').length,
    mapTable: !!document.querySelector('#alabDetail .alab-map-table'),
  }));
  if (detailSummary.hex && detailSummary.notes >= 2 && detailSummary.bars >= 3) {
    pass(`detail rendered: hex=yes notes=${detailSummary.notes} bars=${detailSummary.bars}`);
  } else {
    fail(`detail incomplete: ${JSON.stringify(detailSummary)}`);
  }

  // (d) type header toggles list visibility
  const firstType = await page.evaluate(() =>
    document.querySelector('#alabSidebar .alab-type-hdr').dataset.type
  );
  await page.click(`#alabSidebar .alab-type-hdr[data-type="${firstType}"]`);
  const hidden = await page.evaluate((t) =>
    document.querySelector(`[data-type-list="${t}"]`).style.display, firstType
  );
  if (hidden === 'none') pass(`type header click hides list (${firstType})`);
  else fail(`type list display = "${hidden}" after click (expected "none")`);

  await page.click(`#alabSidebar .alab-type-hdr[data-type="${firstType}"]`);
  const unhid = await page.evaluate((t) =>
    document.querySelector(`[data-type-list="${t}"]`).style.display, firstType
  );
  if (unhid === '') pass(`second click restores list (${firstType})`);
  else fail(`type list display = "${unhid}" after restore`);

  // (e) BPM slider
  await page.evaluate(() => {
    const s = document.getElementById('alabBPM');
    s.value = '90';
    s.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const bpmText = (await page.textContent('#alabBPMVal')).trim();
  if (bpmText === '90') pass(`alabBPMVal = ${bpmText}`);
  else fail(`alabBPMVal = "${bpmText}" (expected 90)`);

  // (f) Volume slider
  await page.evaluate(() => {
    const s = document.getElementById('alabVol');
    s.value = '40';
    s.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const volText = (await page.textContent('#alabVolVal')).trim();
  // MeshAudio.setVolume() persists to localStorage regardless of whether
  // AudioContext is initialized yet. getVolume() only reflects engine state
  // after initAudio(), so we assert the persisted value as the cross-cut proof.
  const volLs = await page.evaluate(() => parseFloat(localStorage.getItem('live-audio-volume')));
  if (volText === '40%' && Math.abs(volLs - 0.4) < 0.001) pass(`vol slider → text="${volText}" ls=${volLs}`);
  else fail(`vol slider mismatch: text="${volText}" ls=${volLs}`);

  // (g) Speed buttons
  const speedCount = await page.evaluate(() => document.querySelectorAll('.alab-speed').length);
  if (speedCount >= 2) {
    await page.evaluate(() => {
      const btns = document.querySelectorAll('.alab-speed');
      btns[btns.length - 1].click();
    });
    const activeSpeed = await page.evaluate(() => {
      const a = document.querySelectorAll('.alab-speed.active');
      return a.length;
    });
    if (activeSpeed === 1) pass('speed button click → exactly one .alab-speed.active');
    else fail(`speed button click → ${activeSpeed} active`);
  } else {
    fail(`only ${speedCount} speed buttons found`);
  }

  // (h) Loop button
  await page.click('#alabLoop');
  const loopOn = await page.evaluate(() => document.getElementById('alabLoop').classList.contains('active'));
  if (loopOn) pass('loop button toggled active');
  else fail('loop button did not toggle active');
  await page.click('#alabLoop'); // turn it off to stop any timer
  const loopOff = await page.evaluate(() => document.getElementById('alabLoop').classList.contains('active'));
  if (!loopOff) pass('loop button toggled off');
  else fail('loop button did not toggle off');

  // (i) Play button triggers audio
  const beforeOsc = await page.evaluate(() => window.__audioStub.oscillators);
  await page.click('#alabPlay');
  // Give it a tick; voice.play schedules synchronously
  await page.waitForTimeout(150);
  const afterOsc = await page.evaluate(() => window.__audioStub.oscillators);
  if (afterOsc > beforeOsc) pass(`play button triggered oscillators (${beforeOsc} → ${afterOsc})`);
  else fail(`play button did not trigger oscillators (${beforeOsc} → ${afterOsc})`);

  // Click a note-row to exercise playOneNote
  const hasNoteRow = await page.evaluate(() => !!document.querySelector('.alab-note-clickable'));
  if (hasNoteRow) {
    const before2 = await page.evaluate(() => window.__audioStub.oscillators);
    await page.click('.alab-note-clickable');
    await page.waitForTimeout(100);
    const after2 = await page.evaluate(() => window.__audioStub.oscillators);
    if (after2 > before2) pass(`note-row click triggered playOneNote (${before2} → ${after2})`);
    else fail(`note-row click did not trigger osc (${before2} → ${after2})`);
  }

  // (j) destroy via navigation
  await page.goto(`${BASE}/#/`, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(200);
  const cleaned = await page.evaluate(() => ({
    hasSidebar: !!document.getElementById('alabSidebar'),
    hasInjectedStyle: Array.from(document.querySelectorAll('style')).some(s => s.textContent && s.textContent.includes('.alab-sidebar')),
  }));
  if (!cleaned.hasSidebar) pass('destroy(): sidebar removed after navigation');
  else fail('destroy(): sidebar still present after navigation');
  if (!cleaned.hasInjectedStyle) pass('destroy(): style element removed');
  else fail('destroy(): style element still injected');

  await browser.close();

  console.log(`\n${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-audio-lab-1297-e2e.js: ERROR', err);
  process.exit(1);
});
