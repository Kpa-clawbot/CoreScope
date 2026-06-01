import { test, expect } from '@playwright/test';

test.describe('Canvas Animations A11y', () => {
  test('Canvas pulse highlight ring maintains minimum a11y weight of 2', async ({ page }) => {
    // Navigate to the live map 
    await page.goto('/#/live');

    // Wait for the map and our test seams to initialize
    await page.waitForFunction(() => window._liveTestSeams !== undefined);

    // Evaluate the pulse lifecycle inside the browser context
    const minWeightSeen = await page.evaluate(async () => {
      return new Promise((resolve) => {
        const seams = window._liveTestSeams;
        
        // Trigger a synthetic pulse at an arbitrary coordinate
        seams.triggerPulse('a11y-test-node', [38.8951, -77.0364], 'ADVERT');
        
        let minSeenWeight = Infinity;

        // Monitor the pulse frame-by-frame
        function checkFrame() {
          const pulses = seams.getPulses();
          
          // If the array is empty, the pulse finished its lifecycle
          if (pulses.length === 0) {
            resolve(minSeenWeight);
            return;
          }

          const p = pulses[0];
          
          // Only assert weight while the highlight ring is actually visible
          if (p.hl_op > 0) {
            if (p.hl_weight < minSeenWeight) {
              minSeenWeight = p.hl_weight;
            }
          }
          
          requestAnimationFrame(checkFrame);
        }
        
        // Start the monitoring loop
        requestAnimationFrame(checkFrame);
      });
    });

    // Assert that at no point during the visible lifecycle did the stroke weight drop below 2
    expect(minWeightSeen).toBeGreaterThanOrEqual(2);
  });
});