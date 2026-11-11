/* What a static build of the UI shows that the embedded one does not: a
 * channel badge in the nav that opens a pre-filled beta issue, an "Open in
 * classic" link, and the note that replaces a control an anonymous build
 * cannot offer. Reads window.CSApi (api-base.js). Inert on the embedded UI,
 * whose channel is classic. */
(function () {
  'use strict';
  var REPO = 'https://github.com/Kpa-clawbot/CoreScope';
  // The static channel is an operator's own frontend, not a beta: no badge.
  var BADGES = { beta: 'Beta', next: 'Next', dev: 'Dev' };

  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  function badgeLabel(channel) {
    return Object.prototype.hasOwnProperty.call(BADGES, channel) ? BADGES[channel] : null;
  }

  // issueUrl opens a new issue pre-filled with what a report needs and nobody
  // remembers to type: page, channel, commit, browser. GitHub's title and body
  // parameters do this without an issue template in the repository.
  function issueUrl(channel, commit, page, browser) {
    // Account queries can contain mail-link tokens. Redact before creating
    // the badge: account.js removes tokens with replaceState, which does not
    // emit hashchange to refresh an already-created issue link.
    page = String(page || '');
    if (/^#\/account(?:\/|\?|$)/.test(page)) page = page.split('?')[0];
    var body = '**Page:** ' + (page || '') + '\n\n' +
      '**What I expected (what the classic site does):**\n\n\n' +
      '**What happened on the ' + (channel || 'beta') + ' frontend:**\n\n\n' +
      '---\n' +
      'Channel: ' + (channel || '') + ' · Commit: ' + (commit || 'unknown') + ' · Browser: ' + (browser || '') + '\n';
    return REPO + '/issues/new?title=' + encodeURIComponent('[' + (channel || 'beta') + '] ') +
      '&body=' + encodeURIComponent(body);
  }

  // classicOnlyHtml stands in for a control this build cannot offer, e.g.
  // classicOnlyHtml('Resetting server stats').
  function classicOnlyHtml(what) {
    var href = window.CSApi.classicHref();
    return '<p class="cs-classic-only">' + esc(what) + ' is available on the classic site.' +
      (href ? ' <a class="cs-classic-link" href="' + esc(href) + '">Open in classic</a>' : '') + '</p>';
  }

  function refreshLinks(doc) {
    var api = window.CSApi;
    var badge = doc.getElementById('csChannelBadge');
    if (badge) badge.href = issueUrl(api.channel, api.commit, location.hash || '#/', navigator.userAgent);
    var classic = doc.getElementById('csClassicLink');
    if (classic) classic.href = api.classicHref() || '#';
  }

  // renderNav adds the badge (and, with classicUrl set, the classic link)
  // after the brand. Both follow the hash, so they open the current page.
  function renderNav(doc) {
    var api = window.CSApi;
    var label = badgeLabel(api.channel);
    if (!label || doc.getElementById('csChannelBadge')) return false;
    var brand = doc.querySelector('.top-nav .nav-brand');
    if (!brand) return false;
    var wrap = doc.createElement('span');
    wrap.className = 'cs-channel-wrap';
    var badge = doc.createElement('a');
    badge.id = 'csChannelBadge';
    badge.className = 'cs-channel-badge';
    badge.target = '_blank';
    badge.rel = 'noopener';
    badge.title = label + (api.commit ? ' ' + api.commit : '') + ': report an issue with this page';
    badge.textContent = label;
    wrap.appendChild(badge);
    if (api.classicHref()) {
      var classic = doc.createElement('a');
      classic.id = 'csClassicLink';
      classic.className = 'cs-classic-nav-link';
      classic.title = 'Open this page on the classic site';
      classic.textContent = 'Classic';
      wrap.appendChild(classic);
    }
    brand.parentNode.insertBefore(wrap, brand.nextSibling);
    refreshLinks(doc);
    window.addEventListener('hashchange', function () { refreshLinks(doc); });
    return true;
  }

  window.CSBeta = { badgeLabel: badgeLabel, issueUrl: issueUrl, classicOnlyHtml: classicOnlyHtml, renderNav: renderNav };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { renderNav(document); });
  } else {
    renderNav(document);
  }
})();
