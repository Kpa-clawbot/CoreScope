package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Adaptive offline threshold (#2260, part 2). The fixed infraSilentHours
// (72 h by default) treats every repeater the same: one heard every minute
// that goes quiet for an hour is suspicious, a node that sends a few adverts
// a day is not. For node.offline, a watched node's threshold is learned from
// its own certain traffic (certainFor), so an outage is mailed within the
// hour instead of three days later.
//
// Learning: certain packets per hour over the week before the node's last
// certain packet. Ending there, not at now, keeps a silence in progress out
// of the rate: otherwise the outage itself drags the rate down until the
// fixed threshold returns and the node reads "back online".
//
// Threshold: arrivals treated as Poisson, P(no packet in t) = e^(-λt); for
// that below one in a million t = ln(10⁶)/λ ≈ 14/λ hours, with λ the 10th
// percentile of the hourly counts so quiet nights do not alert.
//
// Empty runs of hours are classified first. A run of at most
// adaptiveMaxNormalRun hours that recurs (another empty run on a different
// day covers one of the same hours of the day) is the node's normal
// silence: it stays in the percentile, and the threshold is never shorter
// than the longest such run plus an hour, so it does not mail offline and
// online every night. Any other run is an outage: it is left out of the
// learning entirely, so one outage neither stretches the threshold for a
// week nor makes the node unlearnable.
//
// Clamped to [floor, the role's silent window]. Nodes with too little
// certain traffic (λ < 1/hour, under a day of history) keep the fixed
// window, and so do nodes whose 10th-percentile hour was heard by fewer
// than minObservers observers: when one observer carries the node, that
// observer dropping out must not read as the node going offline.

const (
	adaptiveLearnWindow   = 7 * 24 * time.Hour
	adaptiveMinHistory    = 24              // hours of history before a rate is trusted
	adaptiveMinRate       = 1.0             // certain packets per hour at the 10th percentile
	adaptiveSurpriseHours = 13.815510557964 // ln(10⁶): P(silence) < 10⁻⁶
	adaptiveMaxNormalRun  = 6               // longest empty run in hours that may be normal silence
	adaptiveRefreshEvery  = 10 * time.Minute
)

// certainActivity is a node's certain traffic in its learning window.
type certainActivity struct {
	From          time.Time // start of Hourly[0], UTC, on the hour
	Hourly        []int     // certain packets per hour, oldest first, zero hours included
	HourObservers []int     // distinct observers per hour of those packets
}

// CertainActivity returns, per pubkey with a certain last heard, its certain
// packets per hour over adaptiveLearnWindow up to that last heard (capped at
// the store's covered window) and how many observers heard each hour.
// O(packets of the given nodes), with the read lock taken per node so ingest
// waits for one node's walk at most, not all of them.
func (s *PacketStore) CertainActivity(pubkeys []string, now time.Time) map[string]certainActivity {
	now = now.UTC() // FirstSeen is UTC; a local offset breaks the string bounds
	_, pm := s.getCachedNodesAndPM()
	s.mu.RLock()
	covered, _ := s.coverageStart(now)
	s.mu.RUnlock()
	covered = covered.UTC()
	out := make(map[string]certainActivity, len(pubkeys))
	for _, pk := range pubkeys {
		if act, ok := s.certainActivityOf(pk, covered, pm); ok {
			out[pk] = act
		}
	}
	return out
}

// certainActivityOf is CertainActivity for one node, under its own read lock.
func (s *PacketStore) certainActivityOf(pk string, covered time.Time, pm *prefixMap) (certainActivity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	last, ok := parseRelayTS(s.certainHeard[pk])
	if !ok {
		return certainActivity{}, false
	}
	end := last.UTC().Truncate(time.Hour).Add(time.Hour)
	from := end.Add(-adaptiveLearnWindow)
	if covered.After(from) {
		from = covered.Truncate(time.Hour).Add(time.Hour) // first whole hour held
	}
	hours := int(end.Sub(from) / time.Hour)
	if hours <= 0 {
		return certainActivity{}, false
	}
	act := certainActivity{From: from, Hourly: make([]int, hours), HourObservers: make([]int, hours)}
	observers := make([]map[string]struct{}, hours)
	// Pre-filter on the date only: legacy FirstSeen values use a space
	// instead of T and would sort below a same-day RFC3339 bound. The parsed
	// time below decides.
	fromDate := from.Format("2006-01-02")
	for _, tx := range s.byNode[pk] {
		if tx == nil || len(tx.FirstSeen) < 10 || tx.FirstSeen[:10] < fromDate || !certainFor(tx, pk, pm) {
			continue
		}
		t, ok := parseStoreTime(tx.FirstSeen)
		if !ok || t.Before(from) || !t.Before(end) {
			continue
		}
		h := int(t.Sub(from) / time.Hour)
		act.Hourly[h]++
		for _, o := range tx.Observations {
			if o != nil && o.ObserverID != "" {
				if observers[h] == nil {
					observers[h] = map[string]struct{}{}
				}
				observers[h][o.ObserverID] = struct{}{}
			}
		}
	}
	for h, set := range observers {
		act.HourObservers[h] = len(set)
	}
	return act, true
}

// adaptiveOfflineAfter is the learned silence after which a node with this
// activity counts as offline, floored at floor; ok is false when the
// activity is too thin to learn from (see the file comment). The caller
// caps it at the role's silent window.
func adaptiveOfflineAfter(act certainActivity, floor time.Duration, minObservers int) (time.Duration, bool) {
	outage, normalRun := classifySilences(act)
	var counts, observers []int
	for h, c := range act.Hourly {
		if outage[h] {
			continue
		}
		counts = append(counts, c)
		if c > 0 && h < len(act.HourObservers) {
			observers = append(observers, act.HourObservers[h])
		}
	}
	if len(counts) < adaptiveMinHistory || len(observers) == 0 {
		return 0, false
	}
	sort.Ints(counts)
	rate := float64(counts[len(counts)/10])
	if rate < adaptiveMinRate {
		return 0, false
	}
	sort.Ints(observers)
	if observers[len(observers)/10] < minObservers {
		return 0, false
	}
	after := time.Duration(math.Ceil(adaptiveSurpriseHours / rate * float64(time.Hour)))
	if normalRun > 0 {
		// k empty buckets in a row can hide up to k+1 hours of silence.
		if seen := time.Duration(normalRun+1) * time.Hour; after < seen {
			after = seen
		}
	}
	if after < floor {
		after = floor
	}
	return after, true
}

// classifySilences marks the hours of outage runs (empty runs that are not
// normal silence, see the file comment) and returns the longest normal run.
func classifySilences(act certainActivity) (outage []bool, longestNormal int) {
	type run struct{ start, n int }
	var runs []run
	for h := 0; h < len(act.Hourly); h++ {
		if act.Hourly[h] != 0 {
			continue
		}
		r := run{start: h}
		for h < len(act.Hourly) && act.Hourly[h] == 0 {
			r.n++
			h++
		}
		runs = append(runs, r)
	}
	day := func(h int) int { return int(act.From.Add(time.Duration(h)*time.Hour).Unix() / 86400) }
	hod := func(h int) int { return act.From.Add(time.Duration(h) * time.Hour).Hour() }
	recurs := func(i int) bool {
		a := runs[i]
		for j, b := range runs {
			if j == i || day(b.start) == day(a.start) {
				continue
			}
			for x := a.start; x < a.start+a.n; x++ {
				for y := b.start; y < b.start+b.n; y++ {
					if hod(x) == hod(y) {
						return true
					}
				}
			}
		}
		return false
	}
	outage = make([]bool, len(act.Hourly))
	for i, r := range runs {
		if r.n <= adaptiveMaxNormalRun && recurs(i) {
			if r.n > longestNormal {
				longestNormal = r.n
			}
			continue
		}
		for h := r.start; h < r.start+r.n; h++ {
			outage[h] = true
		}
	}
	return outage, longestNormal
}

// adaptiveEntry is one cached threshold; ok false caches "not learnable".
type adaptiveEntry struct {
	after time.Duration
	ok    bool
	at    time.Time
}

// offlineWindows returns the learned offline window per watched pubkey that
// has one, recomputing entries older than adaptiveRefreshEvery: the rate
// moves slowly and the walk over a week of packets need not run every tick.
// Entries for nodes no longer watched are dropped. Loop goroutine only.
func (n *notifier) offlineWindows(pubkeys []string, now time.Time) map[string]time.Duration {
	set := n.a.set.notify.adaptive
	if !set.enabled || len(pubkeys) == 0 {
		n.adaptive = nil
		return nil
	}
	if n.adaptive == nil {
		n.adaptive = map[string]adaptiveEntry{}
	}
	watched := make(map[string]bool, len(pubkeys))
	var stale []string
	for _, pk := range pubkeys {
		pk = strings.ToLower(pk)
		watched[pk] = true
		if e, ok := n.adaptive[pk]; !ok || now.Sub(e.at) >= adaptiveRefreshEvery {
			stale = append(stale, pk)
		}
	}
	for pk := range n.adaptive {
		if !watched[pk] {
			delete(n.adaptive, pk)
		}
	}
	if len(stale) > 0 {
		acts := n.src.certainActivity(stale, now)
		for _, pk := range stale {
			after, ok := adaptiveOfflineAfter(acts[pk], set.floor, set.minObservers)
			n.adaptive[pk] = adaptiveEntry{after: after, ok: ok, at: now}
		}
	}
	out := make(map[string]time.Duration, len(n.adaptive))
	for pk, e := range n.adaptive {
		if e.ok {
			out[pk] = e.after
		}
	}
	return out
}
