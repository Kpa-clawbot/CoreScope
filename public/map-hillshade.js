/* map-hillshade.js — client-side hillshade overlay (#2428 map providers).
 *
 * Fetches Terrarium-encoded elevation PNG tiles (elevation m =
 * R*256 + G + B/256 - 32768) and shades them on a canvas into
 * semi-transparent relief that works over light, dark and satellite basemaps.
 *
 *   window.MC_hillshadeShade(elev, w, h, zoom, lat, out)
 *     Pure kernel. elev: Float32Array (w*h metres), out: Uint8ClampedArray
 *     (w*h*4 RGBA). Horn gradient, light from the NW (azimuth 315°, altitude
 *     45°). Each pixel is black (shadow) or white (highlight) with alpha equal
 *     to how far it is from the flat-terrain brightness, so flat ground is
 *     fully transparent. No-data (<= -500 m, incl. the Terrarium zero value
 *     -32768) is transparent.
 *
 *   window.MC_createHillshadeLayer(opts) -> L.GridLayer
 *     opts { url, opacity = 0.35, maxZoom = 19, maxNativeZoom = 15,
 *            attribution, zIndex = 250, pane? }
 *
 * Edges: gradients use the tile's own pixels only (border rows/columns fall
 * back to a one-sided difference). No neighbour tiles are fetched, so a faint
 * 1px seam can show where two tiles meet on very rough terrain.
 *
 * Security (S6): crossOrigin is set before src; a host without CORS taints the
 * canvas, getImageData throws, and the layer goes blank for good with a single
 * console warning. Images that are not 256x256 or 512x512 are refused.
 */
