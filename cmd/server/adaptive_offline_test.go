package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// chStoreWithPM is chStore with the three-repeater prefix map cached, so
// getCachedNodesAndPM does not go to SQLite.
func chStoreWithPM() *PacketStore {
	s := chStore()
	s.nodePM = chPM()
	s.nodeCacheTime = time.Now()
	return s
}

func TestCertainFor(t *testing.T) {
	pm := chPM()
	at := time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)
	// Own packets are certain without relay evidence; relay cases below
	// carry an actual observed path, independent of the broad byNode index.
	advert := chTx(1, 2, 4, 1, at)
	advert.DecodedJSON = fmt.Sprintf(`{"pubKey":%q}`, chA)
	anon := chTx(2, 2, 7, 1, at) // direct route, but the sender is certain
	anon.DecodedJSON = fmt.Sprintf(`{"srcPubKey":%q}`, chA)
	relay := func(id, route, payload, size int, pk string) *StoreTx {
		tx := chTx(id, route, payload, size, at)
		chObservation(tx, pk)
		return tx
	}
	cases := []struct {
		name string
		tx   *StoreTx
		pk   string
		want bool
	}{
		{"own advert", advert, chA, true},
		{"own anon request, direct route", anon, chA, true},
		{"2-byte flood hop, unique", relay(3, 1, 5, 2, chA), chA, true},
		{"1-byte flood hop shared with chB", relay(4, 1, 5, 1, chA), chA, false},
		{"1-byte flood hop, unique first byte", relay(5, 1, 5, 1, chC), chC, true},
		{"direct-route hop", relay(6, 2, 5, 2, chA), chA, false},
		{"TRACE", relay(7, 1, packetpathTrace, 2, chA), chA, false},
		{"someone else's advert", advert, chC, false},
		{"nil tx", nil, chA, false},
	}
	for _, c := range cases {
		if got := certainFor(c.tx, c.pk, pm); got != c.want {
			t.Errorf("%s: %v; want %v", c.name, got, c.want)
		}
	}
}

func TestLongestSilence(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 30, 0, 0, time.UTC)
	from := time.Date(2026, 10, 22, 6, 30, 0, 0, time.UTC)
	h := func(s string) string { return "2026-10-" + s + ":00:00Z" }
	cases := []struct {
		name    string
		buckets []string
		from    time.Time
		wantMs  int
		start   interface{}
	}{
		{"no certain bucket: the whole window", nil, from, 6 * 3600000, "2026-10-22T06:30:00Z"},
		{"gap between buckets", []string{h("22T07"), h("22T09"), h("22T10"), h("22T11"), h("22T12")}, from, 2 * 3600000, h("22T07")},
		{"silence before the first bucket", []string{h("22T10"), h("22T11"), h("22T12")}, from, int(3.5 * 3600000), "2026-10-22T06:30:00Z"},
		// #2260: the outage in progress is the longest silence.
		{"running silence", []string{h("18T15"), h("18T16")}, time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC), int(now.Sub(time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)).Milliseconds()), h("18T16")},
		{"current hour is not a silence", []string{h("22T12")}, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), 0, nil},
	}
	for _, c := range cases {
		ms, start := longestSilence(c.buckets, c.from, now)
		if ms != c.wantMs || start != c.start {
			t.Errorf("%s: %d from %v; want %d from %v", c.name, ms, start, c.wantMs, c.start)
		}
	}
}

func hourly(n, v int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// learnFrom is a midnight-UTC start, so hour index h is hour h%24 of day h/24.
var learnFrom = time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

// act builds a learning week with obs observers on every hour that has
// packets.
func act(counts []int, obs int) certainActivity {
	o := make([]int, len(counts))
	for i, c := range counts {
		if c > 0 {
			o[i] = obs
		}
	}
	return certainActivity{From: learnFrom, Hourly: counts, HourObservers: o}
}

func zeroAt(counts []int, hours ...int) []int {
	out := append([]int(nil), counts...)
	for _, h := range hours {
		out[h] = 0
	}
	return out
}

func span(from, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = from + i
	}
	return out
}

