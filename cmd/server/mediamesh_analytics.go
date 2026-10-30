package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// MediaMesh (radio2) analytics: how much traffic went over radio2, which
// repeaters run it, and which observers hear it. Built from the in-memory
// store and the MediaMesh marks loaded in mediamesh.go; nothing is stored.
//
// Scope follows the RF analytics: transmissions unique by hash, filtered by
// window (first seen) and, with a region, by the region's observers. A
// transmission counts as MediaMesh when one of its in-scope observations was
// carried on radio2.

// MediaMeshTypeCount is one payload type's total and radio2 share.
type MediaMeshTypeCount struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	MediaMesh int    `json:"mediamesh"`
}

// MediaMeshHour is the number of radio2 transmissions first heard in an hour.
type MediaMeshHour struct {
	Hour  string `json:"hour"` // "2006-01-02T15"
	Count int    `json:"count"`
}

// MediaMeshObserver is an observer that heard radio2 traffic.
type MediaMeshObserver struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	IATA         string  `json:"iata,omitempty"`
	MediaMeshObs int     `json:"mediamesh_observations"`
	TotalObs     int     `json:"total_observations"`
	SharePct     float64 `json:"share_pct"`
	LastHeard    string  `json:"last_heard"`
}

// MediaMeshRepeater is a node that runs radio2, with its last report.
type MediaMeshRepeater struct {
	PublicKey string `json:"public_key"`
	Name      string `json:"name,omitempty"`
	Role      string `json:"role,omitempty"`
	MediaMeshNode
}

// MediaMeshAnalytics is the /api/analytics/mediamesh response.
type MediaMeshAnalytics struct {
	TotalTransmissions     int                  `json:"total_transmissions"`
	MediaMeshTransmissions int                  `json:"mediamesh_transmissions"`
	SharePct               float64              `json:"share_pct"`
	ByType                 []MediaMeshTypeCount `json:"by_type"`
	PerHour                []MediaMeshHour      `json:"per_hour"`
	Observers              []MediaMeshObserver  `json:"observers"`
	Repeaters              []MediaMeshRepeater  `json:"repeaters"`
	Window                 string               `json:"window,omitempty"`
}

func (s *Server) handleAnalyticsMediaMesh(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	window := ParseTimeWindow(r)
	if s.store == nil {
		writeJSON(w, emptyMediaMeshAnalytics())
		return
	}
	writeJSON(w, s.store.GetMediaMeshAnalytics(region, window))
}

func emptyMediaMeshAnalytics() *MediaMeshAnalytics {
	return &MediaMeshAnalytics{
		ByType: []MediaMeshTypeCount{}, PerHour: []MediaMeshHour{},
		Observers: []MediaMeshObserver{}, Repeaters: []MediaMeshRepeater{},
	}
}

// GetMediaMeshAnalytics is computeMediaMeshAnalytics behind the shared
// short-TTL analytics cache (rfCache), like the relay-airtime share.
func (s *PacketStore) GetMediaMeshAnalytics(region string, window TimeWindow) *MediaMeshAnalytics {
	key := "mediamesh|" + region
	if !window.IsZero() {
		key += "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if c, ok := s.rfCache[key]; ok && time.Now().Before(c.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		if v, ok := c.data["result"].(*MediaMeshAnalytics); ok {
			return v
		}
	} else {
		s.cacheMisses++
		s.cacheMu.Unlock()
	}
	res := s.computeMediaMeshAnalytics(region, window)
	s.cacheMu.Lock()
	s.rfCache[key] = &cachedResult{data: map[string]interface{}{"result": res}, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()
	return res
}

