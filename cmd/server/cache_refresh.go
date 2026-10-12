package main

import "sync"

// cacheRefresher runs at most one background refresh per key. The node,
// region-observer and area-node caches serve their stale value while it runs,
// so callers holding PacketStore.mu never wait on SQL (#2146). The zero value
// is ready to use.
type cacheRefresher struct {
	mu       sync.Mutex
	inFlight map[string]bool
}

// start runs fn in a new goroutine unless a refresh for key is already
// running.
func (r *cacheRefresher) start(key string, fn func()) {
	r.mu.Lock()
	if r.inFlight[key] {
		r.mu.Unlock()
		return
	}
	if r.inFlight == nil {
		r.inFlight = make(map[string]bool)
	}
	r.inFlight[key] = true
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.inFlight, key)
			r.mu.Unlock()
		}()
		fn()
	}()
}

func (r *cacheRefresher) running(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inFlight[key]
}

// beforeCacheLoad calls the test hook, if any, right before a cache runs its
// SQL.
func (s *PacketStore) beforeCacheLoad(kind string) {
	if s.cacheLoadHook != nil {
		s.cacheLoadHook(kind)
	}
}
