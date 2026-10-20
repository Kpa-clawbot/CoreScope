/**
 * Client-side hop resolver — eliminates /api/resolve-hops HTTP requests.
 * Mirrors the server's disambiguateHops() logic from server.js.
 */
window.HopResolver = (function() {
  'use strict';

  const MAX_HOP_DIST = 1.8; // ~200km in degrees
  const REGION_RADIUS_KM = 300;

  // Only repeaters and room servers can appear as path hops per protocol.
  // Companions/sensors originate but never relay packets.
  function canAppearInPath(role) {
    if (!role) return false;
    var r = String(role).toLowerCase();
    return r.indexOf('repeater') >= 0 || r.indexOf('room_server') >= 0 || r === 'room';
  }
  let prefixIdx = {};   // lowercase hex prefix → [node, ...]
  let pubkeyIdx = {};   // full lowercase pubkey → node (O(1) lookup)
  let pubkeyPrefix8Idx = {}; // lowercase 8-byte (16 hex) pubkey prefix → node, null when ambiguous
  let nodesList = [];
  let observerIataMap = {}; // observer_id → iata
  let iataCoords = {};  // iata → {lat, lon}
  let affinityMap = {}; // pubkey → { neighborPubkey → score }

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
    pubkeyIdx = {};
    pubkeyPrefix8Idx = {};
    for (const n of nodesList) {
      if (!n.public_key) continue;
      const pk = n.public_key.toLowerCase();
      // pubkeyIdx includes ALL nodes — used by resolveFromServer for
      // server-confirmed full-pubkey lookups (any node type).
      pubkeyIdx[pk] = n;
      const p8 = pk.slice(0, 16);
      pubkeyPrefix8Idx[p8] = (p8 in pubkeyPrefix8Idx) ? null : n;
      // prefixIdx only includes nodes that can appear as path hops.
      if (!canAppearInPath(n.role)) continue;
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
   * Pick the best candidate by scoring against BOTH prev and next resolved hops.
   *
   * Strategy (in priority order):
   * 1. Neighbor-graph edge weight: sum of edge scores to prevPubkey + nextPubkey. Pick max.
   * 2. Geographic centroid: if no candidate has graph edges, compute centroid of
   *    prev+next positions and pick closest candidate by haversine distance.
   * 3. Single-anchor geo fallback: if only one neighbor is resolved, use it as anchor.
   * 4. Original heuristic: first candidate (when no context at all).
   *
   * @param {Array} candidates - candidates with lat/lon/pubkey/name
   * @param {string|null} prevPubkey - pubkey of previous resolved hop
   * @param {string|null} nextPubkey - pubkey of next resolved hop
   * @param {Object|null} prevPos - {lat, lon} of previous resolved hop or origin
   * @param {Object|null} nextPos - {lat, lon} of next resolved hop or observer
   * @returns {Object} best candidate
   */
  function pickByAffinity(candidates, prevPubkey, nextPubkey, prevPos, nextPos) {
    const hasGraph = Object.keys(affinityMap).length > 0;
    const hasAdj = prevPubkey || nextPubkey;

    // Strategy 1: neighbor-graph edge weights (sum of prev + next)
    if (hasGraph && hasAdj) {
      const scored = candidates.map(function(c) {
        let s = 0;
        if (prevPubkey) s += getAffinity(prevPubkey, c.pubkey);
        if (nextPubkey) s += getAffinity(nextPubkey, c.pubkey);
        return { candidate: c, edgeScore: s };
      });
      const withEdges = scored.filter(function(s) { return s.edgeScore > 0; });
      if (withEdges.length > 0) {
        withEdges.sort(function(a, b) { return b.edgeScore - a.edgeScore; });
        _traceMultiCandidate(candidates, scored, withEdges[0].candidate, 'graph');
        return withEdges[0].candidate;
      }
    }

    // Strategy 2/3: geographic — centroid of prev+next, or single anchor
    let anchorLat = null, anchorLon = null, anchorCount = 0;
    if (prevPos && prevPos.lat != null && prevPos.lon != null) {
      anchorLat = (anchorLat || 0) + prevPos.lat;
      anchorLon = (anchorLon || 0) + prevPos.lon;
      anchorCount++;
    }
    if (nextPos && nextPos.lat != null && nextPos.lon != null) {
      anchorLat = (anchorLat || 0) + nextPos.lat;
      anchorLon = (anchorLon || 0) + nextPos.lon;
      anchorCount++;
    }
    if (anchorCount > 0) {
      anchorLat /= anchorCount;
      anchorLon /= anchorCount;
      const geoScored = candidates.map(function(c) {
        const d = (c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0))
          ? haversineKm(c.lat, c.lon, anchorLat, anchorLon) : 999999;
        return { candidate: c, distKm: d };
      });
      geoScored.sort(function(a, b) { return a.distKm - b.distKm; });
      _traceMultiCandidate(candidates, geoScored, geoScored[0].candidate, 'centroid');
      return geoScored[0].candidate;
    }

    // Strategy 4: no context — return first candidate
    _traceMultiCandidate(candidates, null, candidates[0], 'fallback');
    return candidates[0];
  }

  /** Dev-mode console trace for multi-candidate picks */
  function _traceMultiCandidate(candidates, scored, chosen, method) {
    if (typeof console === 'undefined' || !console.debug) return;
    if (candidates.length < 2) return;
    try {
      const prefix = candidates[0].pubkey ? candidates[0].pubkey.slice(0, 2) : '??';
      const scoreSummary = scored ? scored.map(function(s) {
        const pk = (s.candidate || s).pubkey || '?';
        const val = s.edgeScore != null ? s.edgeScore : (s.distKm != null ? s.distKm + 'km' : '?');
        return pk.slice(0, 8) + ':' + val;
      }) : [];
      console.debug('[hop-resolver] hash=' + prefix + ' candidates=' + candidates.length +
        ' scored=[' + scoreSummary.join(',') + '] chose=' + (chosen.pubkey || '?').slice(0, 8) +
        ' method=' + method);
    } catch(e) { /* trace is best-effort */ }
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
   * @param {string|null} observerId - Observer that heard the packet
   * @param {Object} [opts] - { directHops: hop prefixes the observer heard directly }.
   *   Without it, hops is one path and its last hop is the direct one.
   * @returns {Object} resolved map keyed by hop prefix
   */
  function resolve(hops, originLat, originLon, observerLat, observerLon, observerId, opts) {
    if (!hops || !hops.length) return {};

    // Determine observer's IATA for regional filtering
    const packetIata = observerId ? observerIataMap[observerId] : null;
    // #2098 — only a hop the observer heard directly has to be within its
    // region. Earlier hops can be anywhere the mesh reaches, so the region
    // says nothing about them.
    const direct = new Set(((opts && opts.directHops) || [hops[hops.length - 1]]).map(h => String(h).toLowerCase()));

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
        const regionCheck = packetIata && direct.has(h) ? nodeInRegion(c, packetIata) : null;
        resolved[hop] = { name: c.name, pubkey: c.public_key,
          candidates: [{ name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon, regional: regionCheck ? regionCheck.near : false, filterMethod: regionCheck ? regionCheck.method : 'none', distKm: regionCheck ? regionCheck.distKm : undefined }],
          conflicts: [] };
      } else {
        // Multiple candidates — apply geo regional filtering
        const checked = allCandidates.map(c => {
          const r = packetIata && direct.has(h) ? nodeInRegion(c, packetIata) : null;
          return { ...c, regional: r ? r.near : false, filterMethod: r ? r.method : 'none', distKm: r ? r.distKm : undefined };
        });
        const regional = checked.filter(c => c.regional);
        regional.sort((a, b) => (a.distKm || 9999) - (b.distKm || 9999));
        const candidates = regional.length > 0 ? regional : checked;
        // A global fallback is a hop checked against the region with no candidate
        // in it. An earlier hop was not checked at all (#2098): regionSkipped
        // keeps its badge counting every candidate without the fallback label.
        const globalFallback = regional.length === 0 && checked.length > 0 && packetIata != null && direct.has(h);
        const regionSkipped = packetIata != null && !direct.has(h);

        const conflicts = candidates.map(c => ({
          name: c.name, pubkey: c.public_key, lat: c.lat, lon: c.lon,
          regional: c.regional, filterMethod: c.filterMethod, distKm: c.distKm
        }));

        if (candidates.length === 1) {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            candidates: conflicts, conflicts, globalFallback, regionSkipped };
        } else {
          resolved[hop] = { name: candidates[0].name, pubkey: candidates[0].public_key,
            ambiguous: true, candidates: conflicts, conflicts, globalFallback, regionSkipped,
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

    // Combined disambiguation: resolve ambiguous hops using both neighbors.
    // We iterate until no more hops can be resolved (handles cascading dependencies).
    const originPos = (originLat != null && originLon != null) ? { lat: originLat, lon: originLon } : null;
    const observerPos = (observerLat != null && observerLon != null) ? { lat: observerLat, lon: observerLon } : null;

    let changed = true;
    let maxIter = hops.length + 1; // prevent infinite loops
    while (changed && maxIter-- > 0) {
      changed = false;
      for (let i = 0; i < hops.length; i++) {
        const hop = hops[i];
        if (hopPositions[hop]) continue; // already resolved
        const r = resolved[hop];
        if (!r || !r.ambiguous) continue;
        const withLoc = r.candidates.filter(c => c.lat != null && c.lon != null && !(c.lat === 0 && c.lon === 0));
        if (!withLoc.length) continue;

        // Find prev resolved neighbor
        let prevPubkey = null, prevPos = null;
        for (let j = i - 1; j >= 0; j--) {
          if (hopPositions[hops[j]]) {
            prevPos = hopPositions[hops[j]];
            prevPubkey = resolved[hops[j]] ? resolved[hops[j]].pubkey : null;
            break;
          }
        }
        if (!prevPos && originPos) prevPos = originPos;

        // Find next resolved neighbor
        let nextPubkey = null, nextPos = null;
        for (let j = i + 1; j < hops.length; j++) {
          if (hopPositions[hops[j]]) {
            nextPos = hopPositions[hops[j]];
            nextPubkey = resolved[hops[j]] ? resolved[hops[j]].pubkey : null;
            break;
          }
        }
        if (!nextPos && observerPos) nextPos = observerPos;

        // Skip if we have zero context (wait for a later iteration or neighbor resolution)
        if (!prevPubkey && !nextPubkey && !prevPos && !nextPos) continue;

        const picked = pickByAffinity(withLoc, prevPubkey, nextPubkey, prevPos, nextPos);
        r.name = picked.name;
        r.pubkey = picked.pubkey;
        hopPositions[hop] = { lat: picked.lat, lon: picked.lon };
        changed = true;
      }
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

  // ── Whole-path inference for one observation ──
  //
  // resolve() picks each hop greedily and keys its result by prefix, so a
  // prefix seen twice in one path gets one node, and nothing checks that the
  // chosen hops form a path a packet could have taken. inferPath() scores the
  // path as a whole instead, using two firmware facts (Mesh::routeRecvPacket,
  // wasSeen): a relayer appends its own hash, so the last hop is the one the
  // observer heard directly; and a node forwards a packet once, so it appears
  // at most once in a path.
  //
  // Each hop is either one of the located repeaters sharing its prefix, or
  // "unlisted": a node missing from the list or without GPS. With 1-byte
  // prefixes the second is common, and naming it beats drawing a 300 km hop.
  // The cheapest sequence is found by dynamic programming over (hop, node),
  // and each hop's confidence comes from comparing the best path through each
  // of its alternatives.
  //
  // TODO(customizer): expose these once the detail view settles (AGENTS rule 8).
  const PATH_MODEL = {
    typicalHopKm: 50,  // a hop this long costs 1
    maxHopKm: 100,     // per hop; past it the cost climbs 4× as fast
    unlistedCost: 1.6, // a hop whose node is not located
    linkFactor: 0.5,   // a neighbour-graph link makes a hop this much cheaper
    temperature: 0.7,  // turns cost differences into confidence
    regionSlackKm: 50, // an IATA centre stands in for the observer, loosely
  };

  function isLocated(n) {
    return n != null && n.lat != null && n.lon != null &&
      Number.isFinite(Number(n.lat)) && Number.isFinite(Number(n.lon)) &&
      !(Number(n.lat) === 0 && Number(n.lon) === 0);
  }

  // A point on the path: a candidate node or an anchor, with its radians
  // cached for the cost function.
  function pathPoint(lat, lon, node, slackKm) {
    const rlat = Number(lat) * Math.PI / 180;
    return { rlat, rlon: Number(lon) * Math.PI / 180, cos: Math.cos(rlat),
      node: node || null, pubkey: node ? node.public_key : null, slackKm: slackKm || 0 };
  }

  // Equirectangular distance — within a mesh's extent it agrees with
  // haversine to well under a percent, at a fraction of the cost.
  function pointKm(a, b) {
    const x = (b.rlon - a.rlon) * (a.cos + b.cos) / 2;
    const y = b.rlat - a.rlat;
    return 6371 * Math.sqrt(x * x + y * y);
  }

  // Hops further apart than this are not compared directly. Past it the
  // distance limit (MAX_GAP × maxHopKm) spans any regional mesh, so a longer
  // run of unlisted hops costs a flat longBase() instead, which a running
  // minimum handles. This keeps the work at O(hops × MAX_GAP × cands²).
  const MAX_GAP = 8;

  // Cost of the stretch from a (at one position) to b (gap positions later):
  // the unlisted hops between them, plus the distance when both are located.
  function spanCost(a, b, gap) {
    const c = (gap - 1) * PATH_MODEL.unlistedCost;
    if (!a || !b) return c;
    if (gap > MAX_GAP) return c + longBase();
    const d = Math.max(0, pointKm(a, b) - a.slackKm - b.slackKm);
    const lim = gap * PATH_MODEL.maxHopKm;
    let geo = d <= lim ? d / PATH_MODEL.typicalHopKm
      : (lim + 4 * (d - lim)) / PATH_MODEL.typicalHopKm;
    if (gap === 1 && a.pubkey && b.pubkey && getAffinity(a.pubkey, b.pubkey) > 0) geo *= PATH_MODEL.linkFactor;
    return c + geo;
  }

  // The flat part of a span longer than MAX_GAP between two located points:
  // span(p → q) = longBase() + (q - p - 1) × unlistedCost.
  function longBase() {
    return PATH_MODEL.maxHopKm / PATH_MODEL.typicalHopKm;
  }

  // steps[p][g][j * |C[p+g]| + l] = cost from candidate j at hop p to
  // candidate l at hop p+g. Infinity for the same node twice. Computed once,
  // shared by every solve and the confidence pass.
  function buildSteps(C) {
    const L = C.length;
    const steps = [];
    for (let p = 0; p < L; p++) {
      steps.push([null]);
      for (let g = 1; g <= MAX_GAP && p + g < L; g++) {
        const A = C[p], Bq = C[p + g], row = new Float64Array(A.length * Bq.length);
        for (let j = 0; j < A.length; j++) {
          for (let l = 0; l < Bq.length; l++) {
            row[j * Bq.length + l] = A[j].pubkey === Bq[l].pubkey ? Infinity : spanCost(A[j], Bq[l], g);
          }
        }
        steps[p].push(row);
      }
    }
    return steps;
  }

  // Best path cost forward (F) and backward (B) through every (hop, candidate).
  // bans[i] holds pubkeys excluded at hop i, to keep a node to one position.
  function solvePath(C, steps, start, end, bans) {
    const L = C.length, U = PATH_MODEL.unlistedCost, LONG = longBase();
    const F = C.map(c => new Float64Array(c.length).fill(Infinity));
    const B = C.map(c => new Float64Array(c.length).fill(Infinity));
    const backP = C.map(c => new Int32Array(c.length).fill(-1));
    const backK = C.map(c => new Int32Array(c.length).fill(-1));
    const isBanned = (i, k) => bans[i] !== null && bans[i].has(C[i][k].pubkey);

    // Forward. longF tracks min(F[p][j] - p·U) over hops more than MAX_GAP back.
    let longF = Infinity, longP = -1, longK = -1;
    for (let i = 0; i < L; i++) {
      const old = i - MAX_GAP - 1;
      if (old >= 0) {
        for (let j = 0; j < C[old].length; j++) {
          const v = F[old][j] - old * U;
          if (v < longF) { longF = v; longP = old; longK = j; }
        }
      }
      const K = C[i].length;
      for (let k = 0; k < K; k++) {
        if (isBanned(i, k)) continue;
        let best = spanCost(start, C[i][k], i + 1), bp = -1, bk = -1;
        for (let g = 1; g <= MAX_GAP && g <= i; g++) {
          const p = i - g, Fp = F[p], row = steps[p][g];
          for (let j = 0; j < Fp.length; j++) {
            const v = Fp[j] + row[j * K + k];
            if (v < best) { best = v; bp = p; bk = j; }
          }
        }
        const vLong = longF + (i - 1) * U + LONG;
        if (vLong < best) { best = vLong; bp = longP; bk = longK; }
        F[i][k] = best; backP[i][k] = bp; backK[i][k] = bk;
      }
    }

    // Backward. longB tracks min(B[q][l] + q·U) over hops more than MAX_GAP on.
    let longB = Infinity;
    for (let i = L - 1; i >= 0; i--) {
      const old = i + MAX_GAP + 1;
      if (old < L) {
        for (let l = 0; l < C[old].length; l++) longB = Math.min(longB, B[old][l] + old * U);
      }
      for (let k = 0; k < C[i].length; k++) {
        if (isBanned(i, k)) continue;
        let best = Math.min(spanCost(C[i][k], end, L - i), longB - (i + 1) * U + LONG);
        for (let g = 1; g <= MAX_GAP && i + g < L; g++) {
          const Bq = B[i + g], row = steps[i][g], base = k * Bq.length;
          for (let l = 0; l < Bq.length; l++) {
            const v = Bq[l] + row[base + l];
            if (v < best) best = v;
          }
        }
        B[i][k] = best;
      }
    }

    let total = spanCost(start, end, L + 1), lastP = -1, lastK = -1;
    for (let i = 0; i < L; i++) {
      for (let k = 0; k < C[i].length; k++) {
        const v = F[i][k] + spanCost(C[i][k], end, L - i);
        if (v < total) { total = v; lastP = i; lastK = k; }
      }
    }
    const assign = new Int32Array(L).fill(-1);
    for (let p = lastP, k = lastK; p >= 0;) {
      assign[p] = k;
      const np = backP[p][k], nk = backK[p][k];
      p = np; k = nk;
    }
    return { F, B, total, assign };
  }

  // Lowest total cost of any path leaving hop i unlisted: some span from a
  // point before i (or the start) to a point after it (or the end), with
  // every hop between unlisted.
  function unlistedMarginals(C, steps, start, end, F, B) {
    const L = C.length, U = PATH_MODEL.unlistedCost, LONG = longBase();
    const mu = new Float64Array(L).fill(Infinity);
    const cover = (p, q, v) => { for (let i = p + 1; i < q; i++) if (v < mu[i]) mu[i] = v; };
    const minOf = arr => arr.reduce((m, v) => Math.min(m, v), Infinity);

    for (let p = -1; p < L - 1; p++) {
      for (let g = 2; g <= MAX_GAP && p + g <= L; g++) {
        const q = p + g;
        let best = Infinity;
        if (p < 0 && q >= L) {
          best = spanCost(start, end, g);
        } else if (p < 0) {
          for (let l = 0; l < C[q].length; l++) best = Math.min(best, spanCost(start, C[q][l], g) + B[q][l]);
        } else if (q >= L) {
          for (let j = 0; j < C[p].length; j++) best = Math.min(best, F[p][j] + spanCost(C[p][j], end, g));
        } else {
          const row = steps[p][g], K = C[q].length;
          for (let j = 0; j < C[p].length; j++) {
            for (let l = 0; l < K; l++) best = Math.min(best, F[p][j] + row[j * K + l] + B[q][l]);
          }
        }
        cover(p, q, best);
      }
    }
    // Spans longer than MAX_GAP cost the same whichever nodes bound them, as
    // in spanCost(): the flat part only applies between two located points.
    const Fmin = F.map(minOf), Bmin = B.map(minOf);
    for (let p = -1; p < L; p++) {
      for (let q = p + MAX_GAP + 1; q <= L; q++) {
        const flat = (p < 0 && !start) || (q >= L && !end) ? 0 : LONG;
        cover(p, q, (p < 0 ? 0 : Fmin[p]) + (q - p - 1) * U + flat + (q >= L ? 0 : Bmin[q]));
      }
    }
    return mu;
  }

  /**
   * Infer the most likely node behind every hop of one observation's path.
   *
   * @param {string[]} hops - hex prefixes, in path order (last = heard by observer)
   * @param {Object} [opts]
   * @param {{lat,lon}} [opts.observer] - the observer's position
   * @param {string} [opts.observerId] - falls back to its IATA centre when the
   *   observer reports no position (or 0,0)
   * @param {{lat,lon}} [opts.origin] - the sender's position, when known
   * @param {Array<string|null>} [opts.resolvedPath] - server-resolved pubkeys
   *   aligned with hops; a non-null entry restricts that hop to that node
   * @returns {Array} one entry per hop: { hop, name, pubkey, lat, lon,
   *   located, unlisted, unreliable, ambiguous, confidence, candidates,
   *   conflicts } — candidates sorted by confidence, located first; one the
   *   path uses at another hop carries usedAt (that hop's index)
   */
  function inferPath(hops, opts) {
    if (!hops || !hops.length) return [];
    opts = opts || {};
    const L = hops.length;
    const rp = Array.isArray(opts.resolvedPath) && opts.resolvedPath.length === L ? opts.resolvedPath : null;

    let end = null;
    if (isLocated(opts.observer)) {
      end = pathPoint(opts.observer.lat, opts.observer.lon);
    } else if (opts.observerId) {
      const center = iataCoords[observerIataMap[opts.observerId]];
      if (isLocated(center)) end = pathPoint(center.lat, center.lon, null, PATH_MODEL.regionSlackKm);
    }
    const start = isLocated(opts.origin) ? pathPoint(opts.origin.lat, opts.origin.lon) : null;

    const C = [], unplaced = [];
    for (let i = 0; i < L; i++) {
      const hinted = rp && rp[i] ? pubkeyIdx[String(rp[i]).toLowerCase()] : null;
      const nodes = hinted ? [hinted] : (prefixIdx[String(hops[i]).toLowerCase()] || []);
      C.push(nodes.filter(isLocated).map(n => pathPoint(n.lat, n.lon, n)));
      unplaced.push(nodes.filter(n => !isLocated(n)));
    }

    // Keep each node to one position: when the best path uses a node twice,
    // ban it from whichever of the two positions costs less to give up.
    const bans = C.map(() => null);
    const banned = (i, pubkey) => bans.map((s, j) => j === i ? new Set([...(s || []), pubkey]) : s);
    const steps = buildSteps(C);
    let sol = solvePath(C, steps, start, end, bans);
    for (let guard = 0; guard < L; guard++) {
      const seenAt = new Map();
      let dup = null;
      for (let i = 0; i < L && !dup; i++) {
        if (sol.assign[i] < 0) continue;
        const key = C[i][sol.assign[i]].pubkey;
        if (seenAt.has(key)) dup = [seenAt.get(key), i, key];
        else seenAt.set(key, i);
      }
      if (!dup) break;
      const banFirst = banned(dup[0], dup[2]), banSecond = banned(dup[1], dup[2]);
      const s1 = solvePath(C, steps, start, end, banFirst), s2 = solvePath(C, steps, start, end, banSecond);
      const firstWins = s1.total <= s2.total;
      bans.splice(0, L, ...(firstWins ? banFirst : banSecond));
      sol = firstWins ? s1 : s2;
    }

    // For confidence, a node the path uses at one hop is no alternative at
    // another. The best path stays feasible, so the assignment is unchanged.
    const usedAt = new Map();
    sol.assign.forEach((k, i) => { if (k >= 0) usedAt.set(C[i][k].pubkey, i); });
    const confidenceBans = C.map((c, i) => {
      const s = new Set(bans[i] || []);
      for (const pt of c) {
        const j = usedAt.get(pt.pubkey);
        if (j !== undefined && j !== i) s.add(pt.pubkey);
      }
      return s.size ? s : null;
    });
    sol = solvePath(C, steps, start, end, confidenceBans);
    const mu = unlistedMarginals(C, steps, start, end, sol.F, sol.B);
    const T = PATH_MODEL.temperature;
    return hops.map((hop, i) => {
      const costs = Array.from(C[i], (_, k) => sol.F[i][k] + sol.B[i][k]);
      const lowest = Math.min(mu[i], ...costs);
      const weights = costs.map(c => c === Infinity ? 0 : Math.exp(-(c - lowest) / T));
      const wUnlisted = mu[i] === Infinity ? 0 : Math.exp(-(mu[i] - lowest) / T);
      const sum = weights.reduce((s, w) => s + w, wUnlisted) || 1;

      const candidates = C[i].map((pt, k) => {
        const c = { name: pt.node.name, pubkey: pt.pubkey, lat: pt.node.lat, lon: pt.node.lon,
          located: true, confidence: weights[k] / sum };
        const j = usedAt.get(pt.pubkey);
        if (j !== undefined && j !== i) c.usedAt = j; // the path has it at hop j
        return c;
      }).sort((a, b) => b.confidence - a.confidence);
      for (const n of unplaced[i]) {
        candidates.push({ name: n.name, pubkey: n.public_key, lat: null, lon: null, located: false, confidence: null });
      }

      const k = sol.assign[i];
      const entry = {
        hop, name: null, pubkey: null, lat: null, lon: null,
        located: false, unlisted: k < 0, unreliable: false,
        ambiguous: C[i].length > 1,
        confidence: k < 0 ? wUnlisted / sum : weights[k] / sum,
        candidates, conflicts: candidates,
      };
      if (k >= 0) {
        const n = C[i][k].node;
        Object.assign(entry, { name: n.name, pubkey: n.public_key, lat: n.lat, lon: n.lon, located: true });
      } else if (C[i].length) {
        // Located nodes share this prefix, but none fits where the hop is.
        entry.unreliable = true;
      } else if (unplaced[i].length === 1) {
        // The one known node with this prefix has no position to check.
        entry.name = unplaced[i][0].name;
        entry.pubkey = unplaced[i][0].public_key;
      }
      return entry;
    });
  }

  /**
   * Check if the resolver has been initialized with nodes.
   */
  function ready() {
    return nodesList.length > 0;
  }

  /**
   * Load neighbor-graph affinity data.
   * @param {Object} graph - { edges: [{source, target, score, weight}, ...] }
   */
  function setAffinity(graph) {
    affinityMap = {};
    if (!graph || !graph.edges) return;
    for (const e of graph.edges) {
      if (!affinityMap[e.source]) affinityMap[e.source] = {};
      affinityMap[e.source][e.target] = e.score || e.weight || 1;
      if (!affinityMap[e.target]) affinityMap[e.target] = {};
      affinityMap[e.target][e.source] = e.score || e.weight || 1;
    }
  }

  /**
   * Get the affinity score between two pubkeys (0 if not neighbors).
   */
  function getAffinity(pubkeyA, pubkeyB) {
    if (!pubkeyA || !pubkeyB || !affinityMap[pubkeyA]) return 0;
    return affinityMap[pubkeyA][pubkeyB] || 0;
  }

  // #1864: O(1) node-name lookup by FULL pubkey (64-char hex). Returns the
  // node name when a node with this exact public key is known, else null.
  // Used to resolve ANON_REQ source pubkeys to a display name.
  function nameForKey(pubkey) {
    if (!pubkey) return null;
    const n = pubkeyIdx[String(pubkey).toLowerCase()];
    return n && n.name ? n.name : null;
  }

  // #1868: O(1) node lookup by FULL pubkey (64 hex) or by the 8-byte prefix
  // (16 hex) a CONTROL DISCOVER_RESP carries when the request set prefix_only.
  // Returns the node, or null when unknown or when the prefix is ambiguous.
  function nodeForKey(key) {
    if (!key) return null;
    const k = String(key).toLowerCase();
    if (k.length === 16) return pubkeyPrefix8Idx[k] || null;
    return pubkeyIdx[k] || null;
  }

  /**
   * Resolve hops using server-provided resolved_path (full pubkeys).
   * Returns the same format as resolve() — { [hop]: { name, pubkey, ... } }.
   * resolved_path is an array aligned with path_json: each element is a
   * 64-char lowercase hex pubkey or null. Skips entries that are null.
   */
  function resolveFromServer(hops, resolvedPath) {
    if (!hops || !resolvedPath || hops.length !== resolvedPath.length) return {};
    const result = {};
    for (let i = 0; i < hops.length; i++) {
      const hop = hops[i];
      const pubkey = resolvedPath[i];
      if (!pubkey) continue; // null = unresolved, leave for client-side fallback
      // O(1) lookup via pubkeyIdx built during init()
      const node = pubkeyIdx[pubkey.toLowerCase()] || null;
      result[hop] = {
        name: node ? node.name : pubkey.slice(0, 8),
        pubkey: pubkey,
        candidates: node ? [{ name: node.name, pubkey: pubkey, lat: node.lat, lon: node.lon }] : [],
        conflicts: []
      };
    }
    return result;
  }

  return { init: init, resolve: resolve, inferPath: inferPath, resolveFromServer: resolveFromServer, ready: ready, haversineKm: haversineKm, setAffinity: setAffinity, getAffinity: getAffinity, nameForKey: nameForKey, nodeForKey: nodeForKey };
})();
