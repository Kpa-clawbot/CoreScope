package main

import (
	"testing"
	"time"
)

// newTestStore creates a minimal PacketStore for cache invalidation testing.
func newTestStore(t *testing.T) *PacketStore {
	t.Helper()
	return &PacketStore{
		rfCache:      make(map[string]*cachedResult),
		topoCache:    make(map[string]*cachedResult),
		hashCache:    make(map[string]*cachedResult),
		chanCache:    make(map[string]*cachedResult),
		distCache:    make(map[string]*cachedResult),
		subpathCache: make(map[string]*cachedResult),
		rfCacheTTL:   15 * time.Second,
	}
}

// populateAllCaches fills every analytics cache with a dummy entry so tests
// can verify which caches are cleared and which are preserved.
func populateAllCaches(s *PacketStore) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	dummy := &cachedResult{data: map[string]interface{}{"test": true}, expiresAt: time.Now().Add(time.Hour)}
	s.rfCache["global"] = dummy
	s.topoCache["global"] = dummy
	s.hashCache["global"] = dummy
	s.chanCache["global"] = dummy
	s.distCache["global"] = dummy
	s.subpathCache["global"] = dummy
}

// cachePopulated returns which caches still have their "global" entry.
func cachePopulated(s *PacketStore) map[string]bool {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return map[string]bool{
		"rf":      len(s.rfCache) > 0,
		"topo":    len(s.topoCache) > 0,
		"hash":    len(s.hashCache) > 0,
		"chan":     len(s.chanCache) > 0,
		"dist":    len(s.distCache) > 0,
		"subpath": len(s.subpathCache) > 0,
	}
}

func TestInvalidateCachesFor_Eviction(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{eviction: true})

	pop := cachePopulated(s)
	for name, has := range pop {
		if has {
			t.Errorf("eviction should clear %s cache", name)
		}
	}
}

func TestInvalidateCachesFor_NewObservationsOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewObservations: true})

	pop := cachePopulated(s)
	if pop["rf"] {
		t.Error("rf cache should be cleared on new observations")
	}
	// These should be preserved
	for _, name := range []string{"topo", "hash", "chan", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on observation-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_NewTransmissionsOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewTransmissions: true})

	pop := cachePopulated(s)
	if pop["hash"] {
		t.Error("hash cache should be cleared on new transmissions")
	}
	for _, name := range []string{"rf", "topo", "chan", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on transmission-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_ChannelDataOnly(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasChannelData: true})

	pop := cachePopulated(s)
	if pop["chan"] {
		t.Error("chan cache should be cleared on channel data")
	}
	for _, name := range []string{"rf", "topo", "hash", "dist", "subpath"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on channel-data-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_NewPaths(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{hasNewPaths: true})

	pop := cachePopulated(s)
	for _, name := range []string{"topo", "dist", "subpath"} {
		if pop[name] {
			t.Errorf("%s cache should be cleared on new paths", name)
		}
	}
	for _, name := range []string{"rf", "hash", "chan"} {
		if !pop[name] {
			t.Errorf("%s cache should NOT be cleared on path-only ingest", name)
		}
	}
}

func TestInvalidateCachesFor_CombinedFlags(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	// Simulate a typical ingest: new transmissions with observations but no GRP_TXT
	s.invalidateCachesFor(cacheInvalidation{
		hasNewObservations:  true,
		hasNewTransmissions: true,
		hasNewPaths:         true,
	})

	pop := cachePopulated(s)
	// rf, topo, hash, dist, subpath should all be cleared
	for _, name := range []string{"rf", "topo", "hash", "dist", "subpath"} {
		if pop[name] {
			t.Errorf("%s cache should be cleared with combined flags", name)
		}
	}
	// chan should be preserved (no GRP_TXT)
	if !pop["chan"] {
		t.Error("chan cache should NOT be cleared without hasChannelData flag")
	}
}

func TestInvalidateCachesFor_NoFlags(t *testing.T) {
	s := newTestStore(t)
	populateAllCaches(s)

	s.invalidateCachesFor(cacheInvalidation{})

	pop := cachePopulated(s)
	for name, has := range pop {
		if !has {
			t.Errorf("%s cache should be preserved when no flags are set", name)
		}
	}
}
