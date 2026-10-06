/* Settings sync for optional user management
 * (docs/specs/2026-10-06-user-settings-sync-design.md). Inert unless
 * window.MC_USER_MGMT is on and CSAuth reports a logged-in user. While
 * active it wraps Storage.prototype.setItem/removeItem for the keys the
 * server allowlists, merges this device with the account's copy and
 * pushes changes. Exposes window.CSSettingsSync. */
(function () {
  'use strict';

  // ── Three-way merge (pure) ──
  // Values are raw localStorage strings; undefined means "not set".

  function own(o, k) { return o && Object.prototype.hasOwnProperty.call(o, k) ? o[k] : undefined; }

  // parseList returns the array behind raw ([] when unset), or null when
  // raw is not a JSON array.
  function parseList(raw) {
    if (raw === undefined) return [];
    try {
      var v = JSON.parse(raw);
      return Array.isArray(v) ? v : null;
    } catch (e) { return null; }
  }

  // identity of a set item: its idField when it is an object that has one,
  // else its JSON (a string item is compared as itself).
  function identity(item, idField) {
    if (idField && item && typeof item === 'object' && item[idField] != null) return 'f:' + String(item[idField]);
    return 'j:' + JSON.stringify(item);
  }

  function indexList(list, idField) {
    var map = Object.create(null), order = [];
    list.forEach(function (item) {
      var id = identity(item, idField);
      if (id in map) return;
      map[id] = item;
      order.push(id);
    });
    return { map: map, order: order };
  }

  function sameItem(a, b) { return JSON.stringify(a) === JSON.stringify(b); }

  // mergeSet computes P + (L - B) - (B - L) by identity. Returns the merged
  // raw string, undefined when neither side has the key, or null when a
  // side is not a JSON list (the caller then merges it as a scalar).
  function mergeSet(l, p, b, idField) {
    var al = parseList(l), ap = parseList(p), ab = parseList(b);
    if (!al || !ap || !ab) return null;
    var L = indexList(al, idField), P = indexList(ap, idField), B = indexList(ab, idField);
    var out = [];
    P.order.forEach(function (id) {
      var inL = id in L.map, inB = id in B.map;
      if (inB && !inL) return; // removed on this device since the last sync
      var onlyLocalChanged = inL && inB && !sameItem(L.map[id], B.map[id]) && sameItem(P.map[id], B.map[id]);
      out.push(onlyLocalChanged ? L.map[id] : P.map[id]);
    });
    L.order.forEach(function (id) {
      if (id in P.map || id in B.map) return; // in the profile, or removed on another device
      out.push(L.map[id]); // added on this device since the last sync
    });
    if (!out.length && l === undefined && p === undefined) return undefined;
    var s = JSON.stringify(out);
    // Keep an existing spelling of the same list: formatting alone is no change.
    if (l !== undefined && JSON.stringify(al) === s) return l;
    if (p !== undefined && JSON.stringify(ap) === s) return p;
    return s;
  }

  function mergeScalar(l, p, b) { return (l !== b && p === b) ? l : p; }

  // mergeDocs merges local, profile and baseline ({key: raw}) for every
  // allowlisted key. localChanges lists the keys this device must write;
  // differsFromProfile says the result must be pushed.
  function mergeDocs(local, profile, base, allowlist) {
    var keys = {}, localChanges = [], differs = false;
    allowlist.forEach(function (entry) {
      var k = entry.key, l = own(local, k), p = own(profile, k), b = own(base, k), v;
      if (entry.kind === 'set') {
        v = mergeSet(l, p, b, entry.id || '');
        if (v === null) {
          console.warn('[settings-sync] ' + k + ' is not a JSON list on this device or in the account; merged as one value');
          v = mergeScalar(l, p, b);
        }
      } else {
        v = mergeScalar(l, p, b);
      }
      if (v !== undefined) keys[k] = v;
      if (v !== l) localChanges.push(k);
      if (v !== p) differs = true;
    });
    return { keys: keys, localChanges: localChanges, differsFromProfile: differs };
  }

  window.CSSettingsSync = {
    _test: { mergeDocs: mergeDocs }
  };
})();
