package main

import (
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// meshMapperCacheState holds the cached MeshMapper API response.
type meshMapperCacheState struct {
	mu        sync.RWMutex
	data      []byte
	fetchedAt time.Time
}

func (s *Server) handleMeshMapperCoverage(w http.ResponseWriter, r *http.Request) {
	apiKey := s.cfg.MeshMapperAPIKey()
	if apiKey == "" {
		writeError(w, http.StatusServiceUnavailable, "meshmapper_not_configured")
		return
	}

	ttl := s.cfg.MeshMapperCacheTTL()
	apiURL := s.cfg.MeshMapperAPIUrl()

	// Check cache.
	s.meshMapperCache.mu.RLock()
	if s.meshMapperCache.data != nil && time.Since(s.meshMapperCache.fetchedAt) < ttl {
		cached := make([]byte, len(s.meshMapperCache.data))
		copy(cached, s.meshMapperCache.data)
		s.meshMapperCache.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(cached)
		return
	}
	s.meshMapperCache.mu.RUnlock()

	// Fetch from MeshMapper API.
	req, err := http.NewRequestWithContext(r.Context(), "GET", apiURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "meshmapper_unavailable")
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[meshmapper] fetch error: %v", err)
		writeError(w, http.StatusBadGateway, "meshmapper_unavailable")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("[meshmapper] upstream HTTP %d", resp.StatusCode)
		writeError(w, http.StatusBadGateway, "meshmapper_unavailable")
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB limit
	if err != nil {
		log.Printf("[meshmapper] read error: %v", err)
		writeError(w, http.StatusBadGateway, "meshmapper_unavailable")
		return
	}

	s.meshMapperCache.mu.Lock()
	s.meshMapperCache.data = body
	s.meshMapperCache.fetchedAt = time.Now()
	s.meshMapperCache.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
