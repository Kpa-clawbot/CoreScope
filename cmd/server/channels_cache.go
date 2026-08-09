package main

// Handler-level cache for GET /api/channels (?includeEncrypted=true included).
// Encrypted merge bypassed the DB TTL; concurrent cold misses stampeded SQLite.
// Mirrors observers_cache.go (#1481 / #1483).

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const channelsCacheTTL = 90 * time.Second

func (s *Server) effectiveChannelsCacheTTL() time.Duration {
	if s.cfg != nil && s.cfg.ChannelsCache != nil && s.cfg.ChannelsCache.TTLSeconds > 0 {
		return time.Duration(s.cfg.ChannelsCache.TTLSeconds) * time.Second
	}
	return channelsCacheTTL
}

func channelsCacheKey(region string, includeEncrypted bool) string {
	enc := "0"
	if includeEncrypted {
		enc = "1"
	}
	return fmt.Sprintf("channels|%s|%s", region, enc)
}

type channelsCacheEntry struct {
	resp ChannelListResponse
	at   time.Time
}

type channelsCacheField struct {
	entries sync.Map // string → *channelsCacheEntry
	sf      singleflight.Group
	fillCount atomic.Int64 // test-only: counts singleflight wins
}

func (s *Server) isChannelsListCacheStale(cachedAt time.Time) bool {
	if cachedAt.IsZero() {
		return true
	}
	return time.Since(cachedAt) >= s.effectiveChannelsCacheTTL()
}

func (s *Server) fetchChannelsList(region string, includeEncrypted bool) (ChannelListResponse, error) {
	if s.db != nil {
		channels, err := s.db.GetChannels(region)
		if err != nil {
			return ChannelListResponse{}, err
		}
		if includeEncrypted {
			encrypted, err := s.db.GetEncryptedChannels(region)
			if err != nil {
				log.Printf("WARN GetEncryptedChannels: %v", err)
				return ChannelListResponse{}, err // don't cache a partial encrypted merge
			}
			channels = append(channels, encrypted...)
		}
		if channels == nil {
			channels = []map[string]interface{}{}
		}
		return ChannelListResponse{Channels: channels}, nil
	}
	if s.store != nil {
		channels := s.store.GetChannels(region)
		if includeEncrypted {
			channels = append(channels, s.store.GetEncryptedChannels(region)...)
		}
		if channels == nil {
			channels = []map[string]interface{}{}
		}
		return ChannelListResponse{Channels: channels}, nil
	}
	return ChannelListResponse{Channels: []map[string]interface{}{}}, nil
}
