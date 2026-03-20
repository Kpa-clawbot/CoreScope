'use strict';

/**
 * In-memory packet store — loads all packets from SQLite on startup,
 * serves reads from RAM, writes to both RAM + SQLite.
 * Caps memory at configurable limit (default 1GB).
 */
class PacketStore {
  constructor(dbModule, config = {}) {
    this.dbModule = dbModule;  // The full db module (has .db, .insertPacket, .getPacket)
    this.db = dbModule.db;     // Raw better-sqlite3 instance for queries
    this.maxBytes = (config.maxMemoryMB || 1024) * 1024 * 1024;
    this.estPacketBytes = config.estimatedPacketBytes || 450;
    this.maxPackets = Math.floor(this.maxBytes / this.estPacketBytes);

    // Core storage: array sorted by timestamp DESC (newest first)
    this.packets = [];
    // Indexes
    this.byId = new Map();
    this.byHash = new Map();       // hash → [packet, ...]
    this.byObserver = new Map();   // observer_id → [packet, ...]
    this.byNode = new Map();       // pubkey → [packet, ...]

    this.loaded = false;
    this.stats = { totalLoaded: 0, evicted: 0, inserts: 0, queries: 0 };
  }

  /** Load all packets from SQLite into memory */
  load() {
    const t0 = Date.now();
    const rows = this.db.prepare(
      'SELECT * FROM packets ORDER BY timestamp DESC'
    ).all();

    for (const row of rows) {
      if (this.packets.length >= this.maxPackets) break;
      this._index(row);
      this.packets.push(row);
    }

    this.stats.totalLoaded = this.packets.length;
    this.loaded = true;
    const elapsed = Date.now() - t0;
    console.log(`[PacketStore] Loaded ${this.packets.length} packets in ${elapsed}ms (${Math.round(this.packets.length * this.estPacketBytes / 1024 / 1024)}MB est)`);
    return this;
  }

  /** Index a packet into all lookup maps */
  _index(pkt) {
    this.byId.set(pkt.id, pkt);

    if (pkt.hash) {
      if (!this.byHash.has(pkt.hash)) this.byHash.set(pkt.hash, []);
      this.byHash.get(pkt.hash).push(pkt);
    }

    if (pkt.observer_id) {
      if (!this.byObserver.has(pkt.observer_id)) this.byObserver.set(pkt.observer_id, []);
      this.byObserver.get(pkt.observer_id).push(pkt);
    }

    // Index by node pubkeys mentioned in decoded_json
    this._indexByNode(pkt);
  }

  /** Extract node pubkeys/names from decoded_json and index */
  _indexByNode(pkt) {
    if (!pkt.decoded_json) return;
    try {
      const decoded = JSON.parse(pkt.decoded_json);
      const keys = new Set();
      if (decoded.pubKey) keys.add(decoded.pubKey);
      if (decoded.destPubKey) keys.add(decoded.destPubKey);
      if (decoded.srcPubKey) keys.add(decoded.srcPubKey);
      for (const k of keys) {
        if (!this.byNode.has(k)) this.byNode.set(k, []);
        this.byNode.get(k).push(pkt);
      }
    } catch {}
  }

  /** Remove oldest packets when over memory limit */
  _evict() {
    while (this.packets.length > this.maxPackets) {
      const old = this.packets.pop();
      this.byId.delete(old.id);
      // Remove from hash index
      if (old.hash && this.byHash.has(old.hash)) {
        const arr = this.byHash.get(old.hash).filter(p => p.id !== old.id);
        if (arr.length) this.byHash.set(old.hash, arr); else this.byHash.delete(old.hash);
      }
      // Remove from observer index
      if (old.observer_id && this.byObserver.has(old.observer_id)) {
        const arr = this.byObserver.get(old.observer_id).filter(p => p.id !== old.id);
        if (arr.length) this.byObserver.set(old.observer_id, arr); else this.byObserver.delete(old.observer_id);
      }
      // Skip node index cleanup for eviction (expensive, low value)
      this.stats.evicted++;
    }
  }

  /** Insert a new packet (to both memory and SQLite) */
  insert(packetData) {
    const id = this.dbModule.insertPacket(packetData);
    const row = this.dbModule.getPacket(id);
    if (row) {
      this.packets.unshift(row); // newest first
      this._index(row);
      this._evict();
      this.stats.inserts++;
    }
    return id;
  }

