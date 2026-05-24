/* Shared "More" / long-tail navigation route list — single source of truth
   consumed by bottom-nav.js and nav-drawer.js. */
(function () {
  'use strict';
  window.NAV_MORE_ROUTES = [
    { route: 'nodes',     hash: '#/nodes',     label: 'Nodes',     icon: '🖥️' },
    { route: 'tools',     hash: '#/tools',     label: 'Tools',     icon: '🛠️' },
    { route: 'observers', hash: '#/observers', label: 'Observers', icon: '👁️' },
    { route: 'analytics', hash: '#/analytics', label: 'Analytics', icon: '📊' },
    { route: 'audio-lab', hash: '#/audio-lab', label: 'Audio Lab', icon: '🎵' },
    { route: 'perf',      hash: '#/perf',      label: 'Perf',      icon: '⚡' },
    { route: 'mc-keygen', hash: '#/mc-keygen', label: 'MC-Keygen', icon: '🔑' },
    { route: 'los',       hash: '#/los',       label: 'LOS',       icon: '🔭' },
  ];
})();
