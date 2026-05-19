package main

import (
	"sort"
	"sync"
	"time"
)

// ObserverRecord tracks a known mesh observer, built up from MQTT traffic.
// Coordinates and names are learned passively from metadata messages.
type ObserverRecord struct {
	Key          string   `json:"key"`
	Name         string   `json:"name,omitempty"`
	Lat          *float64 `json:"lat,omitempty"`
	Lon          *float64 `json:"lon,omitempty"`
	Region       string   `json:"region,omitempty"`
	RegionGroup  string   `json:"regionGroup,omitempty"`
	ShortKey     string   `json:"shortKey"`
	FirstSeenAt  int64    `json:"firstSeenAt"`
	LastPacketAt int64    `json:"lastPacketAt"`
	PacketCount  int      `json:"packetCount"`
	// IsSeeded marks observers pre-populated from KnownObservers config; they
	// are always included in the directory regardless of retention window.
	IsSeeded     bool     `json:"-"`
}

// HasLocation returns true when this observer has valid GPS coordinates.
func (o *ObserverRecord) HasLocation() bool {
	return o.Lat != nil && o.Lon != nil
}

// IsActive returns true if a packet was seen within the active window.
func (o *ObserverRecord) IsActive(activeWindowMs int64) bool {
	if o.LastPacketAt == 0 {
		return false
	}
	return time.Now().UnixMilli()-o.LastPacketAt <= activeWindowMs
}

// observerShortKey returns the first 8 chars of an observer key.
func observerShortKey(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// ObserverRegistry maintains the in-memory observer directory.
// Observers are auto-discovered from all MQTT traffic, not just health checks.
// The registry is intentionally in-memory — it rebuilds from live traffic and
// any configured KnownObservers seed list on startup.
type ObserverRegistry struct {
	mu             sync.RWMutex
	records        map[string]*ObserverRecord
	activeWindowMs int64
	retentionMs    int64 // 0 means keep forever
}

// NewObserverRegistry creates a registry from HealthCheckConfig defaults.
func NewObserverRegistry(cfg *HealthCheckConfig) *ObserverRegistry {
	activeWindow := cfg.ObserverActiveWindowSeconds
	if activeWindow <= 0 {
		activeWindow = 900
	}
	retention := cfg.ObserverRetentionSeconds
	var retMs int64
	if retention > 0 {
		retMs = int64(retention) * 1000
	}
	r := &ObserverRegistry{
		records:        make(map[string]*ObserverRecord),
		activeWindowMs: int64(activeWindow) * 1000,
		retentionMs:    retMs,
	}
	// Pre-populate from static known-observers list so they appear in the
	// directory even before any MQTT traffic arrives.
	for _, k := range cfg.KnownObservers {
		if k != "" {
			r.records[k] = &ObserverRecord{Key: k, ShortKey: observerShortKey(k), IsSeeded: true}
		}
	}
	return r
}

// Touch records that a packet was received from observerKey and returns the record.
func (r *ObserverRegistry) Touch(observerKey string) *ObserverRecord {
	if observerKey == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[observerKey]
	if !ok {
		rec = &ObserverRecord{
			Key:         observerKey,
			ShortKey:    observerShortKey(observerKey),
			FirstSeenAt: now,
		}
		r.records[observerKey] = rec
	}
	rec.LastPacketAt = now
	rec.PacketCount++
	return rec
}

// UpdateName sets a display name learned from an observer metadata message.
func (r *ObserverRegistry) UpdateName(key, name string) bool {
	if key == "" || name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[key]
	if !ok {
		rec = &ObserverRecord{Key: key, ShortKey: observerShortKey(key)}
		r.records[key] = rec
	}
	if rec.Name == name {
		return false
	}
	rec.Name = name
	return true
}

// UpdateLocation sets GPS coordinates learned from an observer metadata message.
func (r *ObserverRegistry) UpdateLocation(key string, lat, lon float64) bool {
	if key == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[key]
	if !ok {
		rec = &ObserverRecord{Key: key, ShortKey: observerShortKey(key)}
		r.records[key] = rec
	}
	if rec.Lat != nil && *rec.Lat == lat && rec.Lon != nil && *rec.Lon == lon {
		return false
	}
	rec.Lat = &lat
	rec.Lon = &lon
	return true
}

// Get returns a copy of the record for a given key, or nil if not found.
func (r *ObserverRegistry) Get(key string) *ObserverRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[key]
	if !ok {
		return nil
	}
	cp := *rec
	return &cp
}

// Label returns the display name for key, or falls back to the short key prefix.
func (r *ObserverRegistry) Label(key string) string {
	r.mu.RLock()
	rec := r.records[key]
	r.mu.RUnlock()
	if rec != nil && rec.Name != "" {
		return rec.Name
	}
	return observerShortKey(key)
}

// ActiveKeys returns keys of observers active within the configured window, sorted.
func (r *ObserverRegistry) ActiveKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now().UnixMilli()
	var keys []string
	for k, rec := range r.records {
		if rec.LastPacketAt > 0 && now-rec.LastPacketAt <= r.activeWindowMs {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Directory returns all observer records that are within the retention window,
// sorted by label then key. KnownObservers-seeded records are always included
// regardless of the retention window (they have never expired semantics).
// Auto-discovered observers are included only while within the retention window.
func (r *ObserverRegistry) Directory() []*ObserverRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now().UnixMilli()
	var out []*ObserverRecord
	for _, rec := range r.records {
		if rec.IsSeeded {
			// Configured observers always appear in the directory.
		} else if rec.LastPacketAt == 0 {
			// Auto-discovered but never actually seen — skip.
			continue
		} else if r.retentionMs > 0 && now-rec.LastPacketAt > r.retentionMs {
			// Auto-discovered and outside retention window — drop.
			continue
		}
		cp := *rec
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		li := out[i].Name
		if li == "" {
			li = out[i].ShortKey
		}
		lj := out[j].Name
		if lj == "" {
			lj = out[j].ShortKey
		}
		if li != lj {
			return li < lj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// SerializeObserver produces the JSON-serialisable form of an ObserverRecord,
// annotated with activity status.
func (r *ObserverRegistry) SerializeObserver(rec *ObserverRecord) map[string]interface{} {
	hasLoc := rec.HasLocation()
	m := map[string]interface{}{
		"key":          rec.Key,
		"shortKey":     rec.ShortKey,
		"label":        rec.Name,
		"name":         rec.Name,
		"hasLocation":  hasLoc,
		"lat":          nil,
		"lon":          nil,
		"region":       rec.Region,
		"regionGroup":  rec.RegionGroup,
		"packetCount":  rec.PacketCount,
		"firstSeenAt":  rec.FirstSeenAt,
		"lastPacketAt": rec.LastPacketAt,
		"isActive":     rec.IsActive(r.activeWindowMs),
	}
	if rec.Name == "" {
		m["label"] = rec.ShortKey
	}
	if hasLoc {
		m["lat"] = *rec.Lat
		m["lon"] = *rec.Lon
	}
	return m
}