func (s *PacketStore) computeMediaMeshAnalytics(region string, window TimeWindow) *MediaMeshAnalytics {
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	out := emptyMediaMeshAnalytics()
	if !window.IsZero() {
		out.Window = window.Label
	}

	type obsAgg struct {
		name, iata string
		mm, total  int
		last       string
	}
	types := map[string]*MediaMeshTypeCount{}
	hours := map[string]int{}
	observers := map[string]*obsAgg{}

	s.mu.RLock()
	mediaMeshMu.RLock()
	anyMM := len(mediaMeshObs) > 0
	seen := make(map[string]bool, len(s.packets))
	for _, tx := range s.packets {
		if tx == nil || !window.Includes(tx.FirstSeen) {
			continue
		}
		if tx.Hash != "" {
			if seen[tx.Hash] {
				continue
			}
			seen[tx.Hash] = true
		}
		inScope := regionObs == nil
		mm := false
		firstMM := ""
		for _, o := range tx.Observations {
			if regionObs != nil && !regionObs[o.ObserverID] {
				continue
			}
			inScope = true
			// The observer table is about what observers hear: an
			// observer's own transmission still makes the packet MediaMesh,
			// but is not something it heard.
			heard := !o.isOwnTransmission()
			var a *obsAgg
			if heard {
				a = observers[o.ObserverID]
				if a == nil {
					a = &obsAgg{name: o.ObserverName, iata: o.ObserverIATA}
					observers[o.ObserverID] = a
				}
				a.total++
			}
			if !anyMM {
				continue
			}
			if _, ok := mediaMeshObs[o.ID]; !ok {
				continue
			}
			mm = true
			if heard {
				a.mm++
				if o.Timestamp > a.last {
					a.last = o.Timestamp
				}
			}
			if firstMM == "" || (o.Timestamp != "" && o.Timestamp < firstMM) {
				firstMM = o.Timestamp
			}
		}
		if !inScope {
			continue
		}
		name := "UNK"
		if tx.PayloadType != nil {
			if n, ok := payloadTypeNames[*tx.PayloadType]; ok {
				name = n
			}
		}
		t := types[name]
		if t == nil {
			t = &MediaMeshTypeCount{Name: name}
			types[name] = t
		}
		t.Count++
		out.TotalTransmissions++
		if mm {
			t.MediaMesh++
			out.MediaMeshTransmissions++
			if firstMM == "" {
				firstMM = tx.FirstSeen
			}
			if len(firstMM) >= 13 {
				hours[firstMM[:13]]++
			}
		}
	}
	mediaMeshMu.RUnlock()
	s.mu.RUnlock()

	if out.TotalTransmissions > 0 {
		out.SharePct = float64(out.MediaMeshTransmissions) / float64(out.TotalTransmissions) * 100
	}
	for _, t := range types {
		out.ByType = append(out.ByType, *t)
	}
	sort.Slice(out.ByType, func(i, j int) bool {
		if out.ByType[i].Count != out.ByType[j].Count {
			return out.ByType[i].Count > out.ByType[j].Count
		}
		return out.ByType[i].Name < out.ByType[j].Name
	})
	for h, c := range hours {
		out.PerHour = append(out.PerHour, MediaMeshHour{Hour: strings.Replace(h, " ", "T", 1), Count: c})
	}
	sort.Slice(out.PerHour, func(i, j int) bool { return out.PerHour[i].Hour < out.PerHour[j].Hour })
	for id, a := range observers {
		if a.mm == 0 {
			continue
		}
		o := MediaMeshObserver{ID: id, Name: a.name, IATA: a.iata, MediaMeshObs: a.mm, TotalObs: a.total, LastHeard: a.last}
		if a.total > 0 {
			o.SharePct = float64(a.mm) / float64(a.total) * 100
		}
		out.Observers = append(out.Observers, o)
	}
	sort.Slice(out.Observers, func(i, j int) bool {
		if out.Observers[i].MediaMeshObs != out.Observers[j].MediaMeshObs {
			return out.Observers[i].MediaMeshObs > out.Observers[j].MediaMeshObs
		}
		return out.Observers[i].ID < out.Observers[j].ID
	})
	out.Repeaters = s.mediaMeshRepeaters(time.Now())
	return out
}

// mediaMeshRepeaters lists the nodes with a current MediaMesh mark (the same
// rule as the node API: not off, evidence under 7 days), newest first, with
// the node's name and role when known.
func (s *PacketStore) mediaMeshRepeaters(now time.Time) []MediaMeshRepeater {
	p := mediaMeshNodes.Load()
	out := []MediaMeshRepeater{}
	if p == nil || len(*p) == 0 {
		return out
	}
	names := map[string]nodeInfo{}
	nodes, _ := s.getCachedNodesAndPM()
	for _, n := range nodes {
		names[strings.ToLower(n.PublicKey)] = n
	}
	for pk := range *p {
		n := nodeMediaMeshAt(pk, now)
		if n == nil {
			continue
		}
		rep := MediaMeshRepeater{PublicKey: pk, MediaMeshNode: *n}
		if info, ok := names[pk]; ok {
			rep.Name, rep.Role = info.Name, info.Role
		}
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].PublicKey < out[j].PublicKey
	})
	return out
}
