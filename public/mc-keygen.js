/* === CoreScope — mc-keygen.js === */
'use strict';

(function () {
  /* ---------------------------------------------------------------
   * MeshCoreKeyGenerator
   * CPU path  : WASM workers (default, unchanged)
   * GPU path  : blob hash-workers + WebGPU scanner (jkingsman/meshcore-web-keygen)
   * Keys never leave the device.
   * --------------------------------------------------------------- */

  const ED25519_ORDER = 0x1000000000000000000000000000000014def9dea2f79cd65812631a5cf5d3edn;

  // Inline blob-worker that generates SHA-512 scalar candidates for the GPU pipeline.
  // Each invocation produces scalarWords (8 u32 per candidate) + suffixes (32 bytes each).
  const HASH_WORKER_SCRIPT = `
self.onmessage = async (event) => {
  const { type, batchSize } = event.data;
  if (type !== 'generate') return;
  const scalarWords = new Uint32Array(batchSize * 8);
  const suffixes   = new Uint8Array(batchSize * 32);
  for (let i = 0; i < batchSize; i++) {
    const seed   = crypto.getRandomValues(new Uint8Array(32));
    const digest = new Uint8Array(await crypto.subtle.digest('SHA-512', seed));
    const c0   = digest[0] & 248;
    const c31  = (digest[31] & 63) | 64;
    const wo   = i * 8;
    scalarWords[wo]   = c0          | (digest[1]  << 8) | (digest[2]  << 16) | (digest[3]  << 24);
    scalarWords[wo+1] = digest[4]   | (digest[5]  << 8) | (digest[6]  << 16) | (digest[7]  << 24);
    scalarWords[wo+2] = digest[8]   | (digest[9]  << 8) | (digest[10] << 16) | (digest[11] << 24);
    scalarWords[wo+3] = digest[12]  | (digest[13] << 8) | (digest[14] << 16) | (digest[15] << 24);
    scalarWords[wo+4] = digest[16]  | (digest[17] << 8) | (digest[18] << 16) | (digest[19] << 24);
    scalarWords[wo+5] = digest[20]  | (digest[21] << 8) | (digest[22] << 16) | (digest[23] << 24);
    scalarWords[wo+6] = digest[24]  | (digest[25] << 8) | (digest[26] << 16) | (digest[27] << 24);
    scalarWords[wo+7] = digest[28]  | (digest[29] << 8) | (digest[30] << 16) | (c31        << 24);
    for (let b = 0; b < 32; b++) suffixes[i * 32 + b] = digest[32 + b];
  }
  self.postMessage(
    { type: 'results', scalarWords: scalarWords.buffer, suffixes: suffixes.buffer },
    [scalarWords.buffer, suffixes.buffer]
  );
};
`;

  let nobleEd25519 = null;

  class MeshCoreKeyGenerator {
    constructor() {
      this.isRunning = false;
      this.stopRequested = false;
      this.attempts = 0;
      this.startTime = null;
      this.updateInterval = null;
      this.difficultyUpdateInterval = null;
      this.initialized = false;
      this.currentTargetPrefix = '';

      // CPU (WASM) path state
      this.workers = [];
      this.numWorkers = navigator.hardwareConcurrency || 4;
      this.batchSize = 4096;
      this.targetBatchMs = 20;
      this.minBatchSize = 512;
      this.maxBatchSize = 65536;
      this.progressIntervalMs = 150;
      this.currentJobId = 0;
      this.activeSearch = null;
      this.generationMode = 'wasm';
      this.jsFallbackModule = null;
      this.jsFallbackReason = null;
      this.perfStats = this._emptyPerf();

      // GPU path state
      this.gpuAvailable = false;
      this.gpuChecked = false;
      this.gpuScanner = null;
      this.useGpu = false;
      this.gpuBatchSize = 131072;
      this.hashWorkers = [];
      this.maxHashWorkers = Math.min(6, navigator.hardwareConcurrency || 4);
      this._hashWorkerUrl = null;
    }

    // ------------------------------------------------------------------ perf

    _emptyPerf() {
      return { messages: 0, batches: 0, wasmMs: 0, batchWallMs: 0, startedAt: 0, lastLogAt: 0 };
    }
    _resetPerf() { this.perfStats = this._emptyPerf(); this.perfStats.startedAt = performance.now(); }
    _recordPerf(m) {
      if (!m) return;
      this.perfStats.messages += 1;
      this.perfStats.batches = Math.max(this.perfStats.batches, m.batchCount || 0);
      this.perfStats.wasmMs += m.wasmMs || 0;
      this.perfStats.batchWallMs += m.batchWallMs || 0;
    }

    // ------------------------------------------------------------------ init

    async initialize() {
      if (this.initialized) return;
      let libraryUrl = null;
      const cdnUrls = [
        'https://unpkg.com/noble-ed25519@latest',
        'https://cdn.jsdelivr.net/npm/noble-ed25519@latest',
        'https://esm.sh/noble-ed25519@latest',
        'https://cdn.skypack.dev/noble-ed25519',
        './noble-ed25519-offline-simple.js'
      ];
      for (const url of cdnUrls) {
        try { nobleEd25519 = await import(url); libraryUrl = url; break; }
        catch (e) { /* try next */ }
      }
      if (!nobleEd25519) throw new Error('Failed to load Ed25519 library from all sources.');
      this.libraryUrl = libraryUrl;

      if (typeof WebAssembly === 'undefined') {
        await this._loadJsFallback('WebAssembly not available');
      } else {
        try { await this._initWorkers(); }
        catch (e) { await this._loadJsFallback('WASM worker init failed: ' + e.message); }
      }
      this.initialized = true;
    }

    // ------------------------------------------------------------------ CPU / WASM path

    async _initWorkers() {
      if (this.workers.length > 0) return;
      for (let i = 0; i < this.numWorkers; i++) {
        const worker = new Worker('./wasm/worker.js', { type: 'module' });
        const info = { id: i, worker, attemptedTotal: 0 };
        worker.addEventListener('message', (e) => this._onWorkerMsg(info, e.data));
        worker.addEventListener('error', (e) => this._onWorkerErr(info, e));
        this.workers.push(info);
      }
      this.generationMode = 'wasm';
    }

    async _loadJsFallback(reason) {
      if (!this.jsFallbackModule) {
        this.jsFallbackModule = await import('./js/fallback-keygen.js');
      }
      this.generationMode = 'js-fallback';
      this.jsFallbackReason = reason;
    }

    _stopWorkers() {
      for (const w of this.workers) w.worker.postMessage({ type: 'stop' });
    }

    _onWorkerErr(info, err) {
      if (this.activeSearch && !this.activeSearch.done) {
        this.activeSearch.failures = (this.activeSearch.failures || 0) + 1;
        if (this.activeSearch.failures >= this.workers.length) {
          const s = this.activeSearch; s.done = true; this.activeSearch = null;
          s.reject(new Error('All workers failed.'));
        }
      }
    }

    _onWorkerMsg(info, data) {
      if (!this.activeSearch || data.jobId !== this.activeSearch.jobId) return;
      if (data.metrics) this._recordPerf(data.metrics);
      if (data.type === 'progress' || data.type === 'match') {
        const newTotal = data.attemptedTotal ?? (info.attemptedTotal + (data.attemptedDelta || 0));
        this.attempts += Math.max(0, newTotal - info.attemptedTotal);
        info.attemptedTotal = newTotal;
      }
      if (data.type === 'match' && !this.activeSearch.done) {
        this.activeSearch.done = true;
        this.stopRequested = false;
        this.isRunning = false;
        this._stopWorkers();
        const resolve = this.activeSearch.resolve;
        this.activeSearch = null;
        resolve(data.result);
      }
      if (data.type === 'stopped') {
        this.activeSearch.stopped = (this.activeSearch.stopped || 0) + 1;
        if (this.activeSearch.stopped >= this.workers.length && !this.activeSearch.done) {
          const s = this.activeSearch; s.done = true; this.activeSearch = null;
          this.stopRequested ? s.resolve(null) : s.reject(new Error('Search ended without a match.'));
        }
      }
    }

    _startWorkerSearch(prefix) {
      if (!this.workers.length) return Promise.reject(new Error('No workers available.'));
      this.currentJobId += 1;
      for (const w of this.workers) w.attemptedTotal = 0;
      this._resetPerf();
      const jobId = this.currentJobId;
      return new Promise((resolve, reject) => {
        this.activeSearch = { jobId, done: false, stopped: 0, failures: 0, resolve, reject };
        for (const w of this.workers) {
          w.worker.postMessage({
            type: 'start', jobId, targetPrefix: prefix,
            batchSize: this.batchSize, adaptiveBatching: true,
            targetBatchMs: this.targetBatchMs, minBatchSize: this.minBatchSize,
            maxBatchSize: this.maxBatchSize, progressIntervalMs: this.progressIntervalMs
          });
        }
      });
    }

    async _startJsFallback(prefix) {
      await this._loadJsFallback(this.jsFallbackReason);
      return this.jsFallbackModule.searchVanityKey({
        targetPrefix: prefix,
        batchSize: Math.max(64, Math.floor(this.batchSize / 2)),
        getNobleEd25519: () => nobleEd25519,
        shouldStop: () => this.stopRequested || !this.isRunning,
        onAttempted: (n) => { this.attempts += n; }
      });
    }

    // ------------------------------------------------------------------ GPU path

    async detectGpu() {
      if (this.gpuChecked) return this.gpuAvailable;
      this.gpuChecked = true;
      if (!navigator?.gpu) { this.gpuAvailable = false; return false; }
      try {
        // The bundle is a UMD IIFE — use script injection, not ES import()
        if (!globalThis.MeshCoreGpuModule) {
          await new Promise((resolve, reject) => {
            const s = document.createElement('script');
            s.src = new URL('./vendor/webgpu-ed25519.js', document.baseURI).href;
            s.onload = resolve;
            s.onerror = reject;
            document.head.appendChild(s);
          });
        }
        const { WebGpuEd25519Scanner } = globalThis.MeshCoreGpuModule;
        const scanner = new WebGpuEd25519Scanner();
        const ready = await scanner.initialize();
        if (ready) {
          this.gpuScanner = scanner;
          this.gpuAvailable = true;
          return true;
        }
      } catch (e) {
        console.warn('[GPU] module load failed:', e);
      }
      this.gpuAvailable = false;
      return false;
    }

    async enableGpu() {
      const avail = await this.detectGpu();
      if (!avail) return false;
      await this.gpuScanner.autotuneWorkgroupSize(this.gpuBatchSize);
      await this.gpuScanner.warmup();
      this._initHashWorkers();
      this.useGpu = true;
      return true;
    }

    disableGpu() {
      this.useGpu = false;
    }

    _initHashWorkers() {
      if (this.hashWorkers.length > 0) return;
      const blob = new Blob([HASH_WORKER_SCRIPT], { type: 'application/javascript' });
      this._hashWorkerUrl = URL.createObjectURL(blob);
      for (let i = 0; i < this.maxHashWorkers; i++) {
        this.hashWorkers.push(new Worker(this._hashWorkerUrl));
      }
    }

    _terminateHashWorkers() {
      for (const w of this.hashWorkers) w.terminate();
      this.hashWorkers = [];
      if (this._hashWorkerUrl) { URL.revokeObjectURL(this._hashWorkerUrl); this._hashWorkerUrl = null; }
    }

    async _generateHashBatch() {
      const activeWorkers = this.hashWorkers.slice(0, this.maxHashWorkers);
      if (!activeWorkers.length) throw new Error('No hash workers');
      const perWorker = Math.ceil(this.gpuBatchSize / activeWorkers.length);

      const batches = await Promise.all(activeWorkers.map(worker =>
        new Promise((resolve, reject) => {
          const timeout = setTimeout(() => {
            worker.removeEventListener('message', onMsg);
            worker.removeEventListener('error', onErr);
            reject(new Error('Hash worker timeout'));
          }, 30000);
          const onMsg = (e) => {
            if (e.data.type !== 'results') return;
            clearTimeout(timeout);
            worker.removeEventListener('message', onMsg);
            worker.removeEventListener('error', onErr);
            resolve({ scalarWords: new Uint32Array(e.data.scalarWords), suffixes: new Uint8Array(e.data.suffixes) });
          };
          const onErr = (e) => {
            clearTimeout(timeout);
            worker.removeEventListener('message', onMsg);
            worker.removeEventListener('error', onErr);
            reject(e);
          };
          worker.addEventListener('message', onMsg);
          worker.addEventListener('error', onErr);
          worker.postMessage({ type: 'generate', batchSize: perWorker });
        })
      ));

      const scalarWords = new Uint32Array(batches.reduce((s, b) => s + b.scalarWords.length, 0));
      const suffixes   = new Uint8Array(batches.reduce((s, b) => s + b.suffixes.length, 0));
      let wo = 0, so = 0;
      for (const b of batches) {
        scalarWords.set(b.scalarWords, wo); suffixes.set(b.suffixes, so);
        wo += b.scalarWords.length; so += b.suffixes.length;
      }
      return { scalarWords, suffixes };
    }

    _prefixToBytes(prefix) {
      const bytes = [];
      for (let i = 0; i < prefix.length; i += 2) {
        bytes.push(parseInt(prefix.slice(i, i + 2).padEnd(2, '0'), 16));
      }
      return bytes;
    }

    _unpackScalarBytes(scalarWords, index) {
      const bytes = new Uint8Array(32);
      const wo = index * 8;
      for (let w = 0; w < 8; w++) {
        const v = scalarWords[wo + w], b = w * 4;
        bytes[b] = v & 255; bytes[b+1] = (v >>> 8) & 255;
        bytes[b+2] = (v >>> 16) & 255; bytes[b+3] = (v >>> 24) & 255;
      }
      return bytes;
    }

    _derivePublicKey(clampedScalar) {
      let v = 0n;
      for (let i = 0; i < 32; i++) v |= BigInt(clampedScalar[i]) << BigInt(i * 8);
      const scalar = v % ED25519_ORDER;
      if (scalar === 0n) throw new Error('Scalar reduced to zero');
      const point = nobleEd25519.Point.BASE.multiply(scalar);
      return point.toRawBytes ? point.toRawBytes() : point.toBytes();
    }

    // Pipelined GPU search loop: generates next batch while scanning the current one.
    async _gpuLoop(prefix, prefixLen) {
      const prefixBytes = this._prefixToBytes(prefix);
      let nextBatch = this._generateHashBatch();

      while (this.isRunning) {
        let batch;
        try { batch = await nextBatch; }
        catch (e) { nextBatch = this._generateHashBatch(); continue; }

        nextBatch = this._generateHashBatch(); // overlap hash gen with GPU scan

        const matchedIdxs = await this.gpuScanner.scanBatchMatches(batch.scalarWords, prefixBytes, prefixLen);
        this.attempts += batch.scalarWords.length / 8;

        for (const idx of matchedIdxs) {
          const privBytes = new Uint8Array(64);
          privBytes.set(this._unpackScalarBytes(batch.scalarWords, idx), 0);
          privBytes.set(batch.suffixes.slice(idx * 32, idx * 32 + 32), 32);

          let pubBytes;
          try { pubBytes = this._derivePublicKey(privBytes.slice(0, 32)); }
          catch (e) { continue; }

          const pubHex = this.toHex(pubBytes);
          if (pubHex.startsWith('00') || pubHex.startsWith('FF') || !pubHex.startsWith(prefix)) continue;

          const privHex = this.toHex(privBytes);
          const val = await this.validateKeypair(privHex, pubHex);
          if (!val.valid) continue;

          this.isRunning = false;
          return { publicKey: pubHex, privateKey: privHex, attempts: this.attempts, timeElapsed: (Date.now() - this.startTime) / 1000 };
        }

        await new Promise(r => setTimeout(r, 0)); // yield to event loop
      }
      return null;
    }

    // ------------------------------------------------------------------ shared

    toHex(bytes) {
      return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('').toUpperCase();
    }

    async validateKeypair(privHex, pubHex) {
      try {
        await this.initialize();
        const privBytes = new Uint8Array(privHex.match(/.{2}/g).map(h => parseInt(h, 16)));
        if (privBytes.length !== 64) return { valid: false, error: 'Private key must be 64 bytes' };
        const clamped = privBytes.slice(0, 32);
        if (clamped.every(b => b === 0)) return { valid: false, error: 'Private key is all zeros' };
        if ((clamped[0] & 7) !== 0) return { valid: false, error: 'Scalar clamping error (bits 0-2)' };
        if ((clamped[31] & 192) !== 64) return { valid: false, error: 'Scalar clamping error (bits 6-7)' };
        const pubBytes = new Uint8Array(pubHex.match(/.{2}/g).map(h => parseInt(h, 16)));
        if (pubBytes.length !== 32 || pubBytes.every(b => b === 0)) return { valid: false, error: 'Invalid public key' };

        let derived;
        try {
          let scalar = 0n;
          for (let i = 0; i < 32; i++) scalar += BigInt(clamped[i]) << BigInt(8 * i);
          derived = nobleEd25519.Point.BASE.multiply(scalar);
        } catch {
          try { derived = await nobleEd25519.getPublicKey(clamped); }
          catch { derived = nobleEd25519.getPublicKey(clamped); }
        }

        let derivedBytes;
        if (derived instanceof Uint8Array) derivedBytes = derived;
        else if (derived?.toRawBytes) derivedBytes = derived.toRawBytes();
        else if (derived?.toBytes)    derivedBytes = derived.toBytes();
        else {
          const arr = new Uint8Array(32);
          for (let i = 0; i < 31; i++) arr[i] = Number((derived.y >> BigInt(8 * i)) & 255n);
          arr[31] = Number((derived.x & 1n) << 7n);
          derivedBytes = arr;
        }
        if (this.toHex(derivedBytes) !== pubHex) return { valid: false, error: 'Private key does not derive the given public key' };
        return { valid: true };
      } catch (e) {
        return { valid: false, error: 'Validation error: ' + e.message };
      }
    }

    async generateVanityKey(prefix, prefixLen) {
      this.isRunning = true;
      this.stopRequested = false;
      this.attempts = 0;
      this.startTime = Date.now();
      this.currentTargetPrefix = prefix;

      const tick = () => {
        if (!this.isRunning) return;
        const elapsed = (Date.now() - this.startTime) / 1000;
        const rate = this.attempts / Math.max(elapsed, 0.001);
        const el = (id) => document.getElementById(id);
        if (el('kgn-attempts')) el('kgn-attempts').textContent = this.attempts.toLocaleString();
        if (el('kgn-elapsed'))  el('kgn-elapsed').textContent  = elapsed.toFixed(1) + 's';
        if (el('kgn-rate'))     el('kgn-rate').textContent     = Math.round(rate).toLocaleString();
        const method = this.useGpu
          ? `GPU + ${this.maxHashWorkers} hash workers`
          : this.generationMode === 'js-fallback' ? 'JS fallback' : `${this.workers.length} WASM workers`;
        const pt = el('kgn-progress-text');
        if (pt) pt.textContent = `${this.attempts.toLocaleString()} attempts | ${Math.round(rate).toLocaleString()} keys/sec | ${elapsed.toFixed(1)}s [${method}]`;
        const prob = 1 / Math.pow(16, prefixLen);
        const pct = Math.min((this.attempts * prob) * 100, 99);
        const fill = el('kgn-progress-fill');
        if (fill) fill.style.width = pct + '%';
      };

      this.updateInterval = setInterval(tick, 100);
      this.difficultyUpdateInterval = setInterval(() => {
        if (!this.isRunning) return;
        const elapsed = (Date.now() - this.startTime) / 1000;
        if (elapsed >= 10) updateDifficulty(this.currentTargetPrefix, this.attempts / elapsed);
      }, 10000);

      try {
        let matched = null;

        if (this.useGpu && this.gpuScanner) {
          matched = await this._gpuLoop(prefix, prefixLen);
        } else if (this.generationMode === 'js-fallback') {
          matched = await this._startJsFallback(prefix);
        } else {
          try { matched = await this._startWorkerSearch(prefix); }
          catch (e) {
            await this._loadJsFallback('WASM search failed: ' + e.message);
            matched = await this._startJsFallback(prefix);
          }
        }

        if (!matched) return null;
        const validation = await this.validateKeypair(matched.privateKey, matched.publicKey);
        if (!validation.valid) throw new Error('Key validation failed: ' + validation.error);
        this.isRunning = false;
        this._clearTimers();
        tick();
        return { publicKey: matched.publicKey, privateKey: matched.privateKey, attempts: this.attempts, timeElapsed: (Date.now() - this.startTime) / 1000 };
      } catch (e) {
        this.isRunning = false;
        if (!this.useGpu) this._stopWorkers();
        this._clearTimers();
        throw e;
      }
    }

    _clearTimers() {
      if (this.updateInterval)           { clearInterval(this.updateInterval);           this.updateInterval = null; }
      if (this.difficultyUpdateInterval) { clearInterval(this.difficultyUpdateInterval); this.difficultyUpdateInterval = null; }
    }

    stop() {
      this.isRunning = false;
      this.stopRequested = true;
      if (!this.useGpu) this._stopWorkers();
      this._clearTimers();
    }

    destroy() {
      this.stop();
      for (const w of this.workers) w.worker.terminate();
      this.workers = [];
      this._terminateHashWorkers();
      this.initialized = false;
    }
  }

  /* ---------------------------------------------------------------
   * Difficulty estimate helper
   * --------------------------------------------------------------- */
  function difficultyInfo(prefix, currentRate) {
    if (!prefix || !prefix.length) return null;
    const expected = Math.pow(16, prefix.length);
    const rate = currentRate || 10000;
    const secs = expected / rate;

    const fmtCount = (n) => {
      if (n >= 1e12) return (n / 1e12).toFixed(1) + ' trillion';
      if (n >= 1e9)  return (n / 1e9).toFixed(1) + ' billion';
      if (n >= 1e6)  return (n / 1e6).toFixed(1) + ' million';
      if (n >= 1e3)  return (n / 1e3).toFixed(1) + ' thousand';
      return Math.round(n).toLocaleString();
    };
    const fmtTime = (s) => {
      if (s >= 31536000) return (s / 31536000).toFixed(1) + ' years';
      if (s >= 2592000)  return (s / 2592000).toFixed(1) + ' months';
      if (s >= 86400)    return (s / 86400).toFixed(1) + ' days';
      if (s >= 3600)     return (s / 3600).toFixed(1) + ' hours';
      if (s >= 60)       return (s / 60).toFixed(1) + ' minutes';
      return Math.round(s) + ' seconds';
    };
    const fmtRate = (r) => r >= 1000 ? Math.round(r / 1000) + 'k keys/sec' : Math.round(r) + ' keys/sec';

    const levels = [
      [1000,       'Very Easy', 'var(--status-green, #27ae60)'],
      [100000,     'Easy',      '#2ecc71'],
      [10000000,   'Moderate',  'var(--status-yellow, #f39c12)'],
      [1000000000, 'Hard',      'var(--status-orange, #e67e22)'],
      [1e11,       'Very Hard', 'var(--status-red, #e74c3c)'],
    ];
    let level = 'Extreme', color = '#8e44ad';
    for (const [threshold, lbl, col] of levels) {
      if (expected <= threshold) { level = lbl; color = col; break; }
    }

    return { level, color, expected, fmtCount: fmtCount(expected), fmtTime: fmtTime(secs), fmtRate: fmtRate(rate), prob: (1 / expected) };
  }

  function updateDifficulty(prefix, rate) {
    const info   = document.getElementById('kgn-prefix-info');
    const detail = document.getElementById('kgn-prefix-detail');
    if (!info || !detail) return;
    const d = difficultyInfo(prefix, rate);
    if (!d) { info.style.display = 'none'; return; }
    detail.innerHTML = `
      <span class="kgn-difficulty-badge" style="color:${d.color}">${d.level}</span>
      ~${d.fmtCount} attempts &nbsp;·&nbsp; ~${d.fmtTime} at ${d.fmtRate}
      <br><small style="color:var(--text-muted)">Probability: ${(d.prob * 100).toFixed(6)}% per attempt</small>`;
    info.style.display = 'block';
  }

  /* ---------------------------------------------------------------
   * SPA page HTML
   * --------------------------------------------------------------- */
  const HTML = `
<div class="keygen-page">
  <div class="keygen-header">
    <h1>MC-Keygen</h1>
    <p class="keygen-subtitle">Generate custom Ed25519 key pairs for your MeshCore nodes — runs entirely in your browser, keys never leave your device.</p>
    <a href="https://github.com/jkingsman/meshcore-web-keygen" target="_blank" rel="noopener" class="keygen-source-link">View source on GitHub ↗</a>
  </div>

  <div class="kgn-card">
    <div class="kgn-info-box">
      <strong>About MeshCore Keys</strong>
      <ul>
        <li>MeshCore uses the first two characters of your public key as a node identifier. Choosing a custom prefix lets you avoid collisions with neighbouring nodes.</li>
        <li>All processing runs in your browser — keys are never transmitted.</li>
      </ul>
    </div>

    <form id="kgn-form">
      <div class="kgn-field">
        <label for="kgn-prefix" class="kgn-label">Target Prefix (Hex)</label>
        <input type="text" id="kgn-prefix" class="kgn-input kgn-mono"
          placeholder="e.g. F8, F8A1, FFF …" maxlength="8" pattern="[0-9A-Fa-f]+"
          autocomplete="off" spellcheck="false">
        <small class="kgn-hint">1–8 hex characters. Longer prefixes take exponentially longer.</small>
        <div id="kgn-prefix-info" class="kgn-prefix-info" style="display:none">
          <span class="kgn-prefix-label">📊 Difficulty</span>
          <span id="kgn-prefix-detail"></span>
        </div>
        <div id="kgn-reserved-warning" class="kgn-warning" style="display:none">
          ⚠️ Prefixes starting with <code>00</code> or <code>FF</code> are reserved by MeshCore.
        </div>
      </div>

      <div class="kgn-field kgn-gpu-row" id="kgn-gpu-row" style="display:none">
        <label class="kgn-gpu-label" style="display:flex;align-items:center;gap:8px;cursor:pointer">
          <input type="checkbox" id="kgn-gpu-toggle" style="width:16px;height:16px;cursor:pointer">
          <span>⚡ Use GPU acceleration (WebGPU)</span>
        </label>
        <div class="kgn-hint" id="kgn-gpu-hint" style="margin-top:4px"></div>
      </div>

      <div class="kgn-btns">
        <button type="submit" class="kgn-btn kgn-btn-primary" id="kgn-generate-btn" disabled>Generate Key</button>
        <button type="button" class="kgn-btn kgn-btn-secondary" id="kgn-stop-btn" disabled>Stop</button>
        <button type="button" class="kgn-btn kgn-btn-secondary" id="kgn-popup-btn" title="Open in a popup window so generation continues while you browse other pages">⧉ Open in popup</button>
      </div>
    </form>

    <div id="kgn-progress" class="kgn-progress" style="display:none">
      <div class="kgn-progress-bar"><div class="kgn-progress-fill" id="kgn-progress-fill"></div></div>
      <div class="kgn-progress-text" id="kgn-progress-text">Initialising…</div>
    </div>

    <div id="kgn-error" class="kgn-error" style="display:none"></div>

    <div id="kgn-result" class="kgn-result" style="display:none">
      <h3 class="kgn-result-title">✓ Key generated successfully!</h3>

      <div class="kgn-key-block">
        <div class="kgn-key-label">Public Key</div>
        <div class="kgn-key-value kgn-mono" id="kgn-pub"></div>
      </div>
      <div class="kgn-key-block">
        <div class="kgn-key-label">Private Key</div>
        <div class="kgn-key-value kgn-mono" id="kgn-priv"></div>
      </div>
      <div class="kgn-key-block">
        <div class="kgn-key-label">Validation</div>
        <div class="kgn-key-value" id="kgn-validation" style="color:var(--status-green,#27ae60);font-weight:600">
          ✓ RFC 8032 Ed25519 compliant — scalar clamping and key consistency verified
        </div>
      </div>

      <div class="kgn-stats">
        <div class="kgn-stat"><span class="kgn-stat-val" id="kgn-attempts">0</span><span class="kgn-stat-lbl">Attempts</span></div>
        <div class="kgn-stat"><span class="kgn-stat-val" id="kgn-elapsed">0s</span><span class="kgn-stat-lbl">Time</span></div>
        <div class="kgn-stat"><span class="kgn-stat-val" id="kgn-rate">0</span><span class="kgn-stat-lbl">Keys/sec</span></div>
      </div>

      <div class="kgn-btns kgn-result-btns">
        <button type="button" class="kgn-btn kgn-btn-primary" id="kgn-download-btn">Download JSON</button>
        <button type="button" class="kgn-btn kgn-btn-secondary" id="kgn-import-btn">📋 How to Import</button>
      </div>
    </div>
  </div>

  <!-- Import instructions modal -->
  <div id="kgn-modal" class="kgn-modal" style="display:none" role="dialog" aria-modal="true" aria-label="Import instructions">
    <div class="kgn-modal-box">
      <div class="kgn-modal-head">
        <h3>📋 How to Import Keys into MeshCore</h3>
        <button class="kgn-modal-close" id="kgn-modal-close" aria-label="Close">×</button>
      </div>
      <div class="kgn-modal-body">
        <div class="kgn-import-section">
          <h4>🔧 Companion Nodes</h4>
          <ol>
            <li>Connect to your node using the MeshCore app.</li>
            <li>Tap the <strong>Settings gear</strong> icon.</li>
            <li>Tap <strong>Manage Identity Key</strong>.</li>
            <li>Paste your <strong>Private Key</strong> (128-char hex string) into the text box.</li>
            <li>Tap <strong>Import Private Key</strong>.</li>
            <li><strong>Important:</strong> Tap the checkmark ✓ to save changes in settings.</li>
          </ol>
        </div>
        <div class="kgn-import-section">
          <h4>💻 Repeater + Computer (USB serial)</h4>
          <ol>
            <li>Connect the repeater to your computer via USB.</li>
            <li>Open the <a href="https://flasher.meshcore.co.uk/" target="_blank" rel="noopener">MeshCore Web Console</a> or any terminal.</li>
            <li>Run: <code>set prv.key &lt;your_private_key&gt;</code></li>
            <li>Reboot the device.</li>
          </ol>
        </div>
        <div class="kgn-import-section">
          <h4>📡 Repeater + Companion (remote LoRa)</h4>
          <ol>
            <li>Select the repeater from your contact list.</li>
            <li>Enter the password and log in.</li>
            <li>Open <strong>Command Line</strong> (bottom of the repeater info screen).</li>
            <li>Enter <code>set prv.key &lt;hex&gt;</code> and press Enter.</li>
            <li>Reboot the repeater for the new key to take effect.</li>
          </ol>
        </div>
        <div class="kgn-import-section">
          <h4>📄 JSON Import (Companion nodes)</h4>
          <ol>
            <li>Download the JSON file from the generator above.</li>
            <li>In the MeshCore app, go to <strong>Import Config</strong>.</li>
            <li>Select the downloaded JSON file.</li>
          </ol>
        </div>
      </div>
    </div>
  </div>

  <!-- FAQ -->
  <div class="keygen-faq">
    <h2>FAQ</h2>

    <div class="keygen-faq-section">
      <h3>What does this tool do?</h3>
      <p>Generates Ed25519 key pairs where the public key starts with a custom hex prefix (1–8 characters). MeshCore uses the first two characters of the public key as the node identifier — a custom prefix lets you avoid collisions with neighbouring nodes.</p>
    </div>

    <div class="keygen-faq-section">
      <h3>Is it safe to use?</h3>
      <p>Yes. All cryptographic processing runs locally in your browser using the Web Crypto API. Keys are never sent anywhere.</p>
    </div>

    <div class="keygen-faq-section">
      <h3>What is GPU acceleration?</h3>
      <p>When your browser supports WebGPU, you can enable the ⚡ GPU toggle to use your graphics card for prefix scanning. The GPU evaluates thousands of candidates in parallel, typically delivering a significant speedup for longer prefixes. A ~2 MB WebGPU module is downloaded on first use. GPU mode requires Chrome 113+ or Edge 113+; Firefox and Safari do not yet support WebGPU.</p>
    </div>

    <div class="keygen-faq-section">
      <h3>What does "Open in popup" do?</h3>
      <p>Launches the keygen in a separate browser window so generation continues uninterrupted while you navigate to other pages in the main window. Each window runs independently with its own workers.</p>
    </div>

    <div class="keygen-faq-section">
      <h3>How long does generation take?</h3>
      <p>Depends on prefix length and hardware. At ~100,000 keys/second (modern desktop, CPU):</p>
      <table class="keygen-perf-table">
        <thead><tr><th>Prefix length</th><th>Expected time</th></tr></thead>
        <tbody>
          <tr><td>2 characters</td><td>&lt; 1 second</td></tr>
          <tr><td>4 characters</td><td>~0.7 seconds</td></tr>
          <tr><td>6 characters</td><td>~3 minutes</td></tr>
          <tr><td>8 characters</td><td>~12 hours</td></tr>
        </tbody>
      </table>
    </div>

    <div class="keygen-faq-section">
      <h3>What prefixes are reserved?</h3>
      <p>Prefixes beginning with <code>00</code> or <code>FF</code> are reserved by the MeshCore protocol. The tool blocks generation for those.</p>
    </div>

    <div class="keygen-faq-section">
      <h3>Troubleshooting</h3>
      <ul>
        <li><strong>Key not appearing after import:</strong> Tap the checkmark ✓ to save changes.</li>
        <li><strong>Wrong key format:</strong> Copy the full 128-character private key.</li>
        <li><strong>Key import fails:</strong> Verify the public key prefix matches, then retry.</li>
        <li><strong>Browser freezes / slow:</strong> Refresh and try a shorter prefix.</li>
        <li><strong>GPU toggle not visible:</strong> Your browser does not support WebGPU. Try Chrome 113+ or Edge 113+.</li>
      </ul>
    </div>

    <div class="keygen-faq-section keygen-faq-links">
      <h3>Further reading</h3>
      <ul>
        <li><a href="https://github.com/meshcore-dev/MeshCore/blob/main/docs/faq.md" target="_blank" rel="noopener">MeshCore protocol FAQ</a></li>
        <li><a href="https://meshcore.gg/" target="_blank" rel="noopener">MeshCore Discord community</a></li>
        <li><a href="https://github.com/agessaman/meshcore-keygen" target="_blank" rel="noopener">Python version (multi-threaded, batch processing)</a></li>
      </ul>
    </div>
  </div>
</div>`;

  function $ (id) { return document.getElementById(id); }

  function isReserved(prefix) {
    return prefix.length >= 2 && (prefix.startsWith('00') || prefix.startsWith('FF'));
  }

  let generator = null;
  let _modalClickHandler = null;
  let _keydownHandler = null;

  function init(app) {
    app.innerHTML = HTML;

    generator = new MeshCoreKeyGenerator();

    const form        = $('kgn-form');
    const prefixInput = $('kgn-prefix');
    const generateBtn = $('kgn-generate-btn');
    const stopBtn     = $('kgn-stop-btn');
    const progressEl  = $('kgn-progress');
    const resultEl    = $('kgn-result');
    const errorEl     = $('kgn-error');
    const modal       = $('kgn-modal');
    const gpuRow      = $('kgn-gpu-row');
    const gpuToggle   = $('kgn-gpu-toggle');
    const gpuHint     = $('kgn-gpu-hint');

    function showError(msg) { errorEl.textContent = msg; errorEl.style.display = 'block'; }
    function hideError()    { errorEl.style.display = 'none'; }

    function setGenerating(on) {
      generateBtn.disabled = on;
      stopBtn.disabled     = !on;
      progressEl.style.display = on ? 'block' : 'none';
      if (gpuToggle) gpuToggle.disabled = on;
    }

    function showResult(result) {
      $('kgn-pub').textContent  = result.publicKey;
      $('kgn-priv').textContent = result.privateKey;
      $('kgn-attempts').textContent = result.attempts.toLocaleString();
      $('kgn-elapsed').textContent  = result.timeElapsed.toFixed(1) + 's';
      $('kgn-rate').textContent = Math.round(result.attempts / result.timeElapsed).toLocaleString();
      resultEl.style.display = 'block';
      resultEl.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }

    // Probe GPU availability (non-blocking)
    generator.detectGpu().then(avail => {
      if (!avail) return;
      gpuRow.style.display = 'block';
      gpuHint.textContent = 'GPU detected — loads a ~2 MB WebGPU module and autotunes on first use.';
    }).catch(() => {});

    // Popup button — opens the SPA in a separate window; generation is independent
    $('kgn-popup-btn').addEventListener('click', () => {
      const base = location.href.split('#')[0];
      window.open(base + '#/mc-keygen', 'meshcore-keygen',
        'width=800,height=920,resizable=yes,scrollbars=yes');
    });

    prefixInput.addEventListener('input', () => {
      const val = prefixInput.value.trim().toUpperCase();
      const valid = /^[0-9A-F]+$/.test(val) && val.length >= 1 && val.length <= 8;
      const reserved = isReserved(val);
      $('kgn-reserved-warning').style.display = reserved ? 'block' : 'none';
      generateBtn.disabled = !valid || reserved;
      if (valid) updateDifficulty(val, null);
      else $('kgn-prefix-info').style.display = 'none';
    });

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const prefix = prefixInput.value.trim().toUpperCase();

      if (!prefix || !/^[0-9A-F]+$/.test(prefix) || prefix.length > 8) {
        showError('Please enter a valid hex prefix (1–8 characters, 0-9 A-F).');
        return;
      }
      if (isReserved(prefix)) {
        showError('Prefixes starting with 00 or FF are reserved by MeshCore.');
        return;
      }

      hideError();
      resultEl.style.display = 'none';
      setGenerating(true);

      const wantGpu = gpuToggle && gpuToggle.checked;
      $('kgn-progress-text').textContent = wantGpu
        ? 'Loading WebGPU module and autotuning…'
        : 'Loading Ed25519 library…';
      $('kgn-progress-fill').style.width = '0%';

      try {
        await generator.initialize();

        if (wantGpu) {
          const gpuReady = await generator.enableGpu();
          if (!gpuReady) {
            if (gpuToggle) gpuToggle.checked = false;
            gpuHint.textContent = 'GPU unavailable in this session — falling back to CPU.';
            generator.disableGpu();
          }
        } else {
          generator.disableGpu();
        }

        const result = await generator.generateVanityKey(prefix, prefix.length);
        if (result) {
          showResult(result);
        } else {
          showError('Key generation was stopped.');
        }
      } catch (err) {
        showError('Error generating key: ' + err.message);
      } finally {
        setGenerating(false);
        stopBtn.textContent = 'Stop';
        stopBtn.disabled    = true;
        generateBtn.disabled = false;
      }
    });

    stopBtn.addEventListener('click', () => {
      if (!generator.isRunning) return;
      generator.stop();
      stopBtn.disabled = true;
    });

    $('kgn-download-btn').addEventListener('click', () => {
      const pub    = $('kgn-pub').textContent;
      const priv   = $('kgn-priv').textContent;
      const prefix = prefixInput.value.trim().toUpperCase();
      const blob   = new Blob([JSON.stringify({ public_key: pub, private_key: priv }, null, 2)], { type: 'application/json' });
      const url    = URL.createObjectURL(blob);
      const a = Object.assign(document.createElement('a'), { href: url, download: `meshcore_${prefix}_${Date.now()}.json` });
      document.body.appendChild(a); a.click(); document.body.removeChild(a);
      URL.revokeObjectURL(url);
    });

    $('kgn-import-btn').addEventListener('click', () => { modal.style.display = 'flex'; });
    $('kgn-modal-close').addEventListener('click', () => { modal.style.display = 'none'; });

    _modalClickHandler = (e) => { if (e.target === modal) modal.style.display = 'none'; };
    _keydownHandler    = (e) => { if (e.key === 'Escape' && modal.style.display !== 'none') modal.style.display = 'none'; };
    window.addEventListener('click', _modalClickHandler);
    document.addEventListener('keydown', _keydownHandler);

    // Pre-fill from hash query param: #/mc-keygen?prefix=F8
    const hashQuery = location.hash.includes('?') ? location.hash.split('?')[1] : '';
    const urlPrefix = new URLSearchParams(hashQuery).get('prefix');
    if (urlPrefix) {
      const clean = urlPrefix.trim().toUpperCase();
      if (/^[0-9A-F]+$/.test(clean) && clean.length <= 8) {
        prefixInput.value = clean;
        prefixInput.dispatchEvent(new Event('input'));
      }
    }

    prefixInput.focus();
  }

  function destroy() {
    if (generator) { generator.destroy(); generator = null; }
    if (_modalClickHandler) { window.removeEventListener('click', _modalClickHandler); _modalClickHandler = null; }
    if (_keydownHandler)    { document.removeEventListener('keydown', _keydownHandler); _keydownHandler = null; }
  }

  registerPage('mc-keygen', { init, destroy });
})();
