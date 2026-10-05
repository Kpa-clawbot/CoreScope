package main

import (
	"strings"
	"time"
)

// regionNodesTTL bounds how stale a region's node set may be. Nodes join a
// region at advert cadence, so 30s matches the other region and area caches.
const regionNodesTTL = 30 * time.Second

// regionNodesCacheMax bounds the cache. Its keys come from the client's
// region parameter, so without a bound a client could grow it at will; a
// deployment has a handful of real regions.
const regionNodesCacheMax = 64

type regionNodesEntry struct {
	keys []string
	at   time.Time
}

// advertPubkey returns the originator pubkey of an ADVERT transmission, or ""
// for anything else or an advert whose decoded JSON carries none.
func advertPubkey(tx *StoreTx) string {
	if tx == nil || tx.PayloadType == nil || *tx.PayloadType != PayloadADVERT || tx.DecodedJSON == "" {
		return ""
	}
	d := tx.ParsedDecoded()
	if d == nil {
		return ""
	}
	if v, ok := d["pubKey"].(string); ok && v != "" {
		return v
	}
	v, _ := d["public_key"].(string)
	return v
}

// RegionNodePubkeys returns the pubkeys of nodes with at least one advert heard
// by an observer in region (comma-separated IATA codes, case- and
// space-insensitive), over the adverts the store holds. ok is false when the
// region names no code, or there is no database to resolve observers from, so
// the caller applies the SQL filter or none.
//
// Observers are matched by ID through resolveRegionObservers, i.e. against
// the observers table as it is now, like every other store region filter.
// The IATA copied onto each observation at ingest goes stale when an operator
// changes an observer's code.
//
// #2101: this replaces a SQL subquery that joined every advert to all of its
// observations. On a 16.5M-observation database one such count took 155s,
// and /api/nodes ran it twice per request.
//
// Lock order: regionNodesMu and s.mu are never held together. The scan takes
// s.mu (read) on its own, so it nests under nothing (see the ordering note at
// the top of store.go).
func (s *PacketStore) RegionNodePubkeys(region string) (keys []string, ok bool) {
	codes := normalizeRegionCodes(region)
	if len(codes) == 0 || s.db == nil {
		return nil, false
	}
	cacheKey := strings.Join(codes, ",")

	s.regionNodesMu.Lock()
	if e, hit := s.regionNodesCache[cacheKey]; hit && time.Since(e.at) < regionNodesTTL {
		s.regionNodesMu.Unlock()
		return e.keys, true
	}
	s.regionNodesMu.Unlock()

	// Its own mutex and a 30s cache; taken before s.mu, never under it.
	inRegion := s.resolveRegionObservers(cacheKey)
	seen := make(map[string]bool, 256)
	keys = make([]string, 0, 256)

	s.mu.RLock()
	for _, tx := range s.byPayloadType[PayloadADVERT] {
		heard := false
		for _, obs := range tx.Observations {
			if inRegion[obs.ObserverID] {
				heard = true
				break
			}
		}
		if !heard {
			continue
		}
		// Parsed only for adverts heard in the region, and cached on the tx.
		if pk := advertPubkey(tx); pk != "" && !seen[pk] {
			seen[pk] = true
			keys = append(keys, pk)
		}
	}
	s.mu.RUnlock()

	s.regionNodesMu.Lock()
	if len(s.regionNodesCache) >= regionNodesCacheMax {
		for k, e := range s.regionNodesCache {
			if time.Since(e.at) >= regionNodesTTL {
				delete(s.regionNodesCache, k)
			}
		}
		if len(s.regionNodesCache) >= regionNodesCacheMax {
			s.regionNodesCache = make(map[string]regionNodesEntry, regionNodesCacheMax)
		}
	}
	s.regionNodesCache[cacheKey] = regionNodesEntry{keys: keys, at: time.Now()}
	s.regionNodesMu.Unlock()
	return keys, true
}

// regionNodeKeys resolves a region query parameter through the packet store.
// ok is false when there is no store or the region names no code.
func (s *Server) regionNodeKeys(region string) ([]string, bool) {
	if s.store == nil {
		return nil, false
	}
	return s.store.RegionNodePubkeys(region)
}
