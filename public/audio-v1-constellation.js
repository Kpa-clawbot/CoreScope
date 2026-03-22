// Voice v1: "Constellation" — melodic packet sonification
// Original voice: type-based instruments, scale-quantized melody from payload bytes,
// byte-driven note duration and spacing, hop-based filter, observation chord voicing.

(function () {
  'use strict';

  const { buildScale, midiToFreq, mapRange, quantizeToScale } = MeshAudio.helpers;

  // Scales per payload type
  const SCALES = {
    ADVERT: buildScale([0, 2, 4, 7, 9], 48),       // C major pentatonic
    GRP_TXT: buildScale([0, 3, 5, 7, 10], 45),      // A minor pentatonic
    TXT_MSG: buildScale([0, 2, 3, 5, 7, 8, 10], 40),// E natural minor
    TRACE: buildScale([0, 2, 4, 6, 8, 10], 50),      // D whole tone
  };
  const DEFAULT_SCALE = SCALES.ADVERT;

  // Synth ADSR envelopes per type
  const SYNTHS = {
    ADVERT: { type: 'triangle', attack: 0.02, decay: 0.3, sustain: 0.4, release: 0.5 },
    GRP_TXT: { type: 'sine', attack: 0.005, decay: 0.15, sustain: 0.1, release: 0.2 },
    TXT_MSG: { type: 'triangle', attack: 0.01, decay: 0.2, sustain: 0.3, release: 0.4 },
    TRACE: { type: 'sine', attack: 0.05, decay: 0.4, sustain: 0.5, release: 0.8 },
  };
  const DEFAULT_SYNTH = SYNTHS.ADVERT;

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount, payload, hops } = parsed;
    const tm = opts.tempoMultiplier;

    const scale = SCALES[typeName] || DEFAULT_SCALE;
    const synthConfig = SYNTHS[typeName] || DEFAULT_SYNTH;

    // Sample sqrt(len) bytes evenly
    const noteCount = Math.max(2, Math.min(10, Math.ceil(Math.sqrt(payloadBytes.length))));
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      const idx = Math.floor((i / noteCount) * payloadBytes.length);
      sampledBytes.push(payloadBytes[idx]);
    }

    // Pan from longitude
    let panValue = 0;
    if (payload.lat !== undefined && payload.lon !== undefined) {
      panValue = Math.max(-1, Math.min(1, mapRange(payload.lon, -125, -65, -1, 1)));
    } else if (hops.length > 0) {
      panValue = (Math.random() - 0.5) * 0.6;
    }

    // Filter from hops
    const filterFreq = mapRange(Math.min(hopCount, 10), 1, 10, 8000, 800);

    // Volume from observations
    const volume = Math.min(0.6, 0.15 + (obsCount - 1) * 0.02);
    // More observers = richer chord: 1→1, 3→2, 8→3, 15→4, 30→5, 60→6
    const voiceCount = Math.min(Math.max(1, Math.ceil(Math.log2(obsCount + 1))), 8);

    // Audio chain: filter → limiter → panner → master
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.value = filterFreq;
    filter.Q.value = 1;

    const limiter = audioCtx.createDynamicsCompressor();
    limiter.threshold.value = -6;
    limiter.knee.value = 6;
    limiter.ratio.value = 12;
    limiter.attack.value = 0.001;
    limiter.release.value = 0.05;

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = panValue;

    filter.connect(limiter);
    limiter.connect(panner);
    panner.connect(masterGain);

    let timeOffset = audioCtx.currentTime + 0.02; // small lookahead avoids scheduling on "now"
    let lastNoteEnd = timeOffset;

    for (let i = 0; i < sampledBytes.length; i++) {
      const byte = sampledBytes[i];
      const freq = midiToFreq(quantizeToScale(byte, scale));
      const duration = mapRange(byte, 0, 255, 0.05, 0.4) * tm;

      let gap = 0.05 * tm;
      if (i < sampledBytes.length - 1) {
        const delta = Math.abs(sampledBytes[i + 1] - byte);
        gap = mapRange(delta, 0, 255, 0.03, 0.3) * tm;
      }

      const noteStart = timeOffset;
      const noteEnd = noteStart + duration;
      const { attack: a, decay: d, sustain: s, release: r } = synthConfig;

      for (let v = 0; v < voiceCount; v++) {
        const detune = v === 0 ? 0 : (v % 2 === 0 ? 1 : -1) * (v * 5 + 3);
        const osc = audioCtx.createOscillator();
        const envGain = audioCtx.createGain();

        osc.type = synthConfig.type;
        osc.frequency.value = freq;
        osc.detune.value = detune;

        const voiceVol = volume / voiceCount;
        const sustainVol = Math.max(voiceVol * s, 0.0001);

        // Envelope: start silent, ramp up, decay to sustain, hold, release to silence
        // Use exponentialRamp throughout to avoid discontinuities
        envGain.gain.setValueAtTime(0.0001, noteStart);
        envGain.gain.exponentialRampToValueAtTime(Math.max(voiceVol, 0.0001), noteStart + a);
        envGain.gain.exponentialRampToValueAtTime(sustainVol, noteStart + a + d);
        // Hold sustain — cancelAndHoldAtTime not universal, so just let it ride
        // Release: ramp down from wherever we are
        envGain.gain.setTargetAtTime(0.0001, noteEnd, r / 5); // smooth exponential decay

        osc.connect(envGain);
        envGain.connect(filter);
        osc.start(noteStart);
        osc.stop(noteEnd + r + 0.1);
        osc.onended = () => { osc.disconnect(); envGain.disconnect(); };
      }

      timeOffset = noteEnd + gap;
      lastNoteEnd = noteEnd + (synthConfig.release || 0.2);
    }

    // Cleanup shared nodes
    const cleanupMs = (lastNoteEnd - audioCtx.currentTime + 1) * 1000;
    setTimeout(() => {
      try { filter.disconnect(); limiter.disconnect(); panner.disconnect(); } catch (e) {}
    }, cleanupMs);

    return lastNoteEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('constellation', { name: 'constellation', play });
})();
