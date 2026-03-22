/**
 * Client-side hop resolver — eliminates /api/resolve-hops HTTP requests.
 * Mirrors the server's disambiguateHops() logic from server.js.
 */
window.HopResolver = (function() {
  'use strict';

  const MAX_HOP_DIST = 1.8; // ~200km in degrees
  const REGION_RADIUS_KM = 300;
  let prefixIdx = {};   // lowercase hex prefix → [node, ...]
  let nodesList = [];
  let observerIataMap = {}; // observer_id → iata
  let iataCoords = {};  // iata → {lat, lon}

  function dist(lat1, lon1, lat2, lon2) {
    return Math.sqrt((lat1 - lat2) ** 2 + (lon1 - lon2) ** 2);
  }

  function haversineKm(lat1, lon1, lat2, lon2) {
    const R = 6371;
    const dLat = (lat2 - lat1) * Math.PI / 180;
    const dLon = (lon2 - lon1) * Math.PI / 180;
    const a = Math.sin(dLat / 2) ** 2 +
      Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) *
      Math.sin(dLon / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
  }

  /**
   * Initialize (or rebuild) the prefix index from the full nodes list.
   * @param {Array} nodes - Array of {public_key, name, lat, lon, ...}
   * @param {Object} [opts] - Optional: { observers: [{id, iata}], iataCoords: {code: {lat,lon}} }
   */
  function init(nodes, opts) {
    nodesList = nodes || [];
    prefixIdx = {};
    for (const n of nodesList) {
      if (!n.public_key) continue;
      const pk = n.public_key.toLowerCase();
      for (let len = 1; len <= 3; len++) {
        const p = pk.slice(0, len * 2);
        if (!prefixIdx[p]) prefixIdx[p] = [];
        prefixIdx[p].push(n);
      }
    }
    // Store observer IATA mapping and coords if provided
    observerIataMap = {};
    if (opts && opts.observers) {
      for (const o of opts.observers) {
        if (o.id && o.iata) observerIataMap[o.id] = o.iata;
      }
    }
    iataCoords = (opts && opts.iataCoords) || (window.IATA_COORDS_GEO) || {};
  }

  /**
   * Check if a node is near an IATA region center.
   * Returns { near, method, distKm } or null.
   */
  function nodeInRegion(candidate, iata) {
    const center = iataCoords[iata];
    if (!center) return null;
    if (candidate.lat && candidate.lon && !(candidate.lat === 0 && candidate.lon === 0)) {
      const d = haversineKm(candidate.lat, candidate.lon, center.lat, center.lon);
      return { near: d <= REGION_RADIUS_KM, method: 'geo', distKm: Math.round(d) };
    }
    return null; // no GPS — can't geo-filter client-side
  }

  /**
   * Resolve an array of hex hop prefixes to node info.
   * Returns a map: { hop: {name, pubkey, lat, lon, ambiguous, unreliable} }
   *
   * @param {string[]} hops - Hex prefixes
   * @param {number|null} originLat - Sender latitude (forward anchor)
   * @param {number|null} originLon - Sender longitude (forward anchor)
   * @param {number|null} observerLat - Observer latitude (backward anchor)
   * @param {number|null} observerLon - Observer longitude (backward anchor)
   * @returns {Object} resolved map keyed by hop prefix
   */
  function resolve(hops, originLat, originLon, observerLat, observerLon, observerId) {
    if (!hops || !hops.length) return {};

    // Determine observer's IATA for regional filtering
    const packetIata = observerId ? observerIataMap[observerId] : null;

    const resolved = {};
    const hopPositions = {};

    // First pass: find candidates with regional filtering
    for (const hop of hops) {
      const h = hop.toLowerCase();
      const allCandidates = prefixIdx[h] || [];
      if (allCandidates.length === 0) {
        resolved[hop] = { name: null, candidates: [], conflicts: [] };
      } else if (allCandidates.length === 1) {
        const c = allCandidates[0];
        const regionCheck = packetIata ? nodeInRegion(c, packetIata) : null;
        resolved[hop] = { name: c.name, pubkey: c.public_key,
          candidates: [{ name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon, regional: regionCheck ? regionCheck.near : false, filterMethod: regionCheck ? regionCheck.method : 'none', distKm: regionCheck ? regionCheck.distKm : undefined }],
          conflicts: [] };
      } else {
        // Multiple candidates — apply geo regional filtering
        const checked = allCandidates.map(c => {
          const r = packetIata ? nodeInRegion(c, packetIata) : null;
          return { ...c, regional: r ? r.near : false, filterMethod: r ? r.method : 'none', distKm: r ? r.distKm : undefined };
        });
        const regional = checked.filter(c => c.regional);
        const candidates = regional.length > 0 ? regional : checked;
        const globalFallback = regional.length === 0 && checked.length > 0 && packetIata != null;

        const conflicts = candidates.map(c => ({
          name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon,
          regional: c.regional, filterMethod: c.filterMethod, distKm: c.distKm
        }));

        if (candidates.length === 1) {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            candidates: conflicts, conflicts, globalFallback };
        } else {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            ambiguous: true, candidates: conflicts, conflicts, globalFallback,
            hopBytes: Math.ceil(hop.length / 2), totalGlobal: allCandidates.length, totalRegional: regional.length };
        }
      }
    }

    // Build initial positions for unambiguous hops
    for (const hop of hops) {
      const r = resolved[hop];
      if (r && !r.ambiguous && r.pubkey) {
        const node = nodesList.find(n => n.public_key === r.pubkey);
        if (node && node.lat && node.lon && !(node.lat === 0 && node.lon === 0)) {
          hopPositions[hop] = { lat: node.lat, lon: node.lon };
        }
      }
    }

    // Forward pass
    let lastPos = (originLat != null && originLon != null) ? { lat: originLat, lon: originLon } : null;
    for (let i = 0; i < hops.length; i++) {
      const hop = hops[i];
      if (hopPositions[hop]) { lastPos = hopPositions[hop]; continue; }
      const r = resolved[hop];
      if (!r || !r.ambiguous) continue;
      const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (!withLoc.length) continue;
      let anchor = lastPos;
      if (!anchor && i === hops.length - 1 && observerLat != null) {
        anchor = { lat: observerLat, lon: observerLon };
      }
      if (anchor) {
        withLoc.sort((a, b) => dist(a.lat, a.lon, anchor.lat, anchor.lon) - dist(b.lat, b.lon, anchor.lat, anchor.lon));
      }
      r.name = withLoc[0].name;
      r.pubkey = withLoc[0].pubkey;
      hopPositions[hop] = { lat: withLoc[0].lat, lon: withLoc[0].lon };
      lastPos = hopPositions[hop];
    }

    // Backward pass
    let nextPos = (observerLat != null && observerLon != null) ? { lat: observerLat, lon: observerLon } : null;
    for (let i = hops.length - 1; i >= 0; i--) {
      const hop = hops[i];
      if (hopPositions[hop]) { nextPos = hopPositions[hop]; continue; }
      const r = resolved[hop];
      if (!r || !r.ambiguous) continue;
      const withLoc = r.candidates.filter(c => c.lat && c.lon && !(c.lat === 0 && c.lon === 0));
      if (!withLoc.length || !nextPos) continue;
      withLoc.sort((a, b) => dist(a.lat, a.lon, nextPos.lat, nextPos.lon) - dist(b.lat, b.lon, nextPos.lat, nextPos.lon));
      r.name = withLoc[0].name;
      r.pubkey = withLoc[0].pubkey;
      hopPositions[hop] = { lat: withLoc[0].lat, lon: withLoc[0].lon };
      nextPos = hopPositions[hop];
    }

    // Sanity check: drop hops impossibly far from neighbors
    for (let i = 0; i < hops.length; i++) {
      const pos = hopPositions[hops[i]];
      if (!pos) continue;
      const prev = i > 0 ? hopPositions[hops[i - 1]] : null;
      const next = i < hops.length - 1 ? hopPositions[hops[i + 1]] : null;
      if (!prev && !next) continue;
      const dPrev = prev ? dist(pos.lat, pos.lon, prev.lat, prev.lon) : 0;
      const dNext = next ? dist(pos.lat, pos.lon, next.lat, next.lon) : 0;
      const tooFarPrev = prev && dPrev > MAX_HOP_DIST;
      const tooFarNext = next && dNext > MAX_HOP_DIST;
      if ((tooFarPrev && tooFarNext) || (tooFarPrev && !next) || (tooFarNext && !prev)) {
        const r = resolved[hops[i]];
        if (r) r.unreliable = true;
        delete hopPositions[hops[i]];
      }
    }

    return resolved;
  }

  /**
   * Check if the resolver has been initialized with nodes.
   */
  function ready() {
    return nodesList.length > 0;
  }

  return { init: init, resolve: resolve, ready: ready };
})();