func TestAdaptiveOfflineAfter(t *testing.T) {
	floor := 30 * time.Minute
	learned := func(rate float64) time.Duration {
		return time.Duration(adaptiveSurpriseHours / rate * float64(time.Hour))
	}

	quietNights := hourly(168, 20)
	var nightly2h, nightly4h, everyFifth []int
	for d := 0; d < 7; d++ {
		for j := 0; j < 6; j++ {
			quietNights[d*24+j] = 3 // six quiet hours a night: the 10th percentile
		}
		nightly2h = append(nightly2h, d*24+2, d*24+3)
		nightly4h = append(nightly4h, d*24+1, d*24+2, d*24+3, d*24+4)
	}
	for i := 0; i < 30; i++ {
		everyFifth = append(everyFifth, i*5)
	}
	busy := hourly(168, 35)

	dominant := act(busy, 1) // one observer carries the node every hour
	dominant.HourObservers[5] = 3

	cases := []struct {
		name   string
		act    certainActivity
		want   time.Duration
		wantOK bool
	}{
		// 839 packets/day (#2260): 14/35 h ≈ 24 min, so the floor applies.
		{"busy repeater hits the floor", act(busy, 32), floor, true},
		{"5 per hour", act(hourly(168, 5), 5), learned(5), true},
		{"quiet nights set the rate", act(quietNights, 5), learned(3), true},
		{"under a day of history", act(hourly(23, 35), 32), 0, false},
		{"too few observers in the 10th-percentile hour", act(busy, 2), 0, false},
		// Review: a node heard 99 % through one observer passed a weekly
		// count of 3; per hour it is carried by one.
		{"one observer carries the node", dominant, 0, false},
		// Two empty hours every night recur: normal silence, the window is
		// at least 3 h (8 % of hours empty keeps the rate at 35).
		{"recurring 2-hour nightly silence lengthens the window", act(zeroAt(busy, nightly2h...), 5), 3 * time.Hour, true},
		// Four empty hours every night are 17 % of the week: too gappy to learn.
		{"recurring 4-hour nightly silence: not learnable", act(zeroAt(busy, nightly4h...), 5), 0, false},
		// Review: one 6-hour gap is an outage, not normal silence. Left out,
		// it neither lengthens the window nor blocks learning.
		{"one-off 6-hour gap is an outage", act(zeroAt(busy, span(100, 6)...), 5), floor, true},
		// Review: an outage over ~17 h used to make the node unlearnable
		// for a week (the zero hours took the 10th percentile to 0).
		{"30-hour outage stays learnable", act(zeroAt(busy, span(50, 30)...), 5), floor, true},
		// Recurring but longer than adaptiveMaxNormalRun: an outage.
		{"recurring 8-hour gaps are outages", act(zeroAt(busy, append(span(10, 8), span(34, 8)...)...), 5), floor, true},
		// A single empty hour every 5 hours: hour-of-day only repeats on a
		// later day for 12 of the 30, those are normal silence (window at
		// least 2 h); the 18 one-offs are left out of the learning.
		{"scattered empty hours: only recurring ones count", act(zeroAt(hourly(168, 40), everyFifth...), 5), 2 * time.Hour, true},
		{"nothing", certainActivity{}, 0, false},
	}
	for _, c := range cases {
		got, ok := adaptiveOfflineAfter(c.act, floor, 3)
		// Ceil to the nanosecond in the code; compare to the second.
		if ok != c.wantOK || got.Round(time.Second) != c.want.Round(time.Second) {
			t.Errorf("%s: %v %v; want %v %v", c.name, got, ok, c.want, c.wantOK)
		}
	}
}

