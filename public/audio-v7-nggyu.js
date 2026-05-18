// Voice v7: "NGGYU" — Never Gonna Give You Up, 8-bit chiptune
// Plays the chorus melody note-by-note as packets arrive.
// Square-wave oscillator, staccato, cursor advances through the full phrase cycle.

(function () {
  'use strict';

  const { midiToFreq, mapRange } = MeshAudio.helpers;

  // A major: A4=69 B4=71 C#5=73 D5=74 E5=76 F#5=78
  // B major (+2 semitones): B4=71 C#5=73 D#5=75 E5=76 F#5=78 G#5=80
  // 0 = rest
  const MELODY = [
    // --- Chorus 1 (A major) ---
    69, 71, 74, 71, 78, 78, 76,
    69, 71, 74, 71, 76, 74,
    69, 71, 74, 71, 74, 73, 71, 69, 0, 0,
    69, 71, 74, 71, 78, 76,
    69, 71, 74, 71, 76, 74,
    69, 71, 74, 71, 73, 71, 78, 76,
    // --- key-change pickup ---
    0, 0, 71, 73,
    // --- Chorus 2 (+2 semitones, B major) ---
    71, 73, 76, 73, 80, 80, 78,
    71, 73, 76, 73, 78, 76,
    71, 73, 76, 73, 76, 75, 73, 71, 0, 0,
    71, 73, 76, 73, 80, 78,
    71, 73, 76, 73, 78, 76,
    71, 73, 76, 73, 75, 73, 80, 78,
  ];

  // Duration multiplier per note: 1 = base, 2 = held
  const DURS = [
    // Chorus 1
    1, 1, 1, 1, 1.5, 1, 2,
    1, 1, 1, 1, 1.5, 2,
    1, 1, 1, 1, 1,   1, 1, 1, 1, 2,
    1, 1, 1, 1, 1,   2,
    1, 1, 1, 1, 1,   2,
    1, 1, 1, 1, 1,   1, 1, 2,
    // pickup
    1, 1, 1, 1,
    // Chorus 2 (same rhythm)
    1, 1, 1, 1, 1.5, 1, 2,
    1, 1, 1, 1, 1.5, 2,
    1, 1, 1, 1, 1,   1, 1, 1, 1, 2,
    1, 1, 1, 1, 1,   2,
    1, 1, 1, 1, 1,   2,
    1, 1, 1, 1, 1,   1, 1, 2,
  ];

  let cursor = 0;

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, hopCount, obsCount } = parsed;
    const tm = opts.tempoMultiplier;

    // More bytes → more notes per packet (2–7)
    const count = Math.max(2, Math.min(7, Math.ceil(payloadBytes.length / 5)));

    // 8-bit: highpass keeps it crisp; fewer hops = brighter
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'highpass';
    filter.frequency.value = mapRange(Math.min(hopCount, 10), 1, 10, 3500, 600);

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = (Math.random() - 0.5) * 0.35;

    filter.connect(panner);
    panner.connect(masterGain);

    const baseVol = Math.min(0.38, 0.14 + obsCount * 0.014);
    const baseDur = 0.09 * tm;
    const gap     = 0.010 * tm;

    let t = audioCtx.currentTime + 0.02;
    let lastEnd = t;

    for (let i = 0; i < count; i++) {
      const idx  = cursor % MELODY.length;
      const midi = MELODY[idx];
      const dur  = baseDur * (DURS[idx] || 1);
      cursor = (cursor + 1) % MELODY.length;

      t += (midi === 0) ? dur + gap : 0;

      if (midi === 0) {
        lastEnd = t;
        continue;
      }

      const osc = audioCtx.createOscillator();
      const env = audioCtx.createGain();

      osc.type = 'square';
      osc.frequency.value = midiToFreq(midi);

      env.gain.setValueAtTime(0.0001, t);
      env.gain.exponentialRampToValueAtTime(baseVol, t + 0.003);
      env.gain.exponentialRampToValueAtTime(0.0001, t + dur * 0.9);

      osc.connect(env);
      env.connect(filter);
      osc.start(t);
      osc.stop(t + dur + 0.02);
      osc.onended = () => { osc.disconnect(); env.disconnect(); };

      t += dur + gap;
      lastEnd = t;
    }

    const cleanupMs = (lastEnd - audioCtx.currentTime + 1) * 1000;
    setTimeout(() => { try { filter.disconnect(); panner.disconnect(); } catch (e) {} }, cleanupMs);

    return lastEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('NGGYU', { name: 'NGGYU', play });
})();
