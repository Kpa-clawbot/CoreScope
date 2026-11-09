/* Which builds are running. The embedded UI is the server's own build, so
 * GET /api/health describes both. A UI served apart from the server (a static
 * host, the web container) is a build of its own, named by deployment.js
 * (version, commit), and the server may run another version. BuildInfo pairs
 * the two for the nav drawer's version footer, the perf page and issue
 * reports, and flags two different builds when it can tell. Reads
 * window.CSApi (api-base.js), which must load first. */
(function () {
  'use strict';
  var RELEASE = /^v\d+\.\d+\.\d+$/;
  var healthPromise = null;

  // health() is GET /api/health, asked once per page; null when it fails.
  function health() {
    if (healthPromise) return healthPromise;
    try {
      healthPromise = window.CSApi.fetch('/api/health', { headers: { Accept: 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : null; })
        .catch(function () { return null; });
    } catch (e) {
      healthPromise = Promise.resolve(null);
    }
    return healthPromise;
  }

  function known(v) {
    v = v == null ? '' : String(v);
    return v && v !== 'unknown' ? v : null;
  }

  // sha returns v when it is a git commit (7 to 40 hex digits).
  function sha(v) {
    return v && /^[0-9a-f]{7,40}$/i.test(v) ? v.toLowerCase() : null;
  }

  // sameBuild is true or false, or null when it cannot tell. Two commits
  // decide (one may be a longer form of the other); without them, two release
  // tags do. "edge", a git describe string or a build id says nothing.
  function sameBuild(a, b) {
    var x = sha(a.commit), y = sha(b.commit);
    if (x && y) return x.indexOf(y) === 0 || y.indexOf(x) === 0;
    if (a.version && b.version && RELEASE.test(a.version) && RELEASE.test(b.version)) return a.version === b.version;
    return null;
  }

  // describe(health) → { separate, mismatch, ui, server }, each build being
  // { version, commit } with unknown parts null. For the embedded UI, ui is
  // the server build.
  function describe(h) {
    var api = window.CSApi;
    var server = { version: known(h && h.version), commit: known(h && h.commit) };
    var separate = api.channel !== 'classic';
    var ui = separate ? { version: known(api.version), commit: known(api.commit) } : server;
    return { separate: separate, mismatch: separate && sameBuild(ui, server) === false, ui: ui, server: server };
  }

  // label(build) is what to call a build: its version, else its commit
  // (shortened when it is a git commit), else null.
  function label(b) {
    if (b.version) return b.version;
    if (b.commit) return sha(b.commit) ? b.commit.slice(0, 7) : b.commit;
    return null;
  }

  window.BuildInfo = {
    health: health, describe: describe, label: label,
    isRelease: function (v) { return RELEASE.test(v || ''); },
    isCommit: function (v) { return !!sha(v); }
  };
})();
