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
