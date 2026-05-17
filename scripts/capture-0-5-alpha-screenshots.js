#!/usr/bin/env node
'use strict';

const fs = require('fs/promises');
const path = require('path');
const { chromium } = require('playwright');

const ROOT = path.resolve(__dirname, '..');
const OUT_DIR = path.join(ROOT, 'docs', 'screenshots');
const BASE_URL = process.env.BASE_URL || 'http://localhost:3330';
const ONTARIO_CORRIDOR_VIEW = { lat: 44.55, lng: -77.55, zoom: 7 };

const shots = [
  { name: 'home', hash: '#/home', viewport: { width: 1440, height: 960 } },
  {
    name: 'map',
    hash: `#/map?lat=${ONTARIO_CORRIDOR_VIEW.lat}&lon=${ONTARIO_CORRIDOR_VIEW.lng}&zoom=${ONTARIO_CORRIDOR_VIEW.zoom}`,
    viewport: { width: 1440, height: 960 },
    mapView: ONTARIO_CORRIDOR_VIEW,
  },
  { name: 'live', hash: '#/live', viewport: { width: 1440, height: 960 }, mapView: ONTARIO_CORRIDOR_VIEW },
  { name: 'mobile', hash: '#/home', viewport: { width: 390, height: 844 }, mobile: true },
];

async function waitForApp(page) {
  await page.waitForLoadState('domcontentloaded');
  await page.waitForFunction(() => Boolean(window.UIIcon && document.querySelector('#app')), null, { timeout: 15000 });
  await page.waitForTimeout(1200);
}

async function waitForLeafletTiles(page, rootSelector) {
  await page.waitForFunction((selector) => {
    const tiles = window.__captureVisibleTiles
      ? window.__captureVisibleTiles(selector)
      : Array.from((document.querySelector(selector) || document).querySelectorAll('img.leaflet-tile'));
    if (tiles.length < 8) return false;
    return tiles.every((img) => img.complete && img.naturalWidth > 0 && !img.classList.contains('leaflet-tile-loading'));
  }, rootSelector, { timeout: 45000 });

  // Leaflet fades tiles in after image load. Waiting for opacity avoids
  // capturing pale tile placeholders or mixed light/dark tile states.
  await page.waitForFunction((selector) => {
    const tiles = window.__captureVisibleTiles(selector);
    return tiles.length >= 8 && tiles.every((img) => Number(getComputedStyle(img).opacity || '1') >= 0.95);
  }, rootSelector, { timeout: 15000 }).catch(() => {});
}

async function waitForShotReady(page, shot) {
  await page.evaluate(() => {
    window.__captureVisibleTiles = (selector) => {
      const root = document.querySelector(selector);
      if (!root) return [];
      return Array.from(root.querySelectorAll('img.leaflet-tile')).filter((img) => {
        const rect = img.getBoundingClientRect();
        const style = getComputedStyle(img);
        return rect.width > 0
          && rect.height > 0
          && rect.right > 0
          && rect.bottom > 0
          && rect.left < window.innerWidth
          && rect.top < window.innerHeight
          && style.display !== 'none'
          && style.visibility !== 'hidden';
      });
    };
  });

  if (shot.name === 'map') {
    await page.waitForSelector('#leaflet-map[data-loaded="true"]', { timeout: 45000 });
    await page.waitForFunction(() => {
      const rolesReady = document.querySelectorAll('#mcRoleChecks label').length >= 5;
      const jumpsReady = (document.querySelector('#mcJumps')?.textContent || '').includes('YOW')
        && (document.querySelector('#mcJumps')?.textContent || '').includes('YYZ');
      const markerCount = document.querySelectorAll('.mc-cluster-wrap, .leaflet-marker-icon').length;
      return rolesReady && jumpsReady && markerCount > 0;
    }, null, { timeout: 45000 });
    await waitForLeafletTiles(page, '#leaflet-map');
    await page.waitForTimeout(2500);
    return;
  }

  if (shot.name === 'live') {
    await page.waitForSelector('#liveMap .leaflet-container, #liveMap.leaflet-container', { timeout: 45000 }).catch(() => {});
    await page.waitForFunction(() => {
      const nodeCount = Number(document.querySelector('#liveNodeCount')?.textContent || '0');
      const markers = document.querySelectorAll('#liveMap path.leaflet-interactive').length;
      return nodeCount > 0 && markers > 10;
    }, null, { timeout: 45000 });
    await waitForLeafletTiles(page, '#liveMap');
    await page.waitForTimeout(3000);
    return;
  }

  await page.waitForTimeout(800);
}

async function prepare(page, shot) {
  await page.addInitScript(() => {
    localStorage.setItem('meshcore-user-level', 'experienced');
    localStorage.setItem('meshcore-theme', 'dark');
    localStorage.setItem('meshcore-live-heatmap', 'false');
    localStorage.setItem('live-show-suspicious-links', 'false');
    localStorage.setItem('meshcore-color-packets-by-hash', 'true');
    localStorage.setItem('live-realistic-propagation', 'true');
  });
  if (shot.mapView) {
    await page.addInitScript((view) => {
      localStorage.setItem('map-view', JSON.stringify({ lat: view.lat, lng: view.lng, zoom: view.zoom }));
      localStorage.setItem('live-map-view', JSON.stringify({ lat: view.lat, lng: view.lng, zoom: view.zoom }));
    }, shot.mapView);
  }
}

async function capture() {
  await fs.mkdir(OUT_DIR, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  const failures = [];
  try {
    for (const shot of shots) {
      const context = await browser.newContext({
        viewport: shot.viewport,
        deviceScaleFactor: 1,
        isMobile: Boolean(shot.mobile),
      });
      const page = await context.newPage();
      const pageErrors = [];
      page.on('console', msg => {
        if (msg.type() === 'error') pageErrors.push(msg.text());
      });
      page.on('pageerror', err => pageErrors.push(err.message));
      await prepare(page, shot);
      await page.goto(`${BASE_URL}/?capture=${Date.now()}${shot.hash}`, { waitUntil: 'domcontentloaded', timeout: 30000 });
      await waitForApp(page);
      await waitForShotReady(page, shot);

      const overflow = await page.evaluate(() => {
        const doc = document.documentElement;
        return {
          width: doc.scrollWidth,
          viewport: window.innerWidth,
          overflowing: doc.scrollWidth > window.innerWidth + 2,
        };
      });
      if (overflow.overflowing) {
        failures.push(`${shot.name}: horizontal overflow ${overflow.width}px > ${overflow.viewport}px`);
      }
      if (pageErrors.length) {
        failures.push(`${shot.name}: console/page errors: ${pageErrors.slice(0, 3).join(' | ')}`);
      }

      await page.screenshot({
        path: path.join(OUT_DIR, `0.5-alpha-${shot.name}.png`),
        fullPage: false,
      });
      await context.close();
    }
  } finally {
    await browser.close();
  }

  if (failures.length) {
    console.error(failures.join('\n'));
    process.exit(1);
  }
  console.log(`Captured ${shots.length} screenshots in ${path.relative(ROOT, OUT_DIR)}`);
}

capture().catch(err => {
  console.error(err);
  process.exit(1);
});
