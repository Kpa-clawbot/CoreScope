// Voice v2: "Pulse" — percussive packet sonification
// Type → drum voice (kick/snare/hat/rim), bytes → rhythm pattern,
// hops → decay length, obs → accent volume.

(function () {
  'use strict';

  const { mapRange } = MeshAudio.helpers;

  function makeNoiseBuffer(audioCtx, durationSec) {
    const frameCount = Math.ceil(audioCtx.sampleRate * durationSec);
    const buffer = audioCtx.createBuffer(1, frameCount, audioCtx.sampleRate);
    const data = buffer.getChannelData(0);
    for (let i = 0; i < frameCount; i++) data[i] = Math.random() * 2 - 1;
    return buffer;
  }

  // Kick: sine with rapid pitch drop (classic synthesis)
  function playKick(audioCtx, dest, t, accent, decay) {
    const osc = audioCtx.createOscillator();
    const env = audioCtx.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(160, t);
    osc.frequency.exponentialRampToValueAtTime(40, t + decay * 0.5);
    env.gain.setValueAtTime(0.0001, t);
    env.gain.exponentialRampToValueAtTime(accent, t + 0.005);
    env.gain.exponentialRampToValueAtTime(0.0001, t + decay);
    osc.connect(env); env.connect(dest);
    osc.start(t); osc.stop(t + decay + 0.05);
    osc.onended = () => { osc.disconnect(); env.disconnect(); };
  }

  // Snare: bandpass noise + short tone transient
  function playSnare(audioCtx, dest, t, accent, decay) {
    const nb = makeNoiseBuffer(audioCtx, decay + 0.1);
    const noise = audioCtx.createBufferSource();
    noise.buffer = nb;
    const noiseFilter = audioCtx.createBiquadFilter();
    noiseFilter.type = 'bandpass';
    noiseFilter.frequency.value = 2200;
    noiseFilter.Q.value = 0.8;
    const noiseEnv = audioCtx.createGain();
    noiseEnv.gain.setValueAtTime(0.0001, t);
    noiseEnv.gain.exponentialRampToValueAtTime(accent * 0.7, t + 0.003);
    noiseEnv.gain.exponentialRampToValueAtTime(0.0001, t + decay * 0.6);
    noise.connect(noiseFilter); noiseFilter.connect(noiseEnv); noiseEnv.connect(dest);
    noise.start(t); noise.stop(t + decay + 0.1);
    noise.onended = () => { noise.disconnect(); noiseFilter.disconnect(); noiseEnv.disconnect(); };

    const osc = audioCtx.createOscillator();
    const oscEnv = audioCtx.createGain();
    osc.type = 'triangle';
    osc.frequency.setValueAtTime(200, t);
    osc.frequency.exponentialRampToValueAtTime(110, t + decay * 0.25);
    oscEnv.gain.setValueAtTime(0.0001, t);
    oscEnv.gain.exponentialRampToValueAtTime(accent * 0.4, t + 0.003);
    oscEnv.gain.exponentialRampToValueAtTime(0.0001, t + decay * 0.35);
    osc.connect(oscEnv); oscEnv.connect(dest);
    osc.start(t); osc.stop(t + decay + 0.05);
    osc.onended = () => { osc.disconnect(); oscEnv.disconnect(); };
  }

  // Hi-hat: highpass noise, very short
  function playHat(audioCtx, dest, t, accent, decay) {
    const hatDecay = decay * 0.4;
    const nb = makeNoiseBuffer(audioCtx, hatDecay + 0.05);
    const noise = audioCtx.createBufferSource();
    noise.buffer = nb;
    const filter = audioCtx.createBiquadFilter();
    filter.type = 'highpass';
    filter.frequency.value = 9000;
    const env = audioCtx.createGain();
    env.gain.setValueAtTime(0.0001, t);
    env.gain.exponentialRampToValueAtTime(accent * 0.45, t + 0.002);
    env.gain.exponentialRampToValueAtTime(0.0001, t + hatDecay);
    noise.connect(filter); filter.connect(env); env.connect(dest);
    noise.start(t); noise.stop(t + hatDecay + 0.05);
    noise.onended = () => { noise.disconnect(); filter.disconnect(); env.disconnect(); };
  }

  // Rim: short square click
  function playRim(audioCtx, dest, t, accent, decay) {
    const rimDecay = decay * 0.3;
    const osc = audioCtx.createOscillator();
    const env = audioCtx.createGain();
    osc.type = 'square';
    osc.frequency.value = 900;
    env.gain.setValueAtTime(0.0001, t);
    env.gain.exponentialRampToValueAtTime(accent * 0.3, t + 0.001);
    env.gain.exponentialRampToValueAtTime(0.0001, t + rimDecay);
    osc.connect(env); env.connect(dest);
    osc.start(t); osc.stop(t + rimDecay + 0.02);
    osc.onended = () => { osc.disconnect(); env.disconnect(); };
  }

  const DRUM = {
    ADVERT: playKick,
    GRP_TXT: playSnare,
    TXT_MSG: playHat,
    TRACE:   playRim,
  };

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount } = parsed;
    const tm = opts.tempoMultiplier;

    const drumFn = DRUM[typeName] || playKick;
    // More hops → longer, roomier decay
    const decay = mapRange(Math.min(hopCount, 10), 1, 10, 0.07, 0.32) * tm;
    const accent = Math.min(0.65, 0.15 + (obsCount - 1) * 0.025);

    // Sample bytes as hits — sqrt(len) steps, same as constellation
    const hitCount = Math.max(1, Math.min(8, Math.ceil(Math.sqrt(payloadBytes.length))));
    const sampledBytes = [];
    for (let i = 0; i < hitCount; i++) {
      sampledBytes.push(payloadBytes[Math.floor((i / hitCount) * payloadBytes.length)]);
    }

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = 0; // percussion stays centred
    panner.connect(masterGain);

    let t = audioCtx.currentTime + 0.02;
    let lastEnd = t;

    for (let i = 0; i < sampledBytes.length; i++) {
      const byte = sampledBytes[i];
      // ~20% of non-first steps become rests for groove
      if (i > 0 && byte < 51) {
        t += mapRange(byte, 0, 50, 0.04, 0.14) * tm;
        continue;
      }
      const hitAccent = accent * mapRange(byte, 0, 255, 0.45, 1.0);
      drumFn(audioCtx, panner, t, hitAccent, decay);
      t += decay + mapRange(byte, 0, 255, 0.02, 0.1) * tm;
      lastEnd = t;
    }

    const cleanupMs = (lastEnd - audioCtx.currentTime + 1) * 1000;
    setTimeout(() => { try { panner.disconnect(); } catch (e) {} }, cleanupMs);

    return lastEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('pulse', { name: 'pulse', play });
})();