(function () {
  'use strict';

  var NODATA = -500;                    // at or below: ocean floor / no-data
  var DEG = Math.PI / 180;
  // Local refs keep global lookups out of the per-pixel loop.
  var sqrt = Math.sqrt, exp = Math.exp;
  // Light vector, azimuth 315° (NW), altitude 45°; x east, y north, z up.
  var ALT = 45 * DEG, AZ = 315 * DEG;
  var LX = Math.cos(ALT) * Math.sin(AZ);
  var LY = Math.cos(ALT) * Math.cos(AZ);
  var LZ = Math.sin(ALT);
  // |brightness delta| -> alpha via 1 - e^(-GAIN*d): linear for gentle
  // slopes, a soft knee instead of a hard clip so steep faces keep detail.
  var GAIN = 2.4;

  // Vertical exaggeration: strong at low zoom, where a pixel spans hundreds of
  // metres and real slopes average out, easing to 1 by zoom 14. It falls by
  // well under 2x per zoom, so on-screen relief still grows with zoom.
  function exaggeration(zoom) {
    var e = Math.pow(2, (14 - zoom) * 0.35);
    return e < 1 ? 1 : (e > 10 ? 10 : e);
  }

  function hillshadeShade(elev, w, h, zoom, lat, out) {
    var cosLat = Math.cos((Number(lat) || 0) * DEG);
    if (!(cosLat > 0.01)) cosLat = 0.01;
    var mpp = 156543.03392 * cosLat / Math.pow(2, zoom) * (256 / w);
    var k = exaggeration(zoom) / (4 * mpp);
    var x, y, i, o, c, xm, xp, ym, yp, rm, r0, rp, sx, sy;
    var a, b, cc, d, f, g, hh, ii, dzdx, dzdy, dot, v, alpha, len;
    for (y = 0; y < h; y++) {
      ym = y > 0 ? y - 1 : 0;
      yp = y < h - 1 ? y + 1 : h - 1;
      sy = k / (yp - ym || 1);
      rm = ym * w; r0 = y * w; rp = yp * w;
      for (x = 0; x < w; x++) {
        i = r0 + x;
        o = i * 4;
        c = elev[i];
        if (!(c > NODATA)) { out[o] = out[o + 1] = out[o + 2] = out[o + 3] = 0; continue; }
        xm = x > 0 ? x - 1 : 0;
        xp = x < w - 1 ? x + 1 : w - 1;
        sx = k / (xp - xm || 1);
        // 3x3 window, no-data neighbours replaced by the centre so coasts
        // don't read as cliffs.
        a = elev[rm + xm]; if (!(a > NODATA)) a = c;
        b = elev[rm + x];  if (!(b > NODATA)) b = c;
        cc = elev[rm + xp]; if (!(cc > NODATA)) cc = c;
        d = elev[r0 + xm]; if (!(d > NODATA)) d = c;
        f = elev[r0 + xp]; if (!(f > NODATA)) f = c;
        g = elev[rp + xm]; if (!(g > NODATA)) g = c;
        hh = elev[rp + x]; if (!(hh > NODATA)) hh = c;
        ii = elev[rp + xp]; if (!(ii > NODATA)) ii = c;
        dzdx = ((cc + 2 * f + ii) - (a + 2 * d + g)) * sx;   // rise to the east
        dzdy = ((a + 2 * b + cc) - (g + 2 * hh + ii)) * sy;  // rise to the north
        // Surface normal (-dzdx, -dzdy, 1) against the light, minus flat.
        len = sqrt(dzdx * dzdx + dzdy * dzdy + 1);
        dot = (LZ - LX * dzdx - LY * dzdy) / len - LZ;
        v = dot < 0 ? -dot : dot;
        alpha = 255 - 255 * exp(-GAIN * v);
        v = dot < 0 ? 0 : 255;
        out[o] = out[o + 1] = out[o + 2] = v;
        out[o + 3] = alpha;
      }
    }
    return out;
  }

  window.MC_hillshadeShade = hillshadeShade;

  if (typeof L === 'undefined') return;

  var VALID_SIZES = { 256: true, 512: true };

  function tileCentreLat(coords) {
    var n = Math.PI * (1 - 2 * (coords.y + 0.5) / Math.pow(2, coords.z));
    return Math.atan(0.5 * (Math.exp(n) - Math.exp(-n))) / DEG;
  }

  var HillshadeLayer = L.GridLayer.extend({
    options: {
      opacity: 0.35,
      maxZoom: 19,
      maxNativeZoom: 15,
      zIndex: 250,
    },

    initialize: function (url, options) {
      this._url = String(url || '');
      L.setOptions(this, options);
      this._failed = false;
      this._scratch = null;   // { size, canvas, ctx, elev, out } reused per layer
      this.on('tileunload', this._onTileUnload, this);
    },

    getTileUrl: function (coords) {
      return this._url.replace(/\{([zxy])\}/g, function (_, key) { return String(coords[key]); });
    },

    createTile: function (coords, done) {
      var tile = document.createElement('canvas');
      var size = this.getTileSize();
      tile.width = size.x;
      tile.height = size.y;
      if (this._failed) {
        // Leaflet registers the tile only after createTile returns.
        setTimeout(function () { done(null, tile); }, 0);
        return tile;
      }
      var self = this;
      var img = new Image();
      img.crossOrigin = 'anonymous';   // S6: before src
      tile._mcImg = img;
      img.onload = function () {
        img.onload = img.onerror = null;
        tile._mcImg = null;
        self._shadeInto(tile, img, coords);
        done(null, tile);
      };
      img.onerror = function () {
        img.onload = img.onerror = null;
        tile._mcImg = null;
        done(null, tile);              // blank tile, no console noise
      };
      img.src = this.getTileUrl(coords);
      return tile;
    },

    _scratchFor: function (n) {
      var s = this._scratch;
      if (s && s.size === n) return s;
      var canvas = document.createElement('canvas');
      canvas.width = canvas.height = n;
      var ctx = canvas.getContext('2d', { willReadFrequently: true });
      s = this._scratch = {
        size: n, canvas: canvas, ctx: ctx,
        elev: new Float32Array(n * n),
        out: ctx.createImageData(n, n),
      };
      return s;
    },

    _shadeInto: function (tile, img, coords) {
      if (this._failed) return;
      var n = img.naturalWidth;
      if (!VALID_SIZES[n] || img.naturalHeight !== n) return;
      var s = this._scratchFor(n);
      var px;
      s.ctx.clearRect(0, 0, n, n);
      s.ctx.drawImage(img, 0, 0);
      try {
        px = s.ctx.getImageData(0, 0, n, n).data;
      } catch (e) {
        // Tainted canvas: the tile host sent no CORS header. Nothing will decode.
        this._failed = true;
        console.warn('[hillshade] elevation tiles cannot be read (no CORS on the tile host); hillshade disabled.');
        return;
      }
      var elev = s.elev;
      for (var i = 0, j = 0, len = n * n; i < len; i++, j += 4) {
        elev[i] = px[j] * 256 + px[j + 1] + px[j + 2] / 256 - 32768;
      }
      hillshadeShade(elev, n, n, coords.z, tileCentreLat(coords), s.out.data);
      tile.width = tile.height = n;    // CSS size stays the tile size
      tile.getContext('2d').putImageData(s.out, 0, 0);
    },

    _onTileUnload: function (e) {
      var tile = e && e.tile;
      var img = tile && tile._mcImg;
      if (!img) return;
      tile._mcImg = null;
      img.onload = img.onerror = null;  // never draw into a removed canvas
      img.src = '';                     // cancel the download
    },
  });

  window.MC_createHillshadeLayer = function (opts) {
    opts = opts || {};
    var options = {};
    for (var key in opts) {
      if (Object.prototype.hasOwnProperty.call(opts, key) && key !== 'url' && opts[key] !== undefined) {
        options[key] = opts[key];
      }
    }
    return new HillshadeLayer(opts.url, options);
  };
})();