// CertainActivity learns from the week before the last certain packet, so
// an outage does not drag the rate down, and counts only certain packets.
func TestCertainActivityEndsAtLastCertainPacket(t *testing.T) {
	s := chStoreWithPM()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	lastCertain := time.Date(2026, 10, 18, 16, 49, 0, 0, time.UTC)
	hopsSeen := map[string]bool{}
	id := 0
	for h := 0; h < 48; h++ {
		at := lastCertain.Add(-time.Duration(h) * time.Hour)
		for k := 0; k < 3; k++ {
			id++
			tx := chTx(id, 1, 5, 2, at.Add(-time.Duration(k)*time.Minute))
			tx.Observations = []*StoreObs{{ObserverID: fmt.Sprintf("obs%d", k)}}
			chIndexResolved(s, tx, []string{chA}, hopsSeen, s.nodePM)
		}
	}
	// After the outage only the shared 1-byte prefix: attributed, not certain.
	for h := 1; h < 60; h++ {
		id++
		chIndexResolved(s, chTx(id, 1, 5, 1, lastCertain.Add(time.Duration(h)*time.Hour)), []string{chA}, hopsSeen, s.nodePM)
	}

	a := s.CertainActivity([]string{chA, chC}, now)[chA]
	if want := int(adaptiveLearnWindow / time.Hour); len(a.Hourly) != want {
		t.Fatalf("hours = %d; want %d ending at the last certain packet", len(a.Hourly), want)
	}
	total := 0
	for _, c := range a.Hourly {
		total += c
	}
	if total != 48*3 {
		t.Fatalf("total %d; want 144 certain packets", total)
	}
	if got := a.HourObservers[len(a.HourObservers)-1]; got != 3 {
		t.Errorf("observers in the last hour = %d; want 3", got)
	}
	if !a.From.Equal(lastCertain.Truncate(time.Hour).Add(time.Hour).Add(-adaptiveLearnWindow)) {
		t.Errorf("From = %v", a.From)
	}
	if got := a.Hourly[len(a.Hourly)-1]; got != 3 {
		t.Errorf("last hour = %d; want the 3 packets of the last certain hour", got)
	}
	if _, ok := s.CertainActivity([]string{chC}, now)[chC]; ok {
		t.Error("a node never certainly heard has no activity")
	}
}

// End to end through the notifier: a busy repeater learned at ~3 per hour
// goes offline in mail after its learned window, not after 72 h; a node
// without enough observers keeps the fixed window; with the setting off
// nothing is learned or asked.
func TestNotifierAdaptiveOffline(t *testing.T) {
	cases := []struct {
		name      string
		enabled   bool
		observers int
		silentFor time.Duration
		want      string
	}{
		{"learned window passed", true, 5, 5 * time.Hour, users.NotifyBad},
		{"inside the learned window", true, 5, 4 * time.Hour, users.NotifyGood},
		{"too few observers: fixed 72 h", true, 2, 5 * time.Hour, users.NotifyGood},
		{"setting off: fixed 72 h", false, 5, 5 * time.Hour, users.NotifyGood},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := evInput()
			last := evNow.Add(-c.silentFor)
			in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Role: "repeater", LastSeen: last.Format(time.RFC3339)}
			in.Heard[evPkA] = last

			src := &fakeNotifySource{acts: map[string]certainActivity{evPkA: act(hourly(168, 3), c.observers)}}
			n := &notifier{a: &authService{set: &userMgmtSettings{}}, src: src}
			n.a.set.notify.adaptive = resolveAdaptiveOffline(&AdaptiveOfflineConfig{Enabled: c.enabled})
			in.OfflineAfter = n.offlineWindows([]string{evPkA}, evNow)

			if got, _ := evState(evaluateNotifications(in), evUser, users.NotifyNodeOffline, evPkA); got != c.want {
				t.Fatalf("node.offline = %q; want %q (window %v)", got, c.want, in.OfflineAfter)
			}
			if !c.enabled && len(src.actsAsked) != 0 {
				t.Fatal("activity asked with the setting off")
			}
		})
	}
}

func TestNotifierAdaptiveCacheRefreshAndPrune(t *testing.T) {
	src := &fakeNotifySource{acts: map[string]certainActivity{
		evPkA: act(hourly(168, 3), 5),
		evPkB: act(hourly(168, 3), 5),
	}}
	n := &notifier{a: &authService{set: &userMgmtSettings{}}, src: src}
	n.a.set.notify.adaptive = resolveAdaptiveOffline(&AdaptiveOfflineConfig{Enabled: true})

	n.offlineWindows([]string{evPkA, evPkB}, evNow)
	n.offlineWindows([]string{evPkA, evPkB}, evNow.Add(adaptiveRefreshEvery-time.Second))
	if len(src.actsAsked) != 1 {
		t.Fatalf("asked %d times inside the refresh period; want 1", len(src.actsAsked))
	}
	n.offlineWindows([]string{evPkA}, evNow.Add(adaptiveRefreshEvery))
	if len(src.actsAsked) != 2 || len(src.actsAsked[1]) != 1 {
		t.Fatalf("asked %v; want one refresh of the still-watched node", src.actsAsked)
	}
	if _, ok := n.adaptive[evPkB]; ok {
		t.Fatal("an unwatched node stays cached")
	}
}

