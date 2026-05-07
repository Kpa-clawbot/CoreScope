/* === CoreScope — home.js (My Mesh Dashboard) === */
'use strict';

(function () {
  let searchTimeout = null;
  let miniMap = null;
  let searchAbort = null; // AbortController for document-level listeners

  const PREF_KEY = 'meshcore-user-level';
  const MY_NODES_KEY = 'meshcore-my-nodes'; // [{pubkey, name, addedAt}]

  function getMyNodes() {
    try { return JSON.parse(localStorage.getItem(MY_NODES_KEY)) || []; } catch { return []; }
  }
  function saveMyNodes(nodes) { localStorage.setItem(MY_NODES_KEY, JSON.stringify(nodes)); }
  function addMyNode(pubkey, name) {
    const nodes = getMyNodes();
    if (!nodes.find(n => n.pubkey === pubkey)) {
      nodes.push({ pubkey, name: name || pubkey.slice(0, 12), addedAt: new Date().toISOString() });
      saveMyNodes(nodes);
    }
  }
  function removeMyNode(pubkey) {
    saveMyNodes(getMyNodes().filter(n => n.pubkey !== pubkey));
  }
  function isMyNode(pubkey) { return getMyNodes().some(n => n.pubkey === pubkey); }

  function isExperienced() { return localStorage.getItem(PREF_KEY) === 'experienced'; }
  function setLevel(level) { localStorage.setItem(PREF_KEY, level); }

  function init(container) {
    if (!localStorage.getItem(PREF_KEY)) {
      showChooser(container);
      return;
    }
    renderHome(container);
  }

  function showChooser(container) {
    container.innerHTML = `
      <section class="home-chooser">
        <h1>Welcome to ${escapeHtml(window.SITE_CONFIG?.branding?.siteName || 'CoreScope')}</h1>
        <p>How familiar are you with MeshCore?</p>
        <div class="chooser-options">
          <button class="chooser-btn new" id="chooseNew">
            <span class="chooser-icon">🌱</span>
            <strong>I\u2019m new</strong>
            <span>Show me setup guides and tips</span>
          </button>
          <button class="chooser-btn exp" id="chooseExp">
            <span class="chooser-icon">⚡</span>
            <strong>I know what I\u2019m doing</strong>
            <span>Just the analyzer, skip the guides</span>
          </button>
        </div>
      </section>`;
    document.getElementById('chooseNew').addEventListener('click', () => { setLevel('new'); renderHome(container); });
    document.getElementById('chooseExp').addEventListener('click', () => { setLevel('experienced'); renderHome(container); });
  }

  function renderHome(container) {
    const exp = isExperienced();
    const myNodes = getMyNodes();
    const hasNodes = myNodes.length > 0;
    const homeCfg = window.SITE_CONFIG?.home || null;
    const siteName = window.SITE_CONFIG?.branding?.siteName || 'CoreScope';

    container.innerHTML = `
      <section class="home-hero">
        <svg class="home-hero-logo" xmlns="http://www.w3.org/2000/svg" width="1200" height="300" viewBox="0 0 1200 300" aria-hidden="true" focusable="false"><path d="M540 100 A 30 30 0 1 0 540 160" fill="none" stroke="var(--logo-brand-fog, #cfd9c9)" stroke-width="8" opacity="1.00"/>
<path d="M540 73 A 57 57 0 1 0 540 187" fill="none" stroke="var(--logo-brand-fog, #cfd9c9)" stroke-width="8" opacity="0.82"/>
<path d="M540 46 A 84 84 0 1 0 540 214" fill="none" stroke="var(--logo-brand-fog, #cfd9c9)" stroke-width="8" opacity="0.64"/>
<path d="M540 19 A 111 111 0 1 0 540 241" fill="none" stroke="var(--logo-brand-fog, #cfd9c9)" stroke-width="8" opacity="0.46"/>
<path d="M660 100 A 30 30 0 1 1 660 160" fill="none" stroke="var(--logo-brand-teal, #2c8c8c)" stroke-width="8" opacity="1.00"/>
<path d="M660 73 A 57 57 0 1 1 660 187" fill="none" stroke="var(--logo-brand-teal, #2c8c8c)" stroke-width="8" opacity="0.82"/>
<path d="M660 46 A 84 84 0 1 1 660 214" fill="none" stroke="var(--logo-brand-teal, #2c8c8c)" stroke-width="8" opacity="0.64"/>
<path d="M660 19 A 111 111 0 1 1 660 241" fill="none" stroke="var(--logo-brand-teal, #2c8c8c)" stroke-width="8" opacity="0.46"/>

<polyline points="540.00,130.00 540.30,130.17 540.60,130.35 540.90,130.53 541.20,130.71 541.50,130.89 541.80,131.07 542.10,131.26 542.40,131.45 542.70,131.64 543.00,131.83 543.30,132.02 543.60,132.21 543.90,132.41 544.20,132.61 544.50,132.80 544.80,133.00 545.10,133.20 545.40,133.40 545.70,133.60 546.00,133.81 546.30,134.01 546.60,134.21 546.90,134.42 547.20,134.62 547.50,134.82 547.80,135.03 548.10,135.23 548.40,135.44 548.70,135.64 549.00,135.84 549.30,136.04 549.60,136.24 549.90,136.44 550.20,136.64 550.50,136.83 550.80,137.02 551.10,137.22 551.40,137.41 551.70,137.59 552.00,137.78 552.30,137.96 552.60,138.14 552.90,138.31 553.20,138.49 553.50,138.66 553.80,138.82 554.10,138.98 554.40,139.14 554.70,139.29 555.00,139.44 555.30,139.58 555.60,139.71 555.90,139.84 556.20,139.97 556.50,140.09 556.80,140.20 557.10,140.31 557.40,140.41 557.70,140.50 558.00,140.59 558.30,140.67 558.60,140.74 558.90,140.80 559.20,140.85 559.50,140.90 559.80,140.94 560.10,140.97 560.40,140.99 560.70,141.00 561.00,141.00 561.30,140.99 561.60,140.97 561.90,140.95 562.20,140.91 562.50,140.86 562.80,140.80 563.10,140.73 563.40,140.65 563.70,140.56 564.00,140.46 564.30,140.35 564.60,140.23 564.90,140.09 565.20,139.95 565.50,139.79 565.80,139.62 566.10,139.44 566.40,139.25 566.70,139.05 567.00,138.84 567.30,138.61 567.60,138.38 567.90,138.13 568.20,137.87 568.50,137.60 568.80,137.33 569.10,137.04 569.40,136.74 569.70,136.43 570.00,136.11 570.30,135.78 570.60,135.45 570.90,135.10 571.20,134.75 571.50,134.38 571.80,134.01 572.10,133.64 572.40,133.25 572.70,132.86 573.00,132.46 573.30,132.06 573.60,131.65 573.90,131.24 574.20,130.82 574.50,130.40 574.80,129.98 575.10,129.56 575.40,129.13 575.70,128.71 576.00,128.28 576.30,127.85 576.60,127.43 576.90,127.00 577.20,126.58 577.50,126.17 577.80,125.75 578.10,125.35 578.40,124.94 578.70,124.55 579.00,124.16 579.30,123.78 579.60,123.41 579.90,123.05 580.20,122.70 580.50,122.36 580.80,122.03 581.10,121.71 581.40,121.41 581.70,121.13 582.00,120.85 582.30,120.60 582.60,120.36 582.90,120.14 583.20,119.93 583.50,119.75 583.80,119.58 584.10,119.43 584.40,119.31 584.70,119.20 585.00,119.12 585.30,119.06 585.60,119.02 585.90,119.00 586.20,119.01 586.50,119.04 586.80,119.09 587.10,119.17 587.40,119.27 587.70,119.39 588.00,119.54 588.30,119.71 588.60,119.91 588.90,120.13 589.20,120.37 589.50,120.64 589.80,120.92 590.10,121.24 590.40,121.57 590.70,121.92 591.00,122.30 591.30,122.69 591.60,123.11 591.90,123.54 592.20,123.99 592.50,124.46 592.80,124.94 593.10,125.44 593.40,125.95 593.70,126.48 594.00,127.01 594.30,127.56 594.60,128.11 594.90,128.67 595.20,129.24 595.50,129.81 595.80,130.38 596.10,130.96 596.40,131.53 596.70,132.10 597.00,132.67 597.30,133.24 597.60,133.79 597.90,134.34 598.20,134.87 598.50,135.40 598.80,135.91 599.10,136.40 599.40,136.88 599.70,137.34 600.00,137.78 600.30,138.19 600.60,138.59 600.90,138.96 601.20,139.30 601.50,139.61 601.80,139.90 602.10,140.15 602.40,140.37 602.70,140.56 603.00,140.72 603.30,140.84 603.60,140.93 603.90,140.98 604.20,141.00 604.50,140.98 604.80,140.92 605.10,140.83 605.40,140.70 605.70,140.53 606.00,140.32 606.30,140.08 606.60,139.80 606.90,139.49 607.20,139.14 607.50,138.75 607.80,138.34 608.10,137.89 608.40,137.42 608.70,136.91 609.00,136.38 609.30,135.82 609.60,135.24 609.90,134.64 610.20,134.01 610.50,133.37 610.80,132.72 611.10,132.05 611.40,131.37 611.70,130.69 612.00,130.00 612.30,129.31 612.60,128.62 612.90,127.93 613.20,127.25 613.50,126.58 613.80,125.91 614.10,125.27 614.40,124.64 614.70,124.03 615.00,123.45 615.30,122.89 615.60,122.36 615.90,121.86 616.20,121.39 616.50,120.96 616.80,120.57 617.10,120.21 617.40,119.90 617.70,119.64 618.00,119.41 618.30,119.24 618.60,119.11 618.90,119.03 619.20,119.00 619.50,119.02 619.80,119.09 620.10,119.21 620.40,119.39 620.70,119.61 621.00,119.88 621.30,120.20 621.60,120.57 621.90,120.98 622.20,121.44 622.50,121.94 622.80,122.48 623.10,123.06 623.40,123.68 623.70,124.33 624.00,125.01 624.30,125.71 624.60,126.44 624.90,127.19 625.20,127.96 625.50,128.73 625.80,129.52 626.10,130.31 626.40,131.10 626.70,131.89 627.00,132.67 627.30,133.44 627.60,134.19 627.90,134.93 628.20,135.63 628.50,136.31 628.80,136.96 629.10,137.57 629.40,138.14 629.70,138.67 630.00,139.15 630.30,139.58 630.60,139.95 630.90,140.28 631.20,140.54 631.50,140.75 631.80,140.90 632.10,140.98 632.40,141.00 632.70,140.96 633.00,140.85 633.30,140.68 633.60,140.44 633.90,140.14 634.20,139.78 634.50,139.36 634.80,138.89 635.10,138.36 635.40,137.78 635.70,137.14 636.00,136.47 636.30,135.75 636.60,134.99 636.90,134.20 637.20,133.38 637.50,132.54 637.80,131.68 638.10,130.81 638.40,129.93 638.70,129.05 639.00,128.17 639.30,127.31 639.60,126.45 639.90,125.62 640.20,124.82 640.50,124.05 640.80,123.31 641.10,122.62 641.40,121.98 641.70,121.39 642.00,120.85 642.30,120.38 642.60,119.97 642.90,119.63 643.20,119.36 643.50,119.17 643.80,119.05 644.10,119.00 644.40,119.03 644.70,119.14 645.00,119.33 645.30,119.59 645.60,119.93 645.90,120.34 646.20,120.83 646.50,121.38 646.80,121.99 647.10,122.67 647.40,123.40 647.70,124.18 648.00,125.01 648.30,125.87 648.60,126.77 648.90,127.69 649.20,128.64 649.50,129.60 649.80,130.56 650.10,131.52 650.40,132.47 650.70,133.40 651.00,134.31 651.30,135.19 651.60,136.02 651.90,136.82 652.20,137.56 652.50,138.24 652.80,138.86 653.10,139.41 653.40,139.88 653.70,140.27 654.00,140.59 654.30,140.81 654.60,140.95 654.90,141.00 655.20,140.96 655.50,140.82 655.80,140.60 656.10,140.29 656.40,139.89 656.70,139.40 657.00,138.84 657.30,138.19 657.60,137.48 657.90,136.70 658.20,135.86 658.50,134.97 658.80,134.03 659.10,133.06 659.40,132.06 659.70,131.03 660.00,130.00" fill="none" stroke="var(--logo-muted)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" opacity="1.0"/>
<circle cx="540" cy="130" r="13" fill="var(--logo-brand-fog, #cfd9c9)"/>
<circle cx="660" cy="130" r="13" fill="var(--logo-brand-teal, #2c8c8c)"/>
<text x="426.20" y="161.68" font-family="Aldrich, monospace" font-size="88" text-anchor="end" dominant-baseline="alphabetic" font-weight="700" letter-spacing="4.4" fill="var(--logo-brand-fog, #cfd9c9)">CORE</text><text x="773.80" y="161.68" font-family="Aldrich, monospace" font-size="88" text-anchor="start" dominant-baseline="alphabetic" font-weight="700" letter-spacing="4.4" fill="var(--logo-brand-teal, #2c8c8c)">SCOPE</text><text x="604.00" y="279.20" font-family="Aldrich, monospace" font-size="20" text-anchor="middle" dominant-baseline="alphabetic" font-weight="500" letter-spacing="8" fill="var(--logo-muted)">MESH ANALYZER</text></svg>
        <h1>${hasNodes ? 'My Mesh' : escapeHtml(homeCfg?.heroTitle || siteName)}</h1>
        <p>${hasNodes ? 'Your nodes at a glance. Add more by searching below.' : escapeHtml(homeCfg?.heroSubtitle || 'Find your nodes to start monitoring them.')}</p>
        <div class="home-search-wrap">
          <input type="text" id="homeSearch" placeholder="Search by node name or public key…" autocomplete="off" aria-label="Search nodes" role="combobox" aria-expanded="false" aria-owns="homeSuggest" aria-autocomplete="list" aria-activedescendant="">
          <div class="home-suggest" id="homeSuggest" role="listbox"></div>
        </div>
      </section>

      ${hasNodes ? '<div class="my-nodes-grid" id="myNodesGrid"><div class="my-nodes-loading">Loading your nodes…</div></div>' : '<div class="my-nodes-grid" id="myNodesGrid"></div>'}

      ${!hasNodes ? `
        <div class="onboarding-prompt">
          <div class="onboard-icon">📡</div>
          <h2>Claim your first node</h2>
          <p>Search for your node above, or paste your public key. Once claimed, you'll see live status, signal quality, and who's hearing you.</p>
        </div>
      ` : ''}

      <div class="home-detail-area">
        <div class="home-health" id="homeHealth"></div>
        <div class="home-journey" id="homeJourney"></div>
      </div>

      <div class="home-stats" id="homeStats"></div>

      ${exp ? '' : `
      <section class="home-checklist">
        <h2>🚀 Getting on the mesh${homeCfg?.steps ? '' : ' — SF Bay Area'}</h2>
        ${checklist(homeCfg)}
      </section>`}

      <section class="home-footer">
        <div class="home-footer-links">
          ${homeCfg?.footerLinks ? homeCfg.footerLinks.map(l => `<a href="${escapeAttr(l.url)}" class="home-footer-link" target="_blank" rel="noopener">${escapeHtml(l.label)}</a>`).join('') : `
          <a href="#/packets" class="home-footer-link">📦 Packets</a>
          <a href="#/map" class="home-footer-link">🗺️ Network Map</a>
          <a href="#/live" class="home-footer-link">🔴 Live</a>
          <a href="#/nodes" class="home-footer-link">📡 All Nodes</a>
          <a href="#/channels" class="home-footer-link">💬 Channels</a>`}
        </div>
        <div class="home-level-toggle">
          <small>${exp ? 'Want setup guides? ' : 'Already know MeshCore? '}
          <a href="#" id="toggleLevel" style="color:var(--accent)">${exp ? 'Show new user tips' : 'Hide guides'}</a></small>
        </div>
      </section>
    `;

    document.getElementById('toggleLevel')?.addEventListener('click', (e) => {
      e.preventDefault();
      setLevel(exp ? 'new' : 'experienced');
      renderHome(container);
    });

    setupSearch(container);
    loadStats();
    if (hasNodes) loadMyNodes();

    // Checklist accordion
    container.querySelectorAll('.checklist-q').forEach(q => {
      const toggle = () => {
        const item = q.parentElement;
        item.classList.toggle('open');
        q.setAttribute('aria-expanded', item.classList.contains('open'));
      };
      q.addEventListener('click', toggle);
      q.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); }
      });
    });
  }

  function setupSearch(container) {
    const input = document.getElementById('homeSearch');
    const suggest = document.getElementById('homeSuggest');
    if (!input || !suggest) return;

    input.addEventListener('input', () => {
      clearTimeout(searchTimeout);
      const q = input.value.trim();
      if (!q) { suggest.classList.remove('open'); input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-activedescendant', ''); return; }
      searchTimeout = setTimeout(async () => {
        try {
          const data = await api('/nodes/search?q=' + encodeURIComponent(q), { ttl: CLIENT_TTL.nodeSearch });
          const nodes = data.nodes || [];
          if (!nodes.length) {
            suggest.innerHTML = '<div class="suggest-empty">No nodes found</div>';
          } else {
            suggest.innerHTML = nodes.slice(0, 10).map((n, idx) => {
              const claimed = isMyNode(n.public_key);
              return `<div class="suggest-item" role="option" id="suggest-${idx}" data-key="${n.public_key}" data-name="${escapeAttr(n.name || '')}">
                <div class="suggest-main">
                  <span class="suggest-name">${escapeHtml(n.name || 'Unknown')}</span>
                  <small class="suggest-key">${truncate(n.public_key, 16)}</small>
                </div>
                <div class="suggest-actions">
                  <span class="suggest-role badge-${n.role || 'unknown'}">${n.role || '?'}</span>
                  <button class="suggest-claim ${claimed ? 'claimed' : ''}" data-key="${n.public_key}" data-name="${escapeAttr(n.name || '')}" title="${claimed ? 'Remove from My Mesh' : 'Add to My Mesh'}">
                    ${claimed ? '✓ Mine' : '+ Claim'}
                  </button>
                </div>
              </div>`;
            }).join('');
          }
          suggest.classList.add('open');
          input.setAttribute('aria-expanded', 'true');
          input.setAttribute('aria-activedescendant', '');

          // Claim buttons
          suggest.querySelectorAll('.suggest-claim').forEach(btn => {
            btn.addEventListener('click', (e) => {
              e.stopPropagation();
              const pk = btn.dataset.key;
              const nm = btn.dataset.name;
              if (isMyNode(pk)) {
                removeMyNode(pk);
                btn.classList.remove('claimed');
                btn.textContent = '+ Claim';
              } else {
                addMyNode(pk, nm);
                btn.classList.add('claimed');
                btn.textContent = '✓ Mine';
              }
              loadMyNodes();
            });
          });
        } catch { suggest.classList.remove('open'); input.setAttribute('aria-expanded', 'false'); }
      }, 200);
    });

    suggest.addEventListener('click', (e) => {
      const item = e.target.closest('.suggest-item');
      if (!item || !item.dataset.key || e.target.closest('.suggest-claim')) return;
      suggest.classList.remove('open');
      input.setAttribute('aria-expanded', 'false');
      input.value = '';
      loadHealth(item.dataset.key);
    });

    // Use AbortController so re-calling setupSearch won't stack listeners
    if (searchAbort) searchAbort.abort();
    searchAbort = new AbortController();
    document.addEventListener('click', handleOutsideClick, { signal: searchAbort.signal });
  }

  function handleOutsideClick(e) {
    const suggest = document.getElementById('homeSuggest');
    const input = document.getElementById('homeSearch');
    if (suggest && !e.target.closest('.home-search-wrap')) {
      suggest.classList.remove('open');
      if (input) { input.setAttribute('aria-expanded', 'false'); input.setAttribute('aria-activedescendant', ''); }
    }
  }

  function destroy() {
    clearTimeout(searchTimeout);
    if (searchAbort) { searchAbort.abort(); searchAbort = null; }
    if (miniMap) { miniMap.remove(); miniMap = null; }
  }

  // ==================== MY NODES DASHBOARD ====================
  async function loadMyNodes() {
    const grid = document.getElementById('myNodesGrid');
    if (!grid) return;
    const myNodes = getMyNodes();

    // Update hero text dynamically
    const h1 = document.querySelector('.home-hero h1');
    const heroP = document.querySelector('.home-hero p');
    if (myNodes.length) {
      if (h1) h1.textContent = 'My Mesh';
      if (heroP) heroP.textContent = 'Your nodes at a glance. Add more by searching below.';
      // Hide onboarding prompt
      const onboard = document.querySelector('.onboarding-prompt');
      if (onboard) onboard.style.display = 'none';
    }

    if (!myNodes.length) {
      grid.innerHTML = '';
      return;
    }

    const cards = await Promise.all(myNodes.map(async (mn) => {
      try {
        const h = await api('/nodes/' + encodeURIComponent(mn.pubkey) + '/health', { ttl: CLIENT_TTL.nodeHealth });
        const node = h.node || {};
        const stats = h.stats || {};
        const obs = h.observers || [];

        const age = stats.lastHeard ? Date.now() - new Date(stats.lastHeard).getTime() : null;
        const status = age === null ? 'silent' : age < HEALTH_THRESHOLDS.nodeDegradedMs ? 'healthy' : age < HEALTH_THRESHOLDS.nodeSilentMs ? 'degraded' : 'silent';
        const statusDot = status === 'healthy' ? '🟢' : status === 'degraded' ? '🟡' : '🔴';
        const statusText = status === 'healthy' ? 'Active' : status === 'degraded' ? 'Degraded' : 'Silent';
        const name = node.name || mn.name || truncate(mn.pubkey, 12);

        // SNR quality label
        const snrVal = stats.avgSnr;
        const snrLabel = snrVal != null ? (snrVal > 10 ? 'Excellent' : snrVal > 0 ? 'Good' : snrVal > -5 ? 'Marginal' : 'Poor') : null;
        const snrColor = snrVal != null ? (snrVal > 10 ? 'var(--status-green)' : snrVal > 0 ? 'var(--accent)' : snrVal > -5 ? 'var(--status-yellow)' : 'var(--status-red)') : '#6b7280';

        // Build sparkline from recent packets (packet timestamps → hourly buckets)
        const sparkHtml = buildSparkline(h.recentPackets || []);

        return `<div class="my-node-card ${status}" data-key="${mn.pubkey}" tabindex="0" role="button">
          <div class="mnc-header">
            <div class="mnc-status">${statusDot}</div>
            <div class="mnc-name">${escapeHtml(name)}</div>
            <div class="mnc-role">${node.role || '?'}</div>
            <button class="mnc-remove" data-key="${mn.pubkey}" title="Remove from My Mesh" aria-label="Remove ${escapeAttr(name)} from My Mesh">✕</button>
          </div>
          <div class="mnc-status-text">${statusText}${stats.lastHeard ? ' · ' + timeAgo(stats.lastHeard) : ''}</div>
          <div class="mnc-metrics">
            <div class="mnc-metric">
              <div class="mnc-val">${stats.packetsToday ?? 0}</div>
              <div class="mnc-lbl">Packets today</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val">${obs.length}</div>
              <div class="mnc-lbl">Observers</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val" style="color:${snrColor}">${snrVal != null ? Number(snrVal).toFixed(1) + ' dB' : '—'}</div>
              <div class="mnc-lbl">SNR${snrLabel ? ' · ' + snrLabel : ''}</div>
            </div>
            <div class="mnc-metric">
              <div class="mnc-val">${stats.avgHops != null ? stats.avgHops.toFixed(1) : '—'}</div>
              <div class="mnc-lbl">Avg hops</div>
            </div>
          </div>
          ${obs.length ? `<div class="mnc-observers"><strong>Heard by:</strong> ${obs.map(o => escapeHtml(o.observer_name || o.observer_id)).join(', ')}</div>` : ''}
          ${sparkHtml ? `<div class="mnc-spark">${sparkHtml}</div>` : ''}
          <div class="mnc-actions">
            <button class="mnc-btn" data-action="health" data-key="${mn.pubkey}">Full health →</button>
            <button class="mnc-btn" data-action="packets" data-key="${mn.pubkey}">View packets →</button>
          </div>
        </div>`;
      } catch (err) {
        const is404 = err && err.message && err.message.includes('404');
        const statusIcon = is404 ? '📡' : '❓';
        const statusMsg = is404
          ? 'Waiting for first advert — this node has been seen in channel messages but hasn\u2019t advertised yet'
          : 'Could not load data';
        return `<div class="my-node-card silent" data-key="${mn.pubkey}" tabindex="0" role="button">
          <div class="mnc-header">
            <div class="mnc-status">${statusIcon}</div>
            <div class="mnc-name">${escapeHtml(mn.name || truncate(mn.pubkey, 12))}</div>
            <button class="mnc-remove" data-key="${mn.pubkey}" title="Remove" aria-label="Remove ${escapeAttr(mn.name || truncate(mn.pubkey, 12))} from My Mesh">✕</button>
          </div>
          <div class="mnc-status-text">${statusMsg}</div>
        </div>`;
      }
    }));

    grid.innerHTML = cards.join('');

    // Wire up remove buttons
    grid.querySelectorAll('.mnc-remove').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        removeMyNode(btn.dataset.key);
        loadMyNodes();
        // Update title if no nodes left
        const h1 = document.querySelector('.home-hero h1');
        if (h1 && !getMyNodes().length) h1.textContent = 'CoreScope';
      });
    });

    // Wire up action buttons
    grid.querySelectorAll('.mnc-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        if (btn.dataset.action === 'health') loadHealth(btn.dataset.key);
        if (btn.dataset.action === 'packets') window.location.hash = '#/packets/' + btn.dataset.key;
      });
    });

    // Card click → health
    grid.querySelectorAll('.my-node-card').forEach(card => {
      const handler = (e) => {
        if (e.target.closest('.mnc-remove') || e.target.closest('.mnc-btn')) return;
        loadHealth(card.dataset.key);
      };
      card.addEventListener('click', handler);
      card.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(e); }
      });
    });
  }

  function buildSparkline(packets) {
    if (!packets.length) return '';
    // Group into hourly buckets over last 24h
    const now = Date.now();
    const buckets = new Array(24).fill(0);
    packets.forEach(p => {
      const t = new Date(p.timestamp || p.created_at).getTime();
      const hoursAgo = Math.floor((now - t) / 3600000);
      if (hoursAgo >= 0 && hoursAgo < 24) buckets[23 - hoursAgo]++;
    });
    const max = Math.max(...buckets, 1);
    const bars = buckets.map(v => {
      const h = Math.max(2, Math.round((v / max) * 24));
      const opacity = v > 0 ? 0.4 + (v / max) * 0.6 : 0.1;
      return `<div class="home-spark-bar" style="height:${h}px;opacity:${opacity}"></div>`;
    }).join('');
    return `<div class="home-spark-label">24h activity</div><div class="home-spark-bars">${bars}</div>`;
  }

  // ==================== STATS ====================
  async function loadStats() {
    try {
      const s = await api('/stats', { ttl: CLIENT_TTL.nodeSearch });
      const el = document.getElementById('homeStats');
      if (!el) return;
      el.innerHTML = `
        <div class="home-stat"><div class="val">${s.totalTransmissions ?? s.totalPackets ?? '—'}</div><div class="lbl">Transmissions</div></div>
        <div class="home-stat"><div class="val">${s.totalNodes ?? '—'}</div><div class="lbl">Nodes</div></div>
        <div class="home-stat"><div class="val">${s.totalObservers ?? '—'}</div><div class="lbl">Observers</div></div>
        <div class="home-stat"><div class="val">${s.packetsLast24h ?? '—'}</div><div class="lbl">Last 24h</div></div>
      `;
    } catch {}
  }

  // ==================== HEALTH DETAIL ====================
  async function loadHealth(pubkey) {
    const card = document.getElementById('homeHealth');
    const journey = document.getElementById('homeJourney');
    if (!card) return;
    card.innerHTML = '<p style="color:var(--text-muted);padding:12px">Loading…</p>';
    card.classList.add('visible');
    if (journey) journey.classList.remove('visible');

    try {
      const h = await api('/nodes/' + encodeURIComponent(pubkey) + '/health', { ttl: CLIENT_TTL.nodeHealth });
      const node = h.node || {};
      const stats = h.stats || {};
      const packets = h.recentPackets || [];
      const hasLocation = node.lat != null && node.lon != null;
      const observers = h.observers || [];
      const claimed = isMyNode(pubkey);

      let status = 'silent', color = 'red', statusMsg = 'Not heard in 24+ hours';
      if (stats.lastHeard) {
        const ageMs = Date.now() - new Date(stats.lastHeard).getTime();
        const ago = timeAgo(stats.lastHeard);
        if (ageMs < HEALTH_THRESHOLDS.nodeDegradedMs) { status = 'healthy'; color = 'green'; statusMsg = `Last heard ${ago}`; }
        else if (ageMs < HEALTH_THRESHOLDS.nodeSilentMs) { status = 'degraded'; color = 'yellow'; statusMsg = `Last heard ${ago}`; }
        else { statusMsg = `Last heard ${ago}`; }
      }

      const snrVal = stats.avgSnr;
      const snrLabel = snrVal != null ? (snrVal > 10 ? 'Excellent' : snrVal > 0 ? 'Good' : snrVal > -5 ? 'Marginal' : 'Poor') : '';

      card.innerHTML = `
        <div class="health-banner ${color}">
          <span>${status === 'healthy' ? '✅' : status === 'degraded' ? '⚠️' : '❌'}</span>
          <span><strong>${escapeHtml(node.name || truncate(pubkey, 16))}</strong> — ${statusMsg}</span>
          ${!claimed ? `<button class="health-claim" data-key="${pubkey}" data-name="${escapeAttr(node.name || '')}">+ Add to My Mesh</button>` : ''}
        </div>
        <div class="health-body">
          <div class="health-metrics">
            <div class="health-metric"><div class="val">${stats.packetsToday ?? '—'}</div><div class="lbl">Packets Today</div></div>
            <div class="health-metric"><div class="val">${observers.length}</div><div class="lbl">Observers</div></div>
            <div class="health-metric"><div class="val">${stats.lastHeard ? timeAgo(stats.lastHeard) : '—'}</div><div class="lbl">Last seen</div></div>
            <div class="health-metric"><div class="val">${snrVal != null ? Number(snrVal).toFixed(1) + ' dB' : '—'}</div><div class="lbl">Avg SNR${snrLabel ? ' · ' + snrLabel : ''}</div></div>
            <div class="health-metric"><div class="val">${stats.avgHops != null ? stats.avgHops.toFixed(1) : '—'}</div><div class="lbl">Avg Hops</div></div>
          </div>
          ${observers.length ? `<div class="health-observers"><strong>Heard by:</strong> ${observers.map(o => escapeHtml(o.observer_name || o.observer_id)).join(', ')}</div>` : ''}
          ${hasLocation ? '<div class="health-map" id="healthMap"></div>' : ''}
          <div class="health-timeline">
            <h3>Recent Activity</h3>
            ${packets.length ? packets.slice(0, 10).map(p => {
              const decoded = p.decoded_json ? JSON.parse(p.decoded_json) : {};
              const obsId = p.observer_name || p.observer_id || '?';
              return `<div class="timeline-item" tabindex="0" role="button" data-pkt='${JSON.stringify({
                from: node.name || truncate(pubkey, 12),
                observers: [obsId],
                type: p.payload_type,
                time: p.timestamp || p.created_at
              }).replace(/'/g, '&#39;')}'>
                <span class="badge" style="background:var(--type-${payloadTypeColor(p.payload_type)})">${escapeHtml(payloadTypeName(p.payload_type))}</span>
                <span>via ${escapeHtml(obsId)}</span>
                <span class="time">${timeAgo(p.timestamp || p.created_at)}</span>
                <span class="snr">${p.snr != null ? Number(p.snr).toFixed(1) + ' dB' : ''}</span>
              </div>`;
            }).join('') : '<p style="color:var(--text-muted);font-size:.85rem">No recent packets found for this node.</p>'}
          </div>
        </div>
      `;

      // Claim button in health detail
      card.querySelector('.health-claim')?.addEventListener('click', (e) => {
        e.stopPropagation();
        addMyNode(pubkey, node.name);
        e.target.remove();
        loadMyNodes();
        const h1 = document.querySelector('.home-hero h1');
        if (h1) h1.textContent = 'My Mesh';
      });

      // Mini map
      if (hasLocation && typeof L !== 'undefined') {
        if (miniMap) { miniMap.remove(); miniMap = null; }
        const mapEl = document.getElementById('healthMap');
        if (mapEl) {
          miniMap = L.map(mapEl, { zoomControl: false, attributionControl: false }).setView([node.lat, node.lon], 12);
          L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png').addTo(miniMap);
          L.marker([node.lat, node.lon]).addTo(miniMap);
          setTimeout(() => miniMap.invalidateSize(), 100);
        }
      }

      // Scroll to health card
      card.scrollIntoView({ behavior: 'smooth', block: 'start' });

      // Timeline click/keyboard → journey
      card.querySelectorAll('.timeline-item').forEach(item => {
        const activate = () => { try { showJourney(JSON.parse(item.dataset.pkt)); } catch {} };
        item.addEventListener('click', activate);
        item.addEventListener('keydown', (e) => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate(); }
        });
      });
    } catch (e) {
      card.innerHTML = '<p style="color:var(--status-red, #ef4444);padding:12px">Failed to load node health.</p>';
    }
  }

  function showJourney(data) {
    const el = document.getElementById('homeJourney');
    if (!el) return;
    const nodes = [];
    nodes.push({ name: data.from, meta: 'Sender' });
    if (data.observers && data.observers.length) {
      data.observers.forEach(o => nodes.push({ name: o, meta: 'Observer' }));
    }
    const flow = nodes.map((n, i) => {
      const nodeHtml = `<div class="journey-node"><div class="node-name">${escapeHtml(n.name)}</div><div class="node-meta">${n.meta}</div></div>`;
      return i < nodes.length - 1 ? nodeHtml + '<div class="journey-arrow"></div>' : nodeHtml;
    }).join('');
    el.innerHTML = `<div class="journey-title">Packet Journey — ${escapeHtml(payloadTypeName(data.type))}</div><div class="journey-flow">${flow}</div>`;
    el.classList.add('visible');
  }

  // ==================== HELPERS ====================
  function escapeHtml(s) { return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
  function escapeAttr(s) { return String(s).replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }
  function timeSinceMs(d) { return Date.now() - d.getTime(); }

  function checklist(homeCfg) {
    var html = '';
    // Render steps (getting started guide)
    if (homeCfg?.steps?.length) {
      html += homeCfg.steps.map(s => `<div class="checklist-item"><div class="checklist-q" role="button" tabindex="0" aria-expanded="false">${escapeHtml(s.emoji || '')} ${escapeHtml(s.title)}</div><div class="checklist-a">${window.miniMarkdown ? miniMarkdown(s.description) : escapeHtml(s.description)}</div></div>`).join('');
    }
    // Render FAQ/checklist (additional Q&A)
    if (homeCfg?.checklist?.length) {
      if (html) html += '<h3 style="margin:24px 0 12px;font-size:16px">❓ FAQ</h3>';
      html += homeCfg.checklist.map(i => `<div class="checklist-item"><div class="checklist-q" role="button" tabindex="0" aria-expanded="false">${escapeHtml(i.question)}</div><div class="checklist-a">${window.miniMarkdown ? miniMarkdown(i.answer) : escapeHtml(i.answer)}</div></div>`).join('');
    }
    // Fallback: Bay Area defaults when no config at all
    if (!html) {
      const items = [
        { q: '💬 First: Join the Bay Area MeshCore Discord',
          a: '<p>The community Discord is the best place to get help and find local mesh enthusiasts.</p><p><a href="https://discord.gg/q59JzsYTst" target="_blank" rel="noopener" style="color:var(--accent);font-weight:600">Join the Discord ↗</a></p><p>Start with <strong>#intro-to-meshcore</strong> — it has detailed setup instructions.</p>' },
        { q: '🔵 Step 1: Connect via Bluetooth',
          a: '<p>Flash <strong>BLE companion</strong> firmware from <a href="https://flasher.meshcore.co.uk/" target="_blank" rel="noopener" style="color:var(--accent)">MeshCore Flasher</a>.</p><ul><li>Screenless devices: default PIN <code>123456</code></li><li>Screen devices: random PIN shown on display</li><li>If pairing fails: forget device, reboot, re-pair</li></ul>' },
        { q: '📻 Step 2: Set the right frequency preset',
          a: '<p><strong>US Recommended:</strong></p><div style="margin:8px 0;padding:8px 12px;background:var(--surface-1);border-radius:6px;font-family:var(--mono);font-size:.85rem">910.525 MHz · BW 62.5 kHz · SF 7 · CR 5</div><p>Select <strong>"US Recommended"</strong> in the app or flasher.</p>' },
        { q: '📡 Step 3: Advertise yourself',
          a: '<p>Tap the signal icon → <strong>Flood</strong> to broadcast your node to the mesh. Companions only advert when you trigger it manually.</p>' },
        { q: '🔁 Step 4: Check "Heard N repeats"',
          a: '<ul><li><strong>"Sent"</strong> = transmitted, no confirmation</li><li><strong>"Heard 0 repeats"</strong> = no repeater picked it up</li><li><strong>"Heard 1+ repeats"</strong> = you\'re on the mesh!</li></ul>' },
        { q: '📍 Repeaters near you?',
          a: '<p><a href="#/map" style="color:var(--accent)">Check the network map</a> to see active repeaters.</p>' }
      ];
      html = items.map(i => `<div class="checklist-item"><div class="checklist-q" role="button" tabindex="0" aria-expanded="false">${i.q}</div><div class="checklist-a">${i.a}</div></div>`).join('');
    }
    return html;
  }

  registerPage('home', { init, destroy });
})();
