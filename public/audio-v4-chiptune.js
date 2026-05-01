// Voice v4: "Chiptune" — 8-bit style arpeggios
// Square/sawtooth oscillators, fast staccato notes, bright upper-register scales.
// Type → waveform + scale, bytes → note sequence, hops → brightness, obs → octave jumps.

(function () {
  'use strict';

  const { buildScale, midiToFreq, mapRange, quantizeToScale } = MeshAudio.helpers;

  // Bright scales in a high register
  const SCALES = {
    ADVERT:  buildScale([0, 2, 4, 7, 9],          60),  // C major pentatonic, high
    GRP_TXT: buildScale([0, 4, 7, 11, 14],         55),  // major 7th arpeggio
    TXT_MSG: buildScale([0, 2, 4, 5, 7, 9, 11],    57),  // C major full
    TRACE:   buildScale([0, 3, 6, 9],              57),  // diminished, tense
  };
  const DEFAULT_SCALE = SCALES.ADVERT;

  // TRACE gets sawtooth for extra grit, everything else square
  const WAVE_TYPE = { TRACE: 'sawtooth' };

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount } = parsed;
    const tm = opts.tempoMultiplier;

    const scale   = SCALES[typeName] || DEFAULT_SCALE;
    const oscType = WAVE_TYPE[typeName] || 'square';

    // More notes than constellation — chiptune is dense and fast
    const noteCount = Math.max(4, Math.min(16, Math.ceil(payloadBytes.length / 3)));
    const sampledBytes = [];
    for (let i = 0; i < noteCount; i++) {
      sampledBytes.push(payloadBytes[Math.floor((i / noteCount) * payloadBytes.length)]);
    }

    // Highpass: fewer hops = brighter (sound travels cleanly over short paths)
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'highpass';
    filter.frequency.value = mapRange(Math.min(hopCount, 10), 1, 10, 300, 60);

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = (Math.random() - 0.5) * 0.5;

    filter.connect(panner);
    panner.connect(masterGain);

    const volume = Math.min(0.32, 0.08 + obsCount * 0.012);
    // 5+ observers → accent every 4th note up an octave
    const accentOctave = obsCount >= 5 ? 12 : 0;

    let t = audioCtx.currentTime + 0.02;
    let lastEnd = t;

    for (let i = 0; i < sampledBytes.length; i++) {
      const byte = sampledBytes[i];

      let midiNote = quantizeToScale(byte, scale);
      if (accentOctave && i % 4 === 3) midiNote += accentOctave;

      const osc = audioCtx.createOscillator();
      const env = audioCtx.createGain();

      osc.type = oscType;
      osc.frequency.value = midiToFreq(midiNote);

      // Short, punchy ADSR — no sustain, instant attack defines chiptune feel
      const noteDur = mapRange(byte, 0, 255, 0.03, 0.09) * tm;
      const peakVol = Math.max(volume, 0.0001);
      env.gain.setValueAtTime(0.0001, t);
      env.gain.exponentialRampToValueAtTime(peakVol, t + 0.003);
      env.gain.exponentialRampToValueAtTime(0.0001, t + noteDur);

      osc.connect(env);
      env.connect(filter);
      osc.start(t);
      osc.stop(t + noteDur + 0.02);
      osc.onended = () => { osc.disconnect(); env.disconnect(); };

      t += noteDur + 0.008 * tm;
      lastEnd = t;
    }

    const cleanupMs = (lastEnd - audioCtx.currentTime + 1) * 1000;
    setTimeout(() => { try { filter.disconnect(); panner.disconnect(); } catch (e) {} }, cleanupMs);

    return lastEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('chiptune', { name: 'chiptune', play });
})();