func TestResolveAdaptiveOffline(t *testing.T) {
	if s := resolveAdaptiveOffline(nil); s.enabled {
		t.Fatal("absent block must be off")
	}
	if s := resolveAdaptiveOffline(&AdaptiveOfflineConfig{}); s.enabled {
		t.Fatal("enabled false must be off")
	}
	s := resolveAdaptiveOffline(&AdaptiveOfflineConfig{Enabled: true})
	if s.floor != 30*time.Minute || s.minObservers != 3 {
		t.Fatalf("defaults = %+v", s)
	}
	s = resolveAdaptiveOffline(&AdaptiveOfflineConfig{Enabled: true, FloorMinutes: 45, MinObservers: 5})
	if s.floor != 45*time.Minute || s.minObservers != 5 {
		t.Fatalf("configured = %+v", s)
	}
}

// The node analytics endpoint splits each timeline bucket and takes
// availability from certain buckets only.
func TestNodeAnalyticsCertainTimeline(t *testing.T) {
	srv, router := setupTestServer(t)
	const pk = "aabbccdd11223344"
	// A second repeater sharing the first byte makes 1-byte hop "aa" ambiguous.
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES ('aa00000000000000', 'Namesake', 'repeater')`)
	srv.store.InvalidateNodeCache()

	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)
	srv.store.mu.Lock()
	chIndexResolved(srv.store, chTx(900001, 1, 5, 1, hour.Add(10*time.Minute)), []string{pk}, map[string]bool{}, nil)
	srv.store.mu.Unlock()

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/"+pk+"/analytics?days=1", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		ActivityTimeline []TimeBucket `json:"activityTimeline"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := hour.Format(time.RFC3339)
	for _, b := range body.ActivityTimeline {
		if b.Bucket != nil && *b.Bucket == want {
			if b.Certain == nil || *b.Certain != 0 || b.Count != 1 {
				t.Fatalf("bucket %s: count %d certain %v; want 1 packet, 0 certain", want, b.Count, b.Certain)
			}
			return
		}
	}
	t.Fatalf("bucket %s missing from %d buckets", want, len(body.ActivityTimeline))
}

// CertainActivity walks a week of packets per watched node under the read
// lock, at most once per node per adaptiveRefreshEvery. 50 watched busy
// repeaters with 6,000 packets each (~840 a day, the #2260 node). Quote in
// the PR.
func BenchmarkCertainActivity(b *testing.B) {
	s := chStoreWithPM()
	end := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	keys := make([]string, 0, 50)
	nodes := make([]nodeInfo, 0, 50)
	s.certainHeard = map[string]string{}
	id := 0
	for n := 0; n < 50; n++ {
		pk := fmt.Sprintf("%04x%060x", n+1, n+1)
		keys = append(keys, pk)
		nodes = append(nodes, nodeInfo{PublicKey: pk, Role: "repeater"})
		for i := 0; i < 6000; i++ {
			id++
			tx := chTx(id, 1, 5, 2, end.Add(-time.Duration(i)*100*time.Second))
			tx.Observations = []*StoreObs{{ObserverID: "obs1"}, {ObserverID: "obs2"}}
			chObservation(tx, pk)
			s.byNode[pk] = append(s.byNode[pk], tx)
		}
		s.certainHeard[pk] = end.Format(time.RFC3339)
	}
	s.nodePM = buildPrefixMap(nodes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.CertainActivity(keys, end)
	}
}

