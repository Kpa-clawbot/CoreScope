// Voice v3: "Drone" — slow ambient pad chords
// 2-4 bytes form a sustained chord cluster. Long attack/release defines the feel.
// Type → modal scale, hops → filter darkness + release length, obs → chord density.

(function () {
  'use strict';

  const { buildScale, midiToFreq, mapRange, quantizeToScale } = MeshAudio.helpers;

  // Dark, pad-friendly modal scales in a low register
  const SCALES = {
    ADVERT:  buildScale([0, 2, 4, 7, 9],       36),  // major pentatonic, bass
    GRP_TXT: buildScale([0, 3, 5, 7, 10],      33),  // minor pentatonic, very low
    TXT_MSG: buildScale([0, 2, 3, 7, 8],       38),  // phrygian dominant, mid-low
    TRACE:   buildScale([0, 2, 6, 8, 10],      35),  // whole-tone fragment, eerie
  };
  const DEFAULT_SCALE = SCALES.ADVERT;

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount } = parsed;

    const scale = SCALES[typeName] || DEFAULT_SCALE;

    // Only 2-4 notes — this is a chord, not a melody
    const noteCount = Math.max(2, Math.min(4, Math.ceil(Math.log2(payloadBytes.length + 1))));
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      sampledBytes.push(payloadBytes[Math.floor((i / noteCount) * payloadBytes.length)]);
    }

    // Hops → filter darkness (more hops = murkier)
    const filterFreq = mapRange(Math.min(hopCount, 10), 1, 10, 1600, 180);
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.value = filterFreq;
    filter.Q.value = 2.0;

    const limiter = audioCtx.createDynamicsCompressor();
    limiter.threshold.value = -8;
    limiter.knee.value = 6;
    limiter.ratio.value = 8;
    limiter.attack.value = 0.01;
    limiter.release.value = 0.15;

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = (Math.random() - 0.5) * 0.4;

    filter.connect(limiter);
    limiter.connect(panner);
    panner.connect(masterGain);

    // Long, evolving envelope — the core of the drone character
    const attack  = mapRange(obsCount, 1, 20, 0.9, 2.8);
    const hold    = 1.2;
    const release = mapRange(Math.min(hopCount, 10), 1, 10, 1.2, 4.5);
    const totalDur = attack + hold + release;

    const volume = Math.min(0.5, 0.08 + obsCount * 0.018);
    // More observers → denser voicing (up to 3 per chord tone)
    const voicesPerNote = Math.min(3, Math.max(1, Math.ceil(Math.log2(obsCount + 1))));
    const voiceVol = Math.max(volume / (sampledBytes.length * voicesPerNote), 0.0001);

    const t = audioCtx.currentTime + 0.02;

    for (const byte of sampledBytes) {
      const baseMidi = quantizeToScale(byte, scale);

      for (let v = 0; v < voicesPerNote; v++) {
        const osc = audioCtx.createOscillator();
        const env = audioCtx.createGain();
        osc.type = 'sine';
        osc.frequency.value = midiToFreq(baseMidi);
        // Subtle detuning for warmth
        osc.detune.value = v === 0 ? 0 : (v % 2 === 0 ? 1 : -1) * (v * 8);

        const peakVol    = Math.max(voiceVol, 0.0001);
        const sustainVol = Math.max(voiceVol * 0.65, 0.0001);

        env.gain.setValueAtTime(0.0001, t);
        env.gain.exponentialRampToValueAtTime(peakVol, t + attack);
        env.gain.exponentialRampToValueAtTime(sustainVol, t + attack + hold * 0.6);
        env.gain.setTargetAtTime(0.0001, t + attack + hold, release / 5);

        osc.connect(env);
        env.connect(filter);
        osc.start(t);
        osc.stop(t + totalDur + 0.5);
        osc.onended = () => { osc.disconnect(); env.disconnect(); };
      }
    }

    const cleanupMs = (totalDur + 2) * 1000;
    setTimeout(() => {
      try { filter.disconnect(); limiter.disconnect(); panner.disconnect(); } catch (e) {}
    }, cleanupMs);

    return totalDur;
  }

  MeshAudio.registerVoice('drone', { name: 'drone', play });
})();
