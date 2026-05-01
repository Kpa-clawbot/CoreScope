// Voice v5: "Blaster" — Star Wars style laser shots
// Sawtooth/square wave with rapid downward frequency sweep + short delay echo.
// Type → shot character (pistol/rifle/rapid/cannon), bytes → pitch+duration,
// hops → sweep speed, obs → burst count.

(function () {
  'use strict';

  const { mapRange } = MeshAudio.helpers;

  // Per-type shot character
  const TYPE_CFG = {
    ADVERT:  { startRange: [700,  1500], endRange: [65,  160], durRange: [0.20, 0.30], wave: 'sawtooth', echo: 0.09 },
    GRP_TXT: { startRange: [1200, 2500], endRange: [140, 280], durRange: [0.10, 0.18], wave: 'square',   echo: 0.06 },
    TXT_MSG: { startRange: [1800, 3200], endRange: [200, 420], durRange: [0.07, 0.13], wave: 'sawtooth', echo: 0.04 },
    TRACE:   { startRange: [180,  500],  endRange: [22,  65],  durRange: [0.35, 0.55], wave: 'sawtooth', echo: 0.14 },
  };
  const DEFAULT_CFG = TYPE_CFG.ADVERT;

  function fireShot(audioCtx, dest, t, cfg, byte, volume) {
    const startFreq = mapRange(byte, 0, 255, cfg.startRange[0], cfg.startRange[1]);
    const endFreq   = mapRange(byte, 0, 255, cfg.endRange[0],   cfg.endRange[1]);
    const duration  = mapRange(byte, 0, 255, cfg.durRange[0],   cfg.durRange[1]);

    const osc = audioCtx.createOscillator();
    const env = audioCtx.createGain();
    osc.type = cfg.wave;
    osc.frequency.setValueAtTime(startFreq, t);
    osc.frequency.exponentialRampToValueAtTime(Math.max(endFreq, 20), t + duration);

    env.gain.setValueAtTime(0.0001, t);
    env.gain.exponentialRampToValueAtTime(Math.max(volume, 0.0001), t + 0.003);
    env.gain.exponentialRampToValueAtTime(0.0001, t + duration);

    osc.connect(env); env.connect(dest);
    osc.start(t); osc.stop(t + duration + 0.03);
    osc.onended = () => { osc.disconnect(); env.disconnect(); };

    return duration;
  }

  function play(audioCtx, masterGain, parsed, opts) {
    const { payloadBytes, typeName, hopCount, obsCount } = parsed;
    const tm = opts.tempoMultiplier;

    const cfg = TYPE_CFG[typeName] || DEFAULT_CFG;

    // Short delay feedback loop for the classic "space ricochet" echo
    const delay = audioCtx.createDelay(0.6);
    delay.delayTime.value = cfg.echo;
    const delayFb = audioCtx.createGain();
    delayFb.gain.value = 0.16; // decays fast — ~5 taps to silence
    delay.connect(delayFb);
    delayFb.connect(delay);
    delay.connect(masterGain);

    const panner = audioCtx.createStereoPanner();
    panner.pan.value = (Math.random() - 0.5) * 0.65;
    panner.connect(masterGain); // dry
    panner.connect(delay);      // into echo

    const volume = Math.min(0.4, 0.12 + (obsCount - 1) * 0.02);
    // More observers → more shots in the burst (1-4)
    const shotCount = Math.max(1, Math.min(4, Math.ceil(Math.log2(obsCount + 1))));

    const sampledBytes = [];
    for (let i = 0; i < shotCount; i++) {
      sampledBytes.push(payloadBytes[Math.floor((i / shotCount) * payloadBytes.length)]);
    }

    let t = audioCtx.currentTime + 0.02;
    let lastEnd = t;

    for (const byte of sampledBytes) {
      const dur = fireShot(audioCtx, panner, t, cfg, byte, volume / shotCount);
      t += dur * 0.65 * tm + 0.04 * tm; // slight overlap for rapid-fire feel
      lastEnd = t + dur * 0.35;
    }

    // Give the delay feedback time to fully decay before disconnecting
    const cleanupMs = (lastEnd - audioCtx.currentTime + cfg.echo * 10 + 1) * 1000;
    setTimeout(() => {
      try { delayFb.disconnect(); delay.disconnect(); panner.disconnect(); } catch (e) {}
    }, cleanupMs);

    return lastEnd - audioCtx.currentTime;
  }

  MeshAudio.registerVoice('blaster', { name: 'blaster', play });
})();
