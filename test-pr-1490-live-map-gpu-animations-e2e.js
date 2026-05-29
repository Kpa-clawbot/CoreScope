const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});const { test, expect } = require('@playwright/test');

test.describe('Live Map Canvas Animation Engine', () => {
  test('canvas initializes and animations drain correctly', async ({ page }) => {
    // 1. Load the map route
    await page.goto('/#/map');

    // Ensure the map container has loaded
    const mapContainer = page.locator('#liveMap');
    await expect(mapContainer).toBeVisible();

    // 2. Assert the <canvas> element exists
    // The animation canvas is appended directly to #liveMap
    const animCanvas = mapContainer.locator('canvas').first();
    await expect(animCanvas).toBeAttached();

    // 3. Fire synthetic packets
    const packetCount = 5;
    await page.evaluate((count) => {
      // Ensure the VCR speed is at standard 1x for predictable timing
      if (window._liveVcrSetMode) window._liveVcrSetMode('LIVE');
      
      for (let i = 0; i < count; i++) {
        window._liveDrawAnimatedLine(
          [37.4, -122.0], 
          [37.5, -122.1], 
          '#00ff00', 
          null, 
          null, 
          '00AA', 
          'test-hash-' + i
        );
      }
    }, packetCount);

    // Verify the packets successfully pushed to the active array
    let initialAnimCount = await page.evaluate(() => window._liveTestSeams.getAnimCount());
    expect(initialAnimCount).toBe(packetCount);

    // Verify the engine woke up
    let isAwake = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isAwake).toBe(true);

    // 4. Assert activeAnimations drains within 2x duration
    // Base duration is 660ms at 1x speed. 2x duration = 1320ms.
    // We add a tiny buffer (1500ms total) for Playwright/Browser overhead.
    await expect.poll(async () => {
      return await page.evaluate(() => window._liveTestSeams.getAnimCount());
    }, {
      message: 'activeAnimations did not drain to 0 within 2x duration',
      timeout: 1500, 
    }).toBe(0);

    // 5. Assert the engine gracefully went back to sleep
    let isSleeping = await page.evaluate(() => window._liveTestSeams.isAnimating());
    expect(isSleeping).toBe(false);

    // 6. Assert recent paths didn't blow past the limit
    let recentPathsCount = await page.evaluate(() => window._liveTestSeams.getPathCount());
    expect(recentPathsCount).toBeLessThanOrEqual(5);
  });
});
