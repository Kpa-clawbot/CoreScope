#!/usr/bin/env node
/* Issue #1890 — index.html must not hardcode one instance's URL in its
 * Open Graph tags.
 *
 * `<meta property="og:url" content="https://analyzer.00id.net">` shipped to
 * every self-hosted CoreScope. Facebook, Messenger and other OG consumers treat
 * og:url as the canonical destination, so clicking a shared preview from ANY
 * instance navigated to that one host instead of the instance the link came
 * from.
 *
 * og:url, og:image and twitter:image now carry the __CORESCOPE_URL__
 * placeholder. og:url is sent only with CORESCOPE_URL set; without it the
 * server drops the tag and fills the images with the request's own origin
 * (cmd/server/site_url.go, tested in site_url_test.go). build.sh does the
 * same at packaging time, with the GitHub image. No host is hardcoded.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');

const fs = require('fs');
const path = require('path');
const assert = require('assert');

const INDEX = path.resolve(REPO_ROOT, 'public', 'index.html');
const html = fs.readFileSync(INDEX, 'utf8');

let passed = 0, failed = 0;
function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`  ✅ ${name}`);
  } catch (e) {
    failed++;
    console.log(`  ❌ ${name}: ${e.message}`);
  }
}

console.log('\n=== #1890: index.html carries no instance-specific canonical URL ===');

test('og:url is the instance placeholder, not a hardcoded host', () => {
  const m = html.match(/<meta[^>]*property=["']og:url["'][^>]*>/i);
  assert.ok(m, 'og:url missing');
  assert.ok(/content="__CORESCOPE_URL__\/"/.test(m[0]), `og:url must be the placeholder; found: ${m[0]}`);
});

test('og:image and twitter:image are on the instance, not a hardcoded host', () => {
  for (const re of [/<meta[^>]*property=["']og:image["'][^>]*>/i, /<meta[^>]*name=["']twitter:image["'][^>]*>/i]) {
    const m = html.match(re);
    assert.ok(m && /content="__CORESCOPE_URL__\/og-image\.png"/.test(m[0]), `found: ${m && m[0]}`);
  }
  assert.ok(!/raw\.githubusercontent\.com/i.test(html), 'index.html still links raw.githubusercontent.com');
  assert.ok(fs.existsSync(path.resolve(REPO_ROOT, 'public', 'og-image.png')), 'public/og-image.png missing');
});

test('every absolute URL in a meta tag goes through the placeholder', () => {
  const abs = (html.match(/<meta[^>]*content=["']https?:\/\/[^"']*["'][^>]*>/gi) || []);
  assert.deepStrictEqual(abs, [], 'hardcoded absolute URLs in meta tags');
});

test('no rel="canonical" pinning a single instance', () => {
  const m = html.match(/<link[^>]*rel=["']canonical["'][^>]*>/i);
  assert.ok(!m, `canonical link must not be hardcoded; found: ${m && m[0]}`);
});

test('the analyzer.00id.net host appears nowhere in index.html', () => {
  assert.ok(!/00id\.net/i.test(html), 'index.html still references 00id.net');
});

test('og:title and og:description are still present', () => {
  // The fix removes one tag, not the embed. Guard against over-deletion.
  assert.ok(/property=["']og:title["']/i.test(html), 'og:title missing');
  assert.ok(/property=["']og:description["']/i.test(html), 'og:description missing');
  assert.ok(/property=["']og:image["']/i.test(html), 'og:image missing');
});

console.log(`\n  #1890 og:url: ${passed} passed, ${failed} failed\n`);
process.exit(failed ? 1 : 0);
