#!/usr/bin/env node
/* Issue #1297 — B1 audio batch coverage.
 *
 * Exercises public/audio.js (MeshAudio engine) and
 * public/audio-v1-constellation.js (voice module) via the /#/live page.
 *
 * Asserts:
 *   (a) MeshAudio global API surface is present
 *   (b) constellation voice is registered (window._meshAudioVoices)
 *   (c) #liveAudioToggle exists and #audioControls toggles visibility
 *   (d) BPM slider changes #audioBpmVal text + MeshAudio.getBPM()
 *   (e) Volume slider changes #audioVolVal text + MeshAudio.getVolume()
 *   (f) Voice select is populated with at least the constellation voice
 *   (g) MeshAudio helpers (buildScale, midiToFreq, mapRange, quantizeToScale)
 *       produce expected values
 *   (h) sonifyPacket() with a synthetic packet does not throw and exercises
 *       parsePacketBytes/voice.play paths (mocked AudioContext)
 *   (i) localStorage persistence for live-audio-enabled / bpm / volume
 *
 * Stable selectors: #liveAudioToggle, #audioControls, #audioBpmSlider,
 * #audioBpmVal, #audioVolSlider, #audioVolVal, #audioVoiceSelect.
 *
 * CI gating: when CHROMIUM_REQUIRE=1 a missing/broken Chromium is HARD FAIL.
 */
'use strict';

