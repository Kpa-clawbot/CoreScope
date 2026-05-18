// Voice v7: "NGGYU" — Never Gonna Give You Up, 8-bit chiptune
// Plays the chorus melody note-by-note as packets arrive.
// Square-wave oscillator, staccato, cursor advances through the full phrase cycle.

(function () {
  'use strict';

  const { midiToFreq, mapRange } = MeshAudio.helpers;

  // Chorus melody: A major (A4=69 B4=71 C#5=73 D5=74 E5=76 F#5=78)
  // 0 = rest
  const MELODY = [
    // "Never gonna give you up"
    69, 71, 74, 71, 78, 78, 76,
    // "Never gonna let you down"
    69, 71, 74, 71, 76, 74,
    // "Never gonna run around and desert you"
    69, 71, 74, 71, 74, 73, 71, 69, 0, 0,
    // "Never gonna make you cry"
    69, 71, 74, 71, 78, 76,
    // "Never gonna say goodbye"
    69, 71, 74, 71, 76, 74,
    // "Never gonna tell a lie and hurt you"
    69, 71, 74, 71, 73, 71, 78, 76,
  ];

  // Duration multiplier per note: 1 = base, 2 = held
  const DURS = [
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
