/* === CoreScope - ui-icons.js ===
 * Small inline SVG icon registry for app chrome. This keeps navigation and
 * controls consistent without adding a frontend build step or icon package.
 */
(function () {
  'use strict';

  var PATHS = {
    home: '<path d="M3 10.5 12 3l9 7.5"/><path d="M5 9.5V21h14V9.5"/><path d="M9.5 21v-6h5v6"/>',
    packets: '<path d="m21 8-9-5-9 5 9 5 9-5Z"/><path d="m3 8 9 5 9-5"/><path d="M3 8v8l9 5 9-5V8"/>',
    live: '<circle cx="12" cy="12" r="4" class="ui-icon-fill"/><path d="M4.9 4.9a10 10 0 0 0 0 14.2"/><path d="M19.1 4.9a10 10 0 0 1 0 14.2"/>',
    map: '<path d="m9 18-6 3V6l6-3 6 3 6-3v15l-6 3-6-3Z"/><path d="M9 3v15"/><path d="M15 6v15"/>',
    channels: '<path d="M4 6h16v10H7l-3 3V6Z"/><path d="M8 10h8"/><path d="M8 13h5"/>',
    menu: '<path d="M4 7h16"/><path d="M4 12h16"/><path d="M4 17h16"/>',
    nodes: '<rect x="4" y="4" width="6" height="6" rx="1.5"/><rect x="14" y="4" width="6" height="6" rx="1.5"/><rect x="9" y="14" width="6" height="6" rx="1.5"/><path d="M10 7h4"/><path d="M12 10v4"/>',
    tools: '<path d="M14.7 6.3a4 4 0 0 0-5 5L4 17v3h3l5.7-5.7a4 4 0 0 0 5-5l-2.4 2.4-3-3 2.4-2.4Z"/>',
    observers: '<path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="3"/>',
    analytics: '<path d="M4 20V10"/><path d="M10 20V4"/><path d="M16 20v-7"/><path d="M22 20H2"/>',
    perf: '<path d="m13 2-9 12h7l-1 8 9-12h-7l1-8Z"/>',
    lab: '<path d="M9 18V5l11-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="17" cy="16" r="3"/>',
    search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>',
    star: '<path d="m12 3 2.7 5.5 6 .9-4.4 4.2 1 6-5.3-2.8-5.3 2.8 1-6-4.4-4.2 6-.9L12 3Z"/>',
    palette: '<path d="M12 3a9 9 0 0 0 0 18h1.2a2 2 0 0 0 1.4-3.4 1 1 0 0 1 .7-1.7H17a4 4 0 0 0 0-8h-1.5A3.5 3.5 0 0 1 12 3Z"/><circle cx="7.5" cy="11" r="1"/><circle cx="10" cy="7.5" r="1"/><circle cx="14.5" cy="7.5" r="1"/>',
    moon: '<path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a7 7 0 1 0 10.5 10.5Z"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.9 4.9 1.4 1.4"/><path d="m17.7 17.7 1.4 1.4"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m4.9 19.1 1.4-1.4"/><path d="m17.7 6.3 1.4-1.4"/>',
    settings: '<path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z"/><path d="M4 12h2"/><path d="M18 12h2"/><path d="m6.3 6.3 1.4 1.4"/><path d="m16.3 16.3 1.4 1.4"/><path d="M12 4V2"/><path d="M12 22v-2"/><path d="m6.3 17.7 1.4-1.4"/><path d="m16.3 7.7 1.4-1.4"/>',
    compass: '<path d="m16.2 7.8-2.6 5.8-5.8 2.6 2.6-5.8 5.8-2.6Z"/><circle cx="12" cy="12" r="9"/>',
    close: '<path d="M6 6l12 12"/><path d="M18 6 6 18"/>',
    chevronDown: '<path d="m6 9 6 6 6-6"/>',
    chevronLeft: '<path d="m15 18-6-6 6-6"/>',
    chevronRight: '<path d="m9 18 6-6-6-6"/>',
    alert: '<path d="M12 3 2.8 20h18.4L12 3Z"/><path d="M12 9v5"/><path d="M12 17h.01"/>',
    spark: '<path d="M12 2v5"/><path d="M12 17v5"/><path d="M2 12h5"/><path d="M17 12h5"/><path d="m4.9 4.9 3.5 3.5"/><path d="m15.6 15.6 3.5 3.5"/><path d="m19.1 4.9-3.5 3.5"/><path d="m8.4 15.6-3.5 3.5"/>'
  };

  function svg(name, className, title) {
    var path = PATHS[name] || PATHS.spark;
    var cls = className || 'ui-icon';
    var titleMarkup = title ? '<title>' + String(title).replace(/[<>&"]/g, '') + '</title>' : '';
    return '<svg class="' + cls + '" viewBox="0 0 24 24" aria-hidden="' + (title ? 'false' : 'true') + '" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' + titleMarkup + path + '</svg>';
  }

  function mount(root) {
    var scope = root || document;
    if (!scope || !scope.querySelectorAll) return;
    var nodes = scope.querySelectorAll('[data-ui-icon]');
    for (var i = 0; i < nodes.length; i++) {
      var el = nodes[i];
      if (el.getAttribute('data-ui-icon-mounted') === '1') continue;
      el.innerHTML = svg(el.getAttribute('data-ui-icon'), el.getAttribute('data-ui-icon-class') || 'ui-icon');
      el.setAttribute('data-ui-icon-mounted', '1');
    }
  }

  window.UIIcon = { svg: svg, mount: mount };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { mount(document); });
  } else {
    mount(document);
  }
})();