  /** Query packets with filters — all from memory */
  query({ limit = 50, offset = 0, type, route, region, observer, hash, since, until, node, order = 'DESC' } = {}) {
    this.stats.queries++;
    let results = this.packets;

    // Use indexes for single-key filters when possible
    if (hash && !type && !route && !region && !observer && !since && !until && !node) {
      results = this.byHash.get(hash) || [];
    } else if (observer && !type && !route && !region && !hash && !since && !until && !node) {
      results = this.byObserver.get(observer) || [];
    } else if (node && !type && !route && !region && !observer && !hash && !since && !until) {
      results = this.byNode.get(node) || [];
    } else {
      // Apply filters sequentially
      if (type !== undefined) {
        const t = Number(type);
        results = results.filter(p => p.payload_type === t);
      }
      if (route !== undefined) {
        const r = Number(route);
        results = results.filter(p => p.route_type === r);
      }
      if (observer) results = results.filter(p => p.observer_id === observer);
      if (hash) results = results.filter(p => p.hash === hash);
      if (since) results = results.filter(p => p.timestamp > since);
      if (until) results = results.filter(p => p.timestamp < until);
      if (region) {
        // Need to look up observers for this region
        const regionObservers = new Set();
        try {
          const obs = this.db.prepare('SELECT id FROM observers WHERE iata = ?').all(region);
          obs.forEach(o => regionObservers.add(o.id));
        } catch {}
        results = results.filter(p => regionObservers.has(p.observer_id));
      }
      if (node) {
        // Check indexed results first, fall back to text search
        const indexed = this.byNode.get(node);
        if (indexed) {
          const idSet = new Set(indexed.map(p => p.id));
          results = results.filter(p => idSet.has(p.id));
        } else {
          // Text search fallback (node name)
          results = results.filter(p =>
            p.decoded_json && p.decoded_json.includes(node)
          );
        }
      }
    }

    const total = results.length;

    // Sort
    if (order === 'ASC') {
      results = results.slice().sort((a, b) => {
        if (a.timestamp < b.timestamp) return -1;
        if (a.timestamp > b.timestamp) return 1;
        return 0;
      });
    }
    // Default DESC — packets array is already sorted newest-first

    // Paginate
    const paginated = results.slice(Number(offset), Number(offset) + Number(limit));
    return { packets: paginated, total };
  }

  /** Query with groupByHash — aggregate packets by content hash */
  queryGrouped({ limit = 50, offset = 0, type, route, region, observer, hash, since, until, node } = {}) {
    this.stats.queries++;

    // Get filtered results first
    const { packets: filtered, total: filteredTotal } = this.query({
      limit: 999999, offset: 0, type, route, region, observer, hash, since, until, node
    });

    // Group by hash
    const groups = new Map();
    for (const p of filtered) {
      const h = p.hash || p.id.toString();
      if (!groups.has(h)) {
        groups.set(h, {
          hash: p.hash,
          observer_count: new Set(),
          count: 0,
          latest: p.timestamp,
          observer_id: p.observer_id,
          observer_name: p.observer_name,
          path_json: p.path_json,
          payload_type: p.payload_type,
          raw_hex: p.raw_hex,
          decoded_json: p.decoded_json,
        });
      }
      const g = groups.get(h);
      g.count++;
      if (p.observer_id) g.observer_count.add(p.observer_id);
      if (p.timestamp > g.latest) {
        g.latest = p.timestamp;
      }
      // Keep longest path
      if (p.path_json && (!g.path_json || p.path_json.length > g.path_json.length)) {
        g.path_json = p.path_json;
        g.raw_hex = p.raw_hex;
      }
    }

    // Sort by latest DESC, paginate
    const sorted = [...groups.values()]
      .map(g => ({ ...g, observer_count: g.observer_count.size }))
      .sort((a, b) => b.latest.localeCompare(a.latest));

    const total = sorted.length;
    const paginated = sorted.slice(Number(offset), Number(offset) + Number(limit));
    return { packets: paginated, total };
  }

  /** Get timestamps for sparkline */
  getTimestamps(since) {
    const results = [];
    for (const p of this.packets) {
      if (p.timestamp <= since) break; // sorted DESC, so we can stop early
      results.push(p.timestamp);
    }
    return results.reverse(); // return ASC
  }

  /** Get a single packet by ID */
  getById(id) {
    return this.byId.get(id) || null;
  }

  /** Get all siblings of a packet (same hash) */
  getSiblings(hash) {
    return this.byHash.get(hash) || [];
  }

  /** Memory stats */
  getStats() {
    return {
      ...this.stats,
      inMemory: this.packets.length,
      maxPackets: this.maxPackets,
      estimatedMB: Math.round(this.packets.length * this.estPacketBytes / 1024 / 1024),
      maxMB: Math.round(this.maxBytes / 1024 / 1024),
      indexes: {
        byHash: this.byHash.size,
        byObserver: this.byObserver.size,
        byNode: this.byNode.size,
      }
    };
  }
}

module.exports = PacketStore;
