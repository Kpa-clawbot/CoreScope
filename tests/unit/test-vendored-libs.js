/**
 * The pages load their third-party libraries (Leaflet, leaflet.heat,
 * Chart.js) from public/vendor/, not from a CDN: no request to a third party
 * on every page view, and maps and charts keep working where the CDN is
 * blocked or down. This pins both halves:
 *  - no <script src> or <link href> in the served pages points off-site
 *  - every vendored file carrying an integrity attribute matches it, so a
 *    re-download or upgrade that changes the bytes fails here, not in a
 *    browser that refuses to run the script.
 *
 * Usage: node tests/unit/test-vendored-libs.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const PUBLIC = path.join(__dirname, '..', '..', 'public');
const PAGES = ['index.html', 'area-map.html', 'geofilter-builder.html'];
let pass = 0, fail = 0;

function test(name, fn) {
  try { fn(); pass++; console.log('  ✅ ' + name); } catch (e) { fail++; console.log('  ❌ ' + name + ': ' + e.message); }
}

for (const page of PAGES) {
  const html = fs.readFileSync(path.join(PUBLIC, page), 'utf8');
  const tags = html.match(/<(script|link)\b[^>]*>/g) || [];
  test(page + ': no script or stylesheet from another origin', () => {
    const offsite = tags.filter((t) => /\b(src|href)="(https?:)?\/\//.test(t) && (/<script/.test(t) || /rel="stylesheet"/.test(t)));
    if (offsite.length) throw new Error(offsite.join(' | '));
  });
  for (const tag of tags) {
    const ref = (tag.match(/\b(?:src|href)="([^"?]+)/) || [])[1];
    const sri = (tag.match(/\bintegrity="([^"]+)"/) || [])[1];
    if (!ref || !sri) continue;
    test(page + ': ' + ref + ' matches its integrity hash', () => {
      const [alg, want] = sri.split(/-(.+)/);
      const got = crypto.createHash(alg).update(fs.readFileSync(path.join(PUBLIC, ref))).digest('base64');
      if (got !== want) throw new Error(alg + ' is ' + got + ', integrity says ' + want);
    });
  }
}

test('Leaflet images referenced by its stylesheet are vendored', () => {
  const dir = path.join(PUBLIC, 'vendor', 'leaflet-1.9.4');
  const css = fs.readFileSync(path.join(dir, 'leaflet.css'), 'utf8');
  const missing = [...css.matchAll(/url\((images\/[^)]+)\)/g)].map((m) => m[1])
    .filter((f) => !fs.existsSync(path.join(dir, f)));
  if (missing.length) throw new Error('missing ' + missing.join(', '));
});

console.log(`\nVendored libraries: ${pass} passed, ${fail} failed`);
process.exit(fail > 0 ? 1 : 0);