// Review: a node stored offline comes back only on evidence from that moment
// on. Here its learned window is gone (retention dropped the history before
// the outage, or a restart) and the fixed 72 h window has not passed: it
// must stay offline, not be mailed "back online".
func TestOfflineNodeNeedsNewEvidenceToComeBack(t *testing.T) {
	lastCertain := evNow.Add(-49 * time.Hour)
	in := evInput()
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Role: "repeater", LastSeen: lastCertain.Format(time.RFC3339)}
	in.Heard[evPkA] = lastCertain
	row := evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyBad)
	row.ChangedAt = lastCertain.Add(30 * time.Minute) // mailed offline by the learned window
	in.States = []users.NotifyState{row}
	res := evaluateNotifications(in)
	// Unchanged states are not stored again; a good one would be.
	if got, _ := evState(res, evUser, users.NotifyNodeOffline, evPkA); got == users.NotifyGood {
		t.Fatal("node.offline turned good; nothing new was heard")
	}
	if len(res.Changes[evUser]) != 0 {
		t.Fatalf("changes = %+v; want no back-online mail", res.Changes[evUser])
	}

	// Heard again after it went offline: back online.
	in.Heard[evPkA] = evNow.Add(-time.Minute)
	if ch := evaluateNotifications(in).Changes[evUser]; len(ch) != 1 || ch[0].To != users.NotifyGood {
		t.Fatalf("changes = %+v; want one back-online change", ch)
	}
}

// Review (#2262): with a 30-minute learned window and the 30-minute stale
// threshold, a feed that stalls for 29 minutes must not mail a node whose
// last packet came 3 minutes before the stall. Silence is measured up to
// the newest packet in the store.
func TestStalledFeedStopsTheSilenceClock(t *testing.T) {
	in := evInput()
	last := evNow.Add(-32 * time.Minute)
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Role: "repeater", LastSeen: last.Format(time.RFC3339)}
	in.Heard[evPkA] = last
	in.OfflineAfter = map[string]time.Duration{evPkA: 30 * time.Minute}
	in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood)}

	in.NewestPacket = evNow.Add(-29 * time.Minute)
	if ch := evaluateNotifications(in).Changes[evUser]; len(ch) != 0 {
		t.Fatalf("changes = %+v; the feed stalled, the node did not", ch)
	}
	in.NewestPacket = evNow // the feed is live: the node is really silent
	if ch := evaluateNotifications(in).Changes[evUser]; len(ch) != 1 || ch[0].To != users.NotifyBad {
		t.Fatalf("changes = %+v; want offline", ch)
	}
}

// Review (#2262): the window bounds are UTC whatever the caller's zone, and
// a legacy "2006-01-02 15:04:05" FirstSeen on the window's first day counts.
func TestCertainActivityUTCBoundsAndLegacyTimestamps(t *testing.T) {
	s := chStoreWithPM()
	s.retentionHours = 30 // the window start binds on retention, not on the week
	lastCertain := time.Date(2026, 10, 22, 21, 10, 0, 0, time.UTC)
	hopsSeen := map[string]bool{}
	for h := 0; h < 28; h++ {
		chIndexResolved(s, chTx(h+1, 1, 5, 2, lastCertain.Add(-time.Duration(h)*time.Hour)), []string{chA}, hopsSeen, s.nodePM)
	}
	// Local now just after midnight (+02:00): the UTC date is a day earlier.
	now := time.Date(2026, 10, 23, 0, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	a := s.CertainActivity([]string{chA}, now)[chA]
	if a.From.Location() != time.UTC {
		t.Fatalf("From = %v; want UTC", a.From)
	}
	firstHour := a.From
	legacy := chTx(999, 1, 5, 2, firstHour)
	chObservation(legacy, chA)
	legacy.FirstSeen = firstHour.Add(20 * time.Minute).Format("2006-01-02 15:04:05")
	s.mu.Lock()
	s.byNode[chA] = append(s.byNode[chA], legacy)
	s.mu.Unlock()
	b := s.CertainActivity([]string{chA}, now)[chA]
	if b.Hourly[0] != a.Hourly[0]+1 {
		t.Fatalf("first hour %d -> %d; the legacy timestamp was dropped", a.Hourly[0], b.Hourly[0])
	}
}
