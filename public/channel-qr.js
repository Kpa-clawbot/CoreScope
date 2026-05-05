/**
 * channel-qr.js — QR code generation + scanning for MeshCore channels.
 *
 * URL format (per firmware spec):
 *   meshcore://channel/add?name=<urlencoded>&secret=<32hex>
 *
 * Public API (window.ChannelQR):
 *   buildUrl(name, secretHex)            → string
 *   parseChannelUrl(url)                 → {name, secret} | null
 *   generate(name, secretHex, target)    → renders QR + URL + Copy Key into `target`
 *   scan()                               → Promise<{name, secret} | null>
 *
 * Self-contained: does NOT touch channels.js / channel-decrypt.js.
 * The PR that wires the modal into this module is #3.
 *
 * Vendored deps (loaded by index.html):
 *   - public/vendor/qrcode.js   (davidshimjs/qrcodejs, MIT) — QR rendering
 *   - public/vendor/jsqr.min.js (cozmo/jsQR, Apache-2.0)    — QR decoding from camera
 */
(function (root) {
  'use strict';

  const SCHEME_PREFIX = 'meshcore://channel/add';
  const HEX32_RE = /^[0-9a-fA-F]{32}$/;

  function buildUrl(name, secretHex) {
    return SCHEME_PREFIX + '?name=' + encodeURIComponent(String(name)) +
           '&secret=' + String(secretHex);
  }

  /**
   * parseChannelUrl(url) → { name, secret } | null
   * Strict: scheme must be `meshcore:`, host+path `//channel/add`,
   * both `name` and `secret` query params present, secret must be 32 hex chars.
   */
  function parseChannelUrl(url) {
    if (!url || typeof url !== 'string') return null;
    if (url.indexOf(SCHEME_PREFIX) !== 0) return null;

    // Strip prefix → query string
    const rest = url.slice(SCHEME_PREFIX.length);
    if (rest[0] !== '?' && rest !== '') return null;
    const qs = rest.slice(1);
    if (!qs) return null;

    const params = {};
    const pairs = qs.split('&');
    for (let i = 0; i < pairs.length; i++) {
      const eq = pairs[i].indexOf('=');
      if (eq < 0) continue;
      const k = pairs[i].slice(0, eq);
      const v = pairs[i].slice(eq + 1);
      try { params[k] = decodeURIComponent(v); }
      catch (_e) { return null; }
    }

    if (!params.name || !params.secret) return null;
    if (!HEX32_RE.test(params.secret)) return null;

    return { name: params.name, secret: params.secret.toLowerCase() };
  }

  // ---------- DOM helpers (browser-only) ----------

  function _hasDom() {
    return typeof document !== 'undefined' && document.createElement;
  }

  /**
   * Render QR + URL + Copy Key button into `target`.
   * Requires window.QRCode (vendor/qrcode.js) loaded.
   */
  function generate(name, secretHex, target) {
    if (!_hasDom() || !target) return;
    target.innerHTML = '';

    const url = buildUrl(name, secretHex);

    const qrBox = document.createElement('div');
    qrBox.className = 'channel-qr-canvas';
    qrBox.style.display = 'inline-block';
    target.appendChild(qrBox);

    if (typeof root.QRCode === 'function') {
      try {
        // davidshimjs/qrcodejs API: new QRCode(el, {text, width, height, ...})
        new root.QRCode(qrBox, {
          text: url,
          width: 192,
          height: 192,
          correctLevel: root.QRCode.CorrectLevel ? root.QRCode.CorrectLevel.M : 0,
        });
      } catch (e) {
        qrBox.textContent = '[QR render failed: ' + (e && e.message || e) + ']';
      }
    } else {
      qrBox.textContent = '[QR library not loaded]';
    }

    const urlLine = document.createElement('div');
    urlLine.className = 'channel-qr-url';
    urlLine.style.cssText = 'font-family:monospace;font-size:11px;word-break:break-all;margin-top:6px;';
    urlLine.textContent = url;
    target.appendChild(urlLine);

    const copyBtn = document.createElement('button');
    copyBtn.type = 'button';
    copyBtn.className = 'channel-qr-copy';
    copyBtn.textContent = '📋 Copy Key';
    copyBtn.style.cssText = 'margin-top:6px;';
    copyBtn.addEventListener('click', function () {
      const text = secretHex;
      const done = function () {
        const orig = copyBtn.textContent;
        copyBtn.textContent = '✓ Copied';
        setTimeout(function () { copyBtn.textContent = orig; }, 1200);
      };
      if (root.navigator && root.navigator.clipboard && root.navigator.clipboard.writeText) {
        root.navigator.clipboard.writeText(text).then(done, function () {
          // Fallback: select text in a temp input
          _fallbackCopy(text); done();
        });
      } else {
        _fallbackCopy(text); done();
      }
    });
    target.appendChild(copyBtn);
  }

  function _fallbackCopy(text) {
    if (!_hasDom()) return;
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.cssText = 'position:fixed;opacity:0;';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (_e) {}
    document.body.removeChild(ta);
  }

  // ---------- Camera scan ----------

  /**
   * scan() → Promise<{name, secret} | null>
   *
   * Opens a small modal with a live camera preview, decodes via jsQR,
   * resolves with the parsed channel info on first valid match. Closes
   * camera on resolve/reject. Resolves with `null` if user cancels or
   * camera permission is denied (graceful fallback path).
   */
  function scan() {
    if (!_hasDom()) return Promise.resolve(null);
    const nav = root.navigator;
    if (!nav || !nav.mediaDevices || !nav.mediaDevices.getUserMedia ||
        typeof root.jsQR !== 'function') {
      _showCameraFallback();
      return Promise.resolve(null);
    }

    return new Promise(function (resolve) {
      const overlay = document.createElement('div');
      overlay.className = 'channel-qr-scan-overlay';
      overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.85);' +
        'display:flex;flex-direction:column;align-items:center;justify-content:center;z-index:99999;';

      const video = document.createElement('video');
      video.setAttribute('playsinline', 'true');
      video.style.cssText = 'max-width:90vw;max-height:60vh;background:#000;';
      overlay.appendChild(video);

      const status = document.createElement('div');
      status.style.cssText = 'color:#fff;margin-top:12px;font-family:sans-serif;';
      status.textContent = 'Point camera at a MeshCore channel QR…';
      overlay.appendChild(status);

      const cancelBtn = document.createElement('button');
      cancelBtn.type = 'button';
      cancelBtn.textContent = 'Cancel';
      cancelBtn.style.cssText = 'margin-top:12px;';
      overlay.appendChild(cancelBtn);

      document.body.appendChild(overlay);

      const canvas = document.createElement('canvas');
      const ctx = canvas.getContext('2d');
      let stream = null;
      let rafId = 0;
      let done = false;

      function cleanup(result) {
        if (done) return;
        done = true;
        if (rafId) cancelAnimationFrame(rafId);
        if (stream) {
          stream.getTracks().forEach(function (t) { try { t.stop(); } catch (_e) {} });
        }
        if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
        resolve(result);
      }

      cancelBtn.addEventListener('click', function () { cleanup(null); });

      function tick() {
        if (done) return;
        if (video.readyState === video.HAVE_ENOUGH_DATA) {
          canvas.width = video.videoWidth;
          canvas.height = video.videoHeight;
          ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
          let imgData;
          try { imgData = ctx.getImageData(0, 0, canvas.width, canvas.height); }
          catch (_e) { rafId = requestAnimationFrame(tick); return; }
          const code = root.jsQR(imgData.data, imgData.width, imgData.height, {
            inversionAttempts: 'dontInvert',
          });
          if (code && code.data) {
            const parsed = parseChannelUrl(code.data);
            if (parsed) { cleanup(parsed); return; }
            status.textContent = 'QR found but not a MeshCore channel — keep trying…';
          }
        }
        rafId = requestAnimationFrame(tick);
      }

      nav.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
        .then(function (s) {
          stream = s;
          video.srcObject = s;
          video.play().then(function () { tick(); }, function () { tick(); });
        })
        .catch(function () {
          status.textContent = 'Camera not available — paste key manually.';
          setTimeout(function () { cleanup(null); }, 1800);
        });
    });
  }

  function _showCameraFallback() {
    if (!_hasDom()) return;
    const note = document.createElement('div');
    note.className = 'channel-qr-fallback';
    note.style.cssText = 'position:fixed;bottom:20px;left:50%;transform:translateX(-50%);' +
      'background:#222;color:#fff;padding:10px 14px;border-radius:6px;z-index:99999;';
    note.textContent = 'Camera not available — paste key manually.';
    document.body.appendChild(note);
    setTimeout(function () {
      if (note.parentNode) note.parentNode.removeChild(note);
    }, 2500);
  }

  root.ChannelQR = {
    buildUrl: buildUrl,
    parseChannelUrl: parseChannelUrl,
    generate: generate,
    scan: scan,
  };
})(typeof window !== 'undefined' ? window : globalThis);
