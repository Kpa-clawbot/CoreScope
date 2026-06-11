package main

// Known-channels catalogue cache (issue #1323).
//
// Fetches a community-maintained catalogue of hashtag channels (default:
// https://raw.githubusercontent.com/marcelverdult/meshcore-channels/main/channels-by-country.json)
// every N hours into an in-memory snapshot. Never blocks startup; never
// blocks UI on the fetch; fail-soft to last-known. No DB, no disk cache.

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

// DefaultKnownChannelsURL is the pinned upstream catalogue (channels-by-country.json).
const DefaultKnownChannelsURL = "https://raw.githubusercontent.com/marcelverdult/meshcore-channels/main/channels-by-country.json"

// DefaultKnownChannelsRefresh is the default refresh interval (24h).
const DefaultKnownChannelsRefresh = 24 * time.Hour

// KnownChannelEntry is one catalogue entry, region-stamped.
type KnownChannelEntry struct {
	Channel     string `json:"channel"`
	Description string `json:"description,omitempty"`
	Key         string `json:"key,omitempty"`
	Region      string `json:"region"`
	RegionName  string `json:"regionName,omitempty"`
}

// KnownChannelsSnapshot is the immutable parsed catalogue surfaced over /api.
type KnownChannelsSnapshot struct {
	GeneratedAt string              `json:"generatedAt,omitempty"`
	License     string              `json:"license,omitempty"`
	FetchedAt   time.Time           `json:"fetchedAt"`
	Source      string              `json:"source"`
	Entries     []KnownChannelEntry `json:"entries"`
}

// parseKnownChannelsJSON parses the upstream JSON into a snapshot.
// STUB — to be implemented in green commit.
func parseKnownChannelsJSON(raw []byte, source string, now time.Time) (*KnownChannelsSnapshot, error) {
	return &KnownChannelsSnapshot{
		FetchedAt: now,
		Source:    source,
		Entries:   []KnownChannelEntry{},
	}, nil
}

// filterSnapshotByRegion returns a copy filtered to the given region.
// STUB — to be implemented in green commit.
func filterSnapshotByRegion(snap *KnownChannelsSnapshot, region string) *KnownChannelsSnapshot {
	return snap
}

// knownChannelsCache holds the atomic snapshot pointer + config.
type knownChannelsCache struct {
	ptr     atomic.Pointer[KnownChannelsSnapshot]
	url     string
	refresh time.Duration
	client  *http.Client

	fetchCount atomic.Int64
	failCount  atomic.Int64
}

func newKnownChannelsCache(url string, refresh time.Duration) *knownChannelsCache {
	if refresh <= 0 {
		refresh = DefaultKnownChannelsRefresh
	}
	return &knownChannelsCache{
		url:     url,
		refresh: refresh,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *knownChannelsCache) load() *KnownChannelsSnapshot {
	return c.ptr.Load()
}

// fetchOnce — STUB. Returns error so tests fail until implemented.
func (c *knownChannelsCache) fetchOnce(ctx context.Context) error {
	return errors.New("not implemented")
}

func (c *knownChannelsCache) run(ctx context.Context) {
	// stub: no-op
}
