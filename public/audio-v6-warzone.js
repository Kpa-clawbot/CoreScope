// Voice v6: "Warzone" — synthesized gunshots
// Noise crack + sine bass thump per shot. Type → gun character,
// hops → reverb tail length, obs → burst count (SMG fires more rounds).

(function () {
  'use strict';

  const { mapRange } = MeshAudio.helpers;

  function makeNoiseBuffer(audioCtx, dur) {
    const n = Math.ceil(audioCtx.sampleRate * dur);
    const buf = audioCtx.createBuffer(1, n, audioCtx.sampleRate);
    const d = buf.getChannelData(0);
    for (let i = 0; i < n; i++) d[i] = Math.random() * 2 - 1;
    return buf;
  }

  // crack: bandpass noise transient. thump: sine pitch-drop.
  function fireGunshot(audioCtx, dest, t, cfg, accent) {
    const { crackDur, crackFreq, crackQ, thumpFreq, thumpDur, crackVol, thumpVol } = cfg;

    // Crack
    const noise = audioCtx.createBufferSource();
    noise.buffer = makeNoiseBuffer(audioCtx, crackDur + 0.05);
    const crackFilter = audioCtx.createBiquadFilter();
    crackFilter.type = 'bandpass';
    crackFilter.frequency.value = crackFreq;
    crackFilter.Q.value = crackQ;
    const crackEnv = audioCtx.createGain();
    crackEnv.gain.setValueAtTime(0.0001, t);
    crackEnv.gain.exponentialRampToValueAtTime(accent * crackVol, t + 0.002);
    crackEnv.gain.exponentialRampToValueAtTime(0.0001, t + crackDur);
    noise.connect(crackFilter); crackFilter.connect(crackEnv); crackEnv.connect(dest);
    noise.start(t); noise.stop(t + crackDur + 0.05);
    noise.onended = () => { noise.disconnect(); crackFilter.disconnect(); crackEnv.disconnect(); };

    // Bass thump
    const osc = audioCtx.createOscillator();
    const thumpEnv = audioCtx.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(thumpFreq, t);
    osc.frequency.exponentialRampToValueAtTime(Math.max(thumpFreq * 0.22, 20), t + thumpDur);
    thumpEnv.gain.setValueAtTime(0.0001, t);
    thumpEnv.gain.exponentialRampToValueAtTime(accent * thumpVol, t + 0.004);
    thumpEnv.gain.exponentialRampToValueAtTime(0.0001, t + thumpDur);
    osc.connect(thumpEnv); thumpEnv.connect(dest);
    osc.start(t); osc.stop(t + thumpDur + 0.05);
    osc.onended = () => { osc.disconnect(); thumpEnv.disconnect(); };

    return Math.max(crackDur, thumpDur);
  }

  // Gun character per packet type
  const GUN_CFG = {
    ADVERT:  { crackDur: 0.13, crackFreq: 2500, crackQ: 0.7, thumpFreq: 120, thumpDur: 0.16, crackVol: 0.55, thumpVol: 0.45 }, // pistol
    GRP_TXT: { crackDur: 0.22, crackFreq: 1400, crackQ: 0.5, thumpFreq: 75,  thumpDur: 0.30, crackVol: 0.65, thumpVol: 0.65 }, // shotgun
    TXT_MSG: { crackDur: 0.07, crackFreq: 3500, crackQ: 0.9, thumpFreq: 160, thumpDur: 0.09, crackVol: 0.45, thumpVol: 0.30 }, // SMG
    TRACE:   { crackDur: 0.28, crackFreq: 5000, crackQ: 0.4, thumpFreq: 55,  thumpDur: 0.08, crackVol: 0.70, thumpVol: 0.20 }, // sniper
  };
  const DEFAULT_CFG = GUN_CFG.ADVERT;

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount } = parsed;
    const tm = opts.tempoMultiplier;

    const cfg = GUN_CFG[typeName] || DEFAULT_CFG;

    // Hops → reverb delay length (more hops = sound bouncing around a larger space)
    const revTime = mapRange(Math.min(hopCount, 10), 1, 10, 0.08, 0.55);
    const revDelay = audioCtx.createDelay(1.0);
    revDelay.delayTime.value = revTime;
    const revGain = audioCtx.createGain();
    revGain.gain.value = 0.10;
    revDelay.connect(revGain);
    revGain.connect(masterGain);

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = (Math.random() - 0.5) * 0.7;
    panner.connect(masterGain); // dry shot
    panner.connect(revDelay);   // reverb tail

    const accent = Math.min(0.68, 0.20 + (obsCount - 1) * 0.024);

    // SMG fires a burst; pistol/shotgun/sniper fire 1-2 rounds
    const shotCount = typeName === 'TXT_MSG'
      ? Math.max(2, Math.min(6, obsCount))
      : Math.max(1, Math.min(2, Math.ceil(Math.log2(obsCount + 1))));

    const sampledBytes = [];
    for (let i = 0; i < shotCount; i++) {
      sampledBytes.push(payloadBytes[Math.floor((i / shotCount) * payloadBytes.length)]);
    }

    let t = audioCtx.currentTime + 0.02;
    let lastEnd = t;
    const step = cfg.crackDur * 0.9 * tm + 0.03 * tm;

    for (const byte of sampledBytes) {
      const shotAccent = accent * mapRange(byte, 0, 255, 0.6, 1.0);
      const dur = fireGunshot(audioCtx, panner, t, cfg, shotAccent);
      t += step;
      lastEnd = Math.max(lastEnd, t + dur);
    }

    const cleanupMs = (lastEnd - audioCtx.currentTime + revTime * 4 + 1) * 1000;
    setTimeout(() => {
      try { revGain.disconnect(); revDelay.disconnect(); panner.disconnect(); } catch (e) {}
    }, cleanupMs);

    return lastEnd - audioCtx.currentTime + revTime;
  }

  MeshAudio.registerVoice('warzone', { name: 'warzone', play });
})();
