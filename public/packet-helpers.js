/* === CoreScope — packet-helpers.js (shared packet utilities) === */
'use strict';

/**
 * Cached JSON.parse helpers for packet data (issue #387).
 * Avoids repeated parsing of path_json / decoded_json on the same packet object.
 * Results are cached as _parsedPath / _parsedDecoded properties on the packet.
 *
 * Handles pre-parsed objects (non-string values) gracefully — returns them as-is.
 */

window.getParsedPath = function getParsedPath(p) {
  if (p._parsedPath !== undefined) return p._parsedPath || [];
  var raw = p.path_json;
  if (typeof raw !== 'string') {
    p._parsedPath = Array.isArray(raw) ? raw : [];
    return p._parsedPath;
  }
  try { p._parsedPath = JSON.parse(raw) || []; } catch (e) { p._parsedPath = []; }
  return p._parsedPath;
};

/**
 * Clear cached _parsedPath/_parsedDecoded from a packet object.
 * Must be called after spreading a parent packet into an observation/child,
 * otherwise the child inherits stale cached values from the parent (issue #504).
 */
window.clearParsedCache = function clearParsedCache(p) {
  delete p._parsedPath;
  delete p._parsedDecoded;
  delete p._parsedResolvedPath;
  return p;
};

/**
 * Parse resolved_path (server-side resolved full pubkeys).
 * Returns array of pubkey strings (or null entries) if present, or null if absent.
 * Cached as _parsedResolvedPath on the packet object.
 */
window.getResolvedPath = function getResolvedPath(p) {
  if (p._parsedResolvedPath !== undefined) return p._parsedResolvedPath;
  var raw = p.resolved_path;
  if (!raw) { p._parsedResolvedPath = null; return null; }
  if (typeof raw !== 'string') {
    p._parsedResolvedPath = Array.isArray(raw) ? raw : null;
    return p._parsedResolvedPath;
  }
  try { p._parsedResolvedPath = JSON.parse(raw) || null; } catch (e) { p._parsedResolvedPath = null; }
  return p._parsedResolvedPath;
};

window.getParsedDecoded = function getParsedDecoded(p) {
  if (p._parsedDecoded !== undefined) return p._parsedDecoded || {};
  var raw = p.decoded_json;
  if (typeof raw !== 'string') {
    p._parsedDecoded = (raw && typeof raw === 'object') ? raw : {};
    return p._parsedDecoded;
  }
  try { p._parsedDecoded = JSON.parse(raw) || {}; } catch (e) { p._parsedDecoded = {}; }
  return p._parsedDecoded;
};

/**
 * One observation's hops, inferred as a whole path (HopResolver.inferPath).
 * Shared by the packet detail pane and the map route view so both show the
 * same nodes. The observation's own resolved_path is the server hint.
 *
 * @param {Object} obs - an observation (path_json, observer_id, resolved_path)
 * @param {{lat,lon}|null} observer - where its observer is, when known
 * @param {{lat,lon}|null} origin - where the sender is, when known
 * @returns {Array} one inferPath entry per hop
 */
window.inferObservationPath = function inferObservationPath(obs, observer, origin) {
  return window.HopResolver.inferPath(window.getParsedPath(obs), {
    observer: observer,
    observerId: obs.observer_id,
    origin: origin,
    resolvedPath: window.getResolvedPath(obs),
  });
};

/**
 * Load the neighbour graph into HopResolver, once per page life. Its links
 * are evidence for inferPath(); a failed load is retried on the next call.
 */
var hopAffinityLoad = null;
window.ensureHopAffinity = function ensureHopAffinity() {
  if (!hopAffinityLoad) {
    hopAffinityLoad = api('/analytics/neighbor-graph', { ttl: 300000 })
      .then(function (graph) { window.HopResolver.setAffinity(graph); })
      .catch(function () { hopAffinityLoad = null; });
  }
  return hopAffinityLoad;
};

/**
 * A route-view hop for one inferPath entry. An unlisted hop becomes a
 * placeholder rather than the nearest node that happens to share its prefix.
 *
 * @param {string} hop - the hop's prefix
 * @param {Object} entry - its inferPath entry
 * @param {string} [role] - the node's role, for its glyph
 */
window.routeHopFromInferred = function routeHopFromInferred(hop, entry, role) {
  var e = entry || {};
  if (e.located) {
    return { lat: e.lat, lon: e.lon, name: e.name, pubkey: e.pubkey, role: role || null, resolved: true, confidence: e.confidence };
  }
  if (e.pubkey) return { name: e.name, pubkey: e.pubkey, role: role || null, resolved: false, gpsless: true };
  return { name: String(hop).slice(0, 8), pubkey: hop, resolved: false, unlisted: true };
};