const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';

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
      console.error(`test-audio-live-1297-e2e.js: FAIL — Chromium required but unavailable: ${err.message}`);
      process.exit(1);
    }
    console.log(`test-audio-live-1297-e2e.js: SKIP (Chromium unavailable: ${err.message.split('\n')[0]})`);
    process.exit(0);
  }

  let failures = 0;
  let passes = 0;
  const fail = (msg) => { failures += 1; console.error(`  FAIL: ${msg}`); };
  const pass = (msg) => { passes += 1; console.log(`  PASS: ${msg}`); };

  const ctx = await browser.newContext();
  // #1532 — controls panel defaults collapsed; pre-seed expanded pref.
  await ctx.addInitScript(() => {
    try { localStorage.setItem('live-controls-expanded', 'true'); } catch (_) {}
  });
  const page = await ctx.newPage();
  page.setDefaultTimeout(15000);

  // Stub AudioContext BEFORE app boots so sonifyPacket can run headlessly
  // without real audio hardware. Capture invocations on window.__audioStub.
  await page.addInitScript(() => {
    window.__audioStub = { gainNodes: 0, oscillators: 0, contexts: 0 };
    function makeNode() {
      return {
        connect() { return makeNode(); },
        disconnect() {},
        start() {},
        stop() {},
        gain: { value: 0, setValueAtTime() {}, linearRampToValueAtTime() {}, exponentialRampToValueAtTime() {} },
        frequency: { value: 0, setValueAtTime() {}, linearRampToValueAtTime() {} },
        Q: { value: 0 },
        pan: { value: 0 },
        threshold: { value: 0 },
        knee: { value: 0 },
        ratio: { value: 0 },
        attack: { value: 0 },
        release: { value: 0 },
        type: 'sine',
      };
    }
    class FakeAudioContext {
      constructor() {
        window.__audioStub.contexts += 1;
        this.state = 'running';
        this.currentTime = 0;
        this.destination = makeNode();
      }
      createGain() { window.__audioStub.gainNodes += 1; return makeNode(); }
      createOscillator() { window.__audioStub.oscillators += 1; return makeNode(); }
      createBiquadFilter() { return makeNode(); }
      createDynamicsCompressor() { return makeNode(); }
      createStereoPanner() { return makeNode(); }
      createPanner() { return makeNode(); }
      resume() { this.state = 'running'; return Promise.resolve(); }
      suspend() { this.state = 'suspended'; return Promise.resolve(); }
    }
    window.AudioContext = FakeAudioContext;
    window.webkitAudioContext = FakeAudioContext;
  });

  await page.goto(`${BASE}/#/live`, { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => window.MeshAudio && document.getElementById('liveAudioToggle'));

  // (a) MeshAudio API surface
  const apiKeys = await page.evaluate(() => Object.keys(window.MeshAudio || {}).sort());
  const expectedKeys = ['getBPM', 'getContext', 'getVoiceName', 'getVoiceNames',
    'helpers', 'isEnabled', 'registerVoice', 'restore', 'setBPM', 'setEnabled',
    'setVoice', 'setVolume', 'getVolume', 'sonifyPacket'].sort();
  const missing = expectedKeys.filter(k => !apiKeys.includes(k));
  if (missing.length === 0) pass(`MeshAudio API has all expected methods (${apiKeys.length} keys)`);
  else fail(`MeshAudio missing methods: ${missing.join(', ')}`);

  // (b) constellation voice registered
  const voiceNames = await page.evaluate(() => Object.keys(window._meshAudioVoices || {}));
  if (voiceNames.includes('constellation')) pass('constellation voice registered');
  else fail(`constellation voice not registered (have: ${voiceNames.join(', ') || 'none'})`);

  // (c) Toggle reveals audio controls
  const initiallyHidden = await page.evaluate(() => document.getElementById('audioControls').classList.contains('hidden'));
  if (initiallyHidden) pass('audioControls hidden by default');
  else fail('audioControls should be hidden by default');

  await page.evaluate(() => {
    const t = document.getElementById('liveAudioToggle');
    t.checked = true;
    t.dispatchEvent(new Event('change', { bubbles: true }));
  });
  const shown = await page.evaluate(() => !document.getElementById('audioControls').classList.contains('hidden'));
  if (shown) pass('audioControls revealed after toggle on');
  else fail('audioControls did not unhide after toggle');

  const engineEnabled = await page.evaluate(() => window.MeshAudio.isEnabled());
  if (engineEnabled) pass('MeshAudio.isEnabled() true after toggle');
  else fail('MeshAudio.isEnabled() false after toggle');

  // (d) BPM slider
  await page.evaluate(() => {
    const s = document.getElementById('audioBpmSlider');
    s.value = '180';
    s.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const bpmText = await page.textContent('#audioBpmVal');
  const bpmEngine = await page.evaluate(() => window.MeshAudio.getBPM());
  if (bpmText.trim() === '180' && bpmEngine === 180) pass(`BPM slider → text=${bpmText} engine=${bpmEngine}`);
  else fail(`BPM slider mismatch: text=${bpmText} engine=${bpmEngine}`);

  // (e) Volume slider
  await page.evaluate(() => {
    const s = document.getElementById('audioVolSlider');
    s.value = '55';
    s.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const volText = await page.textContent('#audioVolVal');
  const volEngine = await page.evaluate(() => Math.round(window.MeshAudio.getVolume() * 100));
  if (volText.trim() === '55' && volEngine === 55) pass(`Volume slider → text=${volText} engine=${volEngine}`);
  else fail(`Volume slider mismatch: text=${volText} engine=${volEngine}`);

  // (f) Voice select populated
  const voiceOptionCount = await page.evaluate(() =>
    document.querySelectorAll('#audioVoiceSelect option').length
  );
  if (voiceOptionCount >= 1) pass(`voice select has ${voiceOptionCount} option(s)`);
  else fail('voice select empty');

  // (g) Helpers
  const helperResults = await page.evaluate(() => {
    const h = window.MeshAudio.helpers;
    return {
      scale: h.buildScale([0, 4, 7], 60), // major triad-ish, 3 octaves * 3 notes = 9 notes
      freq60: h.midiToFreq(60),           // middle C ~ 261.625
      freq69: h.midiToFreq(69),           // A4 = 440
      map: h.mapRange(5, 0, 10, 0, 100),  // 50
      quant: h.quantizeToScale(128, [0, 1, 2, 3]), // ~2
    };
  });
  if (helperResults.scale.length === 9) pass(`buildScale → 9 notes`);
  else fail(`buildScale length=${helperResults.scale.length} (want 9)`);
  if (Math.abs(helperResults.freq69 - 440) < 0.01) pass('midiToFreq(69) ≈ 440Hz');
  else fail(`midiToFreq(69) = ${helperResults.freq69}`);
  if (Math.abs(helperResults.map - 50) < 0.01) pass('mapRange linear OK');
  else fail(`mapRange = ${helperResults.map}`);
  if (helperResults.quant === 2) pass('quantizeToScale OK');
  else fail(`quantizeToScale = ${helperResults.quant}`);

  // (h) sonifyPacket with synthetic packet — exercises parsePacketBytes
  //     + voice.play. Headers + ~20 payload bytes.
  const sonifyResult = await page.evaluate(() => {
    const beforeOsc = window.__audioStub.oscillators;
    const beforeGain = window.__audioStub.gainNodes;
    let threw = null;
    try {
      const pkt = {
        raw: 'a1b2c3' + '00112233445566778899aabbccddeeff' + '1020',
        observation_count: 3,
        decoded: {
          header: { payloadTypeName: 'ADVERT' },
          payload: { lat: 47.6, lon: -122.3 },
          path: { hops: [{ hop: 'aa' }, { hop: 'bb' }] },
        },
      };
      window.MeshAudio.sonifyPacket(pkt);
    } catch (e) { threw = e.message; }
    return {
      threw,
      deltaOsc: window.__audioStub.oscillators - beforeOsc,
      deltaGain: window.__audioStub.gainNodes - beforeGain,
    };
  });
  if (!sonifyResult.threw && sonifyResult.deltaOsc > 0) {
    pass(`sonifyPacket exercised voice (oscillators +${sonifyResult.deltaOsc}, gains +${sonifyResult.deltaGain})`);
  } else {
    fail(`sonifyPacket: threw=${sonifyResult.threw} oscΔ=${sonifyResult.deltaOsc}`);
  }

  // Try a second packet with a different type to cover more SCALES/SYNTHS branches
  const sonify2 = await page.evaluate(() => {
    let threw = null;
    try {
      ['GRP_TXT', 'TXT_MSG', 'TRACE', 'UNKNOWN'].forEach(t => {
        window.MeshAudio.sonifyPacket({
          raw: '010203' + 'ff'.repeat(16),
          observation_count: 1,
          decoded: { header: { payloadTypeName: t }, payload: {}, path: { hops: [] } },
        });
      });
    } catch (e) { threw = e.message; }
    return threw;
  });
  if (!sonify2) pass('sonifyPacket multi-type OK');
  else fail(`sonifyPacket multi-type threw: ${sonify2}`);

  // (i) localStorage persistence
  const ls = await page.evaluate(() => ({
    enabled: localStorage.getItem('live-audio-enabled'),
    bpm: localStorage.getItem('live-audio-bpm'),
    vol: localStorage.getItem('live-audio-volume'),
  }));
  if (ls.enabled === 'true' && ls.bpm === '180' && parseFloat(ls.vol) > 0.5) {
    pass(`localStorage persisted: enabled=${ls.enabled} bpm=${ls.bpm} vol=${ls.vol}`);
  } else {
    fail(`localStorage persistence: ${JSON.stringify(ls)}`);
  }

  // Toggle OFF should re-hide
  await page.evaluate(() => {
    const t = document.getElementById('liveAudioToggle');
    t.checked = false;
    t.dispatchEvent(new Event('change', { bubbles: true }));
  });
  const reHidden = await page.evaluate(() => document.getElementById('audioControls').classList.contains('hidden'));
  if (reHidden) pass('audioControls re-hides after toggle off');
  else fail('audioControls did not re-hide after toggle off');

  await browser.close();

  console.log(`\n${passes} passed, ${failures} failed`);
  process.exit(failures > 0 ? 1 : 0);
}

main().catch((err) => {
  console.error('test-audio-live-1297-e2e.js: ERROR', err);
  process.exit(1);
});
