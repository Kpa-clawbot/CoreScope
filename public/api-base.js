/* The one place that knows where the API lives. Every /api request and the
 * WebSocket go through window.CSApi, so the same files work embedded in the
 * Go server (apiBase '') and from a static host that calls the API on another
 * origin (apiBase 'https://api.example'). Reads window.CS_DEPLOYMENT, set by
 * deployment.js, which must load first. .eslintrc.json keeps raw '/api'
 * fetches out of every other file. */
(function () {
  'use strict';
  var dep = window.CS_DEPLOYMENT || {};
  var base = String(dep.apiBase || '').replace(/\/+$/, '');
  var auth = dep.auth !== false;
  var classicBase = String(dep.classicUrl || '').replace(/\/+$/, '');

  // Logins on another origin send the session cookie, which no server accepts
  // across origins yet, so CORS blocks every request. build.sh refuses the
  // pair; a hand-edited deployment.js gets told why instead.
  if (base && auth && window.console) {
    window.console.warn('CoreScope: deployment.js pairs an apiBase on another origin with auth: true. ' +
      'The server does not accept the session cookie across origins, so every API request will be blocked. ' +
      'Set auth: false, or serve this UI from the API origin.');
  }

  // url('/api/nodes?limit=50') is the full URL of an API path.
  function url(path) {
    if (typeof path !== 'string' || path.charAt(0) !== '/') throw new Error('api path must start with /: ' + path);
    return base + path;
  }

  // wsUrl('/ws') is the WebSocket URL on the API host.
  function wsUrl(path) {
    if (base) return base.replace(/^http/, 'ws') + path;
    return (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + path;
  }

  // credentials picks what a request may carry. Same origin keeps the
  // browser default. Cross-origin with logins sends the session cookie, which
  // no server accepts yet: build.sh refuses that combination. Cross-origin
  // without logins sends nothing: the server's embed CORS tier never allows
  // credentials, so an 'include' request would be blocked.
  function credentials() {
    if (!base) return 'same-origin';
    return auth ? 'include' : 'omit';
  }

  function apiFetch(path, opts) {
    var o = {};
    if (opts) for (var k in opts) if (Object.prototype.hasOwnProperty.call(opts, k)) o[k] = opts[k];
    if (!o.credentials) o.credentials = credentials();
    return fetch(url(path), o);
  }

  // postJSON sends a JSON body as text/plain, so a cross-origin POST stays a
  // CORS simple request with no preflight. The server decodes the body as JSON
  // whatever its Content-Type; cmd/server/simple_post_test.go pins that.
  function postJSON(path, body) {
    return apiFetch(path, { method: 'POST', body: JSON.stringify(body), headers: { 'Content-Type': 'text/plain;charset=UTF-8' } });
  }

  // classicHref('#/account') opens the same route on the embedded UI, or
  // returns null when this build has no classic UI to send people to.
  function classicHref(hash) {
    if (!classicBase) return null;
    var h = typeof hash === 'string' ? hash : (location.hash || '');
    return classicBase + '/' + (h && h.charAt(0) !== '#' ? '#' + h : h);
  }

  window.CSApi = {
    base: base,
    auth: auth,
    crossOrigin: !!base,
    // anonymous: a static build that must not offer logins or admin actions.
    anonymous: !!base && !auth,
    // embedded: the server serves this UI (the classic descriptor on its own
    // origin). A packaged build in the web container is same-origin but not
    // embedded.
    embedded: !base && (dep.channel || 'classic') === 'classic',
    channel: dep.channel || 'classic',
    commit: dep.commit || null,
    url: url,
    wsUrl: wsUrl,
    fetch: apiFetch,
    postJSON: postJSON,
    classicHref: classicHref
  };
})();
