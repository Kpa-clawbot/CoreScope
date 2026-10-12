package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

var evNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const (
	evUser   = int64(1)
	evAdmin  = int64(2)
	evAdmin2 = int64(3)
)

var (
	evPkA = strings.Repeat("a", 64)
	evPkB = strings.Repeat("b", 64)
	evPkF = strings.Repeat("f", 64)
	evPkG = strings.Repeat("e", 64)
)

func evAgo(d time.Duration) string { return evNow.Add(-d).Format(time.RFC3339) }
func evMv(v int) *int              { return &v }

// evInput: one user watching evPkA with the default node events, two
// admins without prefs, no stored states.
func evInput() notifyInput {
	return notifyInput{
		Now: evNow, Health: (&Config{}).GetHealthThresholds(), LowMv: 3300,
		Accounts: map[int64]users.User{
			evUser:   {ID: evUser, Role: users.RoleUser, Status: users.StatusActive},
			evAdmin:  {ID: evAdmin, Role: users.RoleAdmin, Status: users.StatusActive},
			evAdmin2: {ID: evAdmin2, Role: users.RoleAdmin, Status: users.StatusActive},
		},
		Prefs:   []users.NotifyPrefs{{UserID: evUser, Enabled: true, Events: []string{users.NotifyNodeOffline, users.NotifyNodeBattery}}},
		Watches: []users.NotifyWatch{{UserID: evUser, Pubkey: evPkA}},
		Nodes:   map[string]notifyNode{},
		Heard:   map[string]time.Time{},
		Relayed: map[string]time.Time{},
	}
}

func evStateRow(uid int64, event, subject, state string) users.NotifyState {
	return users.NotifyState{NotifyKey: users.NotifyKey{UserID: uid, Event: event, Subject: subject}, State: state, ChangedAt: evNow.Add(-time.Hour)}
}

func evState(res notifyResult, uid int64, event, subject string) (string, bool) {
	for _, s := range res.States {
		if s.UserID == uid && s.Event == event && s.Subject == subject {
			return s.State, true
		}
	}
	return "", false
}

func TestEvaluateFirstEvaluationStoresWithoutChanges(t *testing.T) {
	in := evInput()
	in.Watches = append(in.Watches, users.NotifyWatch{UserID: evUser, Pubkey: evPkB})
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(48 * time.Hour)}
	in.Nodes[evPkB] = notifyNode{Pubkey: evPkB, Name: "Bravo", Role: "companion", LastSeen: evAgo(time.Hour), BatteryMv: evMv(3100)}
	res := evaluateNotifications(in)
	if len(res.Changes) != 0 {
		t.Fatalf("changes on the first evaluation: %+v", res.Changes)
	}
	for _, c := range []struct{ event, subject, want string }{
		{users.NotifyNodeOffline, evPkA, users.NotifyBad},
		{users.NotifyNodeOffline, evPkB, users.NotifyGood},
		{users.NotifyNodeBattery, evPkB, users.NotifyBad},
	} {
		if got, ok := evState(res, evUser, c.event, c.subject); !ok || got != c.want {
			t.Errorf("%s %s = %q (stored %v); want %q", c.event, c.subject[:4], got, ok, c.want)
		}
	}
	if _, ok := evState(res, evUser, users.NotifyNodeBattery, evPkA); ok {
		t.Error("battery state stored for a node without telemetry")
	}
	for _, s := range res.States {
		if !s.ChangedAt.Equal(evNow) {
			t.Errorf("ChangedAt = %v; want the input's Now", s.ChangedAt)
		}
	}
}

func TestEvaluateOfflineAndBack(t *testing.T) {
	in := evInput()
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(24 * time.Hour)}
	in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood)}
	res := evaluateNotifications(in)
	ch := res.Changes[evUser]
	if len(ch) != 1 || ch[0].Event != users.NotifyNodeOffline || ch[0].Subject != evPkA || ch[0].To != users.NotifyBad ||
		ch[0].Name != "Alpha" || !ch[0].At.Equal(evNow) {
		t.Fatalf("changes = %+v; want one offline change for Alpha", ch)
	}
	// One minute inside the 24 h companion window: still online, nothing to write.
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(24*time.Hour - time.Minute)}
	if res := evaluateNotifications(in); len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("unchanged online node produced %+v", res)
	}
	// Stored bad, advert 30 h old, but the packet store heard it an hour ago.
	in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyBad)}
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(30 * time.Hour)}
	in.Heard[evPkA] = evNow.Add(-time.Hour)
	if ch := evaluateNotifications(in).Changes[evUser]; len(ch) != 1 || ch[0].To != users.NotifyGood {
		t.Fatalf("changes = %+v; want one back-online change", ch)
	}
}

func TestEvaluateRelayAwareInfra(t *testing.T) {
	cases := []struct {
		name, role, lastSeen string
		relayed              time.Duration
		want                 string
	}{
		{"repeater relaying", "repeater", evAgo(80 * time.Hour), time.Hour, users.NotifyGood},
		{"room relaying, role case", "Room", evAgo(80 * time.Hour), time.Hour, users.NotifyGood},
		{"repeater silent exactly 72 h", "repeater", evAgo(72 * time.Hour), 0, users.NotifyBad},
		{"repeater 71 h", "repeater", evAgo(71 * time.Hour), 0, users.NotifyGood},
		{"companion: relay time ignored", "companion", evAgo(30 * time.Hour), time.Hour, users.NotifyBad},
		{"no timestamp at all", "repeater", "", 0, users.NotifyBad},
	}
	for _, c := range cases {
		in := evInput()
		in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Role: c.role, LastSeen: c.lastSeen}
		if c.relayed > 0 {
			in.Relayed[evPkA] = evNow.Add(-c.relayed)
		}
		if got, _ := evState(evaluateNotifications(in), evUser, users.NotifyNodeOffline, evPkA); got != c.want {
			t.Errorf("%s: %q; want %q", c.name, got, c.want)
		}
	}
}

func TestEvaluateBatteryHysteresis(t *testing.T) {
	cases := []struct {
		prev   string
		mv     int
		want   string
		change bool
	}{
		{"", 3299, users.NotifyBad, false},
		{"", 3300, users.NotifyGood, false},
		{"", 3350, users.NotifyGood, false},
		{users.NotifyGood, 3299, users.NotifyBad, true},
		{users.NotifyGood, 3300, users.NotifyGood, false},
		{users.NotifyBad, 3399, users.NotifyBad, false},
		{users.NotifyBad, 3400, users.NotifyGood, true},
	}
	for _, c := range cases {
		in := evInput()
		in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(time.Hour), BatteryMv: evMv(c.mv)}
		in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood)}
		if c.prev != "" {
			in.States = append(in.States, evStateRow(evUser, users.NotifyNodeBattery, evPkA, c.prev))
		}
		res := evaluateNotifications(in)
		got, stored := evState(res, evUser, users.NotifyNodeBattery, evPkA)
		if !stored {
			got = c.prev
		}
		ch := res.Changes[evUser]
		if got != c.want || (len(ch) == 1) != c.change {
			t.Errorf("prev %q, %d mV: state %q, changes %+v; want %q, change %v", c.prev, c.mv, got, ch, c.want, c.change)
			continue
		}
		if c.change && (ch[0].Event != users.NotifyNodeBattery || ch[0].BatteryMv == nil || *ch[0].BatteryMv != c.mv) {
			t.Errorf("prev %q, %d mV: change = %+v", c.prev, c.mv, ch[0])
		}
	}
}

func TestEvaluateAbsentNodeIsOfflineOnce(t *testing.T) {
	in := evInput() // evPkA is watched but not in the analyzer database
	in.States = []users.NotifyState{
		evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeBattery, evPkA, users.NotifyBad),
	}
	ch := evaluateNotifications(in).Changes[evUser]
	if len(ch) != 1 || ch[0].Event != users.NotifyNodeOffline || ch[0].To != users.NotifyBad || ch[0].Name != evPkA[:8] {
		t.Fatalf("changes = %+v; want one offline change named by the key prefix", ch)
	}
	in.States[0] = evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyBad)
	if res := evaluateNotifications(in); len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("absent node evaluated again: %+v", res)
	}
}

func TestEvaluateForeignOncePerAdmin(t *testing.T) {
	in := evInput()
	in.Prefs[0].Events = append(in.Prefs[0].Events, users.NotifyForeignNew) // a non-admin with an admin event
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin, Enabled: true, Events: []string{users.NotifyForeignNew}})
	in.Nodes[evPkF] = notifyNode{Pubkey: evPkF, Name: "Foxtrot", Foreign: true, LastSeen: evAgo(time.Hour)}
	res := evaluateNotifications(in)
	if len(res.Changes[evAdmin]) != 0 {
		t.Fatalf("first evaluation mailed: %+v", res.Changes[evAdmin])
	}
	if s, ok := evState(res, evAdmin, users.NotifyForeignNew, foreignBaselineSubject); !ok || s != users.NotifyGood {
		t.Fatalf("baseline row = %q, %v", s, ok)
	}
	if s, ok := evState(res, evAdmin, users.NotifyForeignNew, evPkF); !ok || s != users.NotifyTold {
		t.Fatalf("existing foreign node = %q, %v; want told", s, ok)
	}
	if _, ok := evState(res, evUser, users.NotifyForeignNew, evPkF); ok {
		t.Fatal("non-admin evaluated for foreign.new")
	}

	// Next tick: a second foreign node appears; a second admin opts in now.
	in.States = res.States
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin2, Enabled: true, Events: []string{users.NotifyForeignNew}})
	in.Nodes[evPkG] = notifyNode{Pubkey: evPkG, Name: "Golf", Foreign: true}
	res = evaluateNotifications(in)
	ch := res.Changes[evAdmin]
	if len(ch) != 1 || ch[0].Subject != evPkG || ch[0].To != users.NotifyTold || ch[0].Name != "Golf" {
		t.Fatalf("admin changes = %+v; want Golf once", ch)
	}
	if len(res.Changes[evAdmin2]) != 0 {
		t.Fatalf("second admin's first evaluation mailed: %+v", res.Changes[evAdmin2])
	}

	in.States = append(in.States, res.States...)
	if res := evaluateNotifications(in); len(res.Changes) != 0 {
		t.Fatalf("foreign nodes mailed twice: %+v", res.Changes)
	}
}

func TestEvaluateObserverOfflineAndBack(t *testing.T) {
	cases := []struct {
		prev   string
		age    time.Duration
		want   string
		change bool
	}{
		{"", 2 * time.Hour, users.NotifyGood, false},
		{"", 25 * time.Hour, users.NotifyBad, false},
		{users.NotifyGood, 24 * time.Hour, users.NotifyGood, false},
		{users.NotifyGood, 24*time.Hour + time.Minute, users.NotifyBad, true},
		{users.NotifyBad, 2 * time.Hour, users.NotifyBad, false},
		{users.NotifyBad, 60 * time.Minute, users.NotifyGood, true},
	}
	for _, c := range cases {
		in := evInput()
		in.Prefs = []users.NotifyPrefs{{UserID: evAdmin, Enabled: true, Events: []string{users.NotifyObserverOffline}}}
		in.Watches = nil
		in.Observers = []notifyObserver{{ID: "OBS1", Name: "Roof", LastSeen: evAgo(c.age)}}
		if c.prev != "" {
			in.States = []users.NotifyState{evStateRow(evAdmin, users.NotifyObserverOffline, "OBS1", c.prev)}
		}
		res := evaluateNotifications(in)
		got, stored := evState(res, evAdmin, users.NotifyObserverOffline, "OBS1")
		if !stored {
			got = c.prev
		}
		ch := res.Changes[evAdmin]
		if got != c.want || (len(ch) == 1) != c.change || (c.change && ch[0].Name != "Roof") {
			t.Errorf("prev %q, age %v: state %q, changes %+v; want %q, change %v", c.prev, c.age, got, ch, c.want, c.change)
		}
	}
	in := evInput()
	in.Prefs = []users.NotifyPrefs{{UserID: evAdmin, Enabled: true, Events: []string{users.NotifyObserverOffline}}}
	in.Observers = []notifyObserver{{ID: "OBS2"}}
	if s, _ := evState(evaluateNotifications(in), evAdmin, users.NotifyObserverOffline, "OBS2"); s != users.NotifyBad {
		t.Fatalf("observer without last_seen = %q; want bad", s)
	}
}

func TestEvaluateOnlyChosenEventsAndKnownAccounts(t *testing.T) {
	in := evInput()
	in.Prefs[0].Events = []string{users.NotifyNodeBattery, users.NotifyObserverOffline} // observer.offline: the user is no admin
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Role: "companion", LastSeen: evAgo(48 * time.Hour), BatteryMv: evMv(4000)}
	in.Observers = []notifyObserver{{ID: "OBS1", LastSeen: evAgo(time.Hour)}}
	res := evaluateNotifications(in)
	if len(res.States) != 1 || res.States[0].Event != users.NotifyNodeBattery {
		t.Fatalf("states = %+v; want only node.battery", res.States)
	}
	in.Accounts = map[int64]users.User{} // the user was deleted after the read
	if res := evaluateNotifications(in); len(res.States) != 0 {
		t.Fatalf("deleted user evaluated: %+v", res.States)
	}
}

func TestEvaluateChangesAreOrderedPerUser(t *testing.T) {
	in := evInput()
	in.Watches = []users.NotifyWatch{{UserID: evUser, Pubkey: evPkB}, {UserID: evUser, Pubkey: evPkA}}
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(48 * time.Hour), BatteryMv: evMv(3000)}
	in.Nodes[evPkB] = notifyNode{Pubkey: evPkB, Name: "Bravo", Role: "companion", LastSeen: evAgo(48 * time.Hour)}
	in.States = []users.NotifyState{
		evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeOffline, evPkB, users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeBattery, evPkA, users.NotifyGood),
	}
	var got []string
	for _, c := range evaluateNotifications(in).Changes[evUser] {
		got = append(got, c.Event+" "+c.Name)
	}
	if want := "node.offline Alpha|node.offline Bravo|node.battery Alpha"; strings.Join(got, "|") != want {
		t.Fatalf("order = %q; want %q", strings.Join(got, "|"), want)
	}
}

// 100 users x 50 watches over 2,000 nodes: the spec's Performance figure
// (5,000 state rows per event type). Run with -bench and quote the result
// in the PR.
func BenchmarkEvaluateNotifications(b *testing.B) {
	in := notifyInput{Now: evNow, Health: (&Config{}).GetHealthThresholds(), LowMv: 3300,
		Accounts: map[int64]users.User{}, Nodes: map[string]notifyNode{}, Heard: map[string]time.Time{}, Relayed: map[string]time.Time{}}
	for i := 0; i < 2000; i++ {
		pk := fmt.Sprintf("%064x", i)
		in.Nodes[pk] = notifyNode{Pubkey: pk, Role: "repeater", LastSeen: evAgo(time.Duration(i) * time.Minute), BatteryMv: evMv(3200 + i%300)}
	}
	for u := int64(1); u <= 100; u++ {
		in.Accounts[u] = users.User{ID: u, Role: users.RoleUser, Status: users.StatusActive}
		in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: u, Enabled: true, Events: users.NodeNotifyEvents})
		for w := 0; w < 50; w++ {
			in.Watches = append(in.Watches, users.NotifyWatch{UserID: u, Pubkey: fmt.Sprintf("%064x", (int(u)*37+w*11)%2000)})
		}
	}
	in.States = evaluateNotifications(in).States
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		evaluateNotifications(in)
	}
}

// P7: an admin who loses the admin role loses their admin-event states, like
// opting out of those events, so a re-promotion starts a fresh baseline.
func TestEvaluateDemotedAdminDropsAdminEventStates(t *testing.T) {
	in := evInput()
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin, Enabled: true,
		Events: []string{users.NotifyNodeOffline, users.NotifyForeignNew, users.NotifyObserverOffline}})
	in.Watches = append(in.Watches, users.NotifyWatch{UserID: evAdmin, Pubkey: evPkA})
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(time.Hour)}
	in.Nodes[evPkF] = notifyNode{Pubkey: evPkF, Name: "Foxtrot", Foreign: true}
	in.Observers = []notifyObserver{{ID: "OBS1", Name: "Roof", LastSeen: evAgo(time.Hour)}}
	in.States = evaluateNotifications(in).States
	if s, ok := evState(notifyResult{States: in.States}, evAdmin, users.NotifyForeignNew, foreignBaselineSubject); !ok || s != users.NotifyGood {
		t.Fatalf("baseline row = %q, %v", s, ok)
	}

	// Demoted: the admin-event rows are dropped, the node rows stay, nothing mailed.
	admin := in.Accounts[evAdmin]
	admin.Role = users.RoleUser
	in.Accounts[evAdmin] = admin
	in.Nodes[evPkG] = notifyNode{Pubkey: evPkG, Name: "Golf", Foreign: true}
	in.Observers[0].LastSeen = evAgo(48 * time.Hour)
	res := evaluateNotifications(in)
	if len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("demoted admin evaluated: %+v", res)
	}
	want := map[users.NotifyKey]bool{
		{UserID: evAdmin, Event: users.NotifyForeignNew, Subject: foreignBaselineSubject}: true,
		{UserID: evAdmin, Event: users.NotifyForeignNew, Subject: evPkF}:                  true,
		{UserID: evAdmin, Event: users.NotifyObserverOffline, Subject: "OBS1"}:            true,
	}
	if len(res.Drop) != len(want) {
		t.Fatalf("drop = %+v; want %d admin-event keys", res.Drop, len(want))
	}
	for _, k := range res.Drop {
		if !want[k] {
			t.Errorf("dropped %+v", k)
		}
	}
	dropped := map[users.NotifyKey]bool{}
	for _, k := range res.Drop {
		dropped[k] = true
	}
	var kept []users.NotifyState
	for _, s := range in.States {
		if !dropped[s.NotifyKey] {
			kept = append(kept, s)
		}
	}
	in.States = kept
	if res := evaluateNotifications(in); len(res.Drop) != 0 {
		t.Fatalf("dropped again: %+v", res.Drop)
	}

	// Re-promoted: Golf appeared and the observer went offline meanwhile; the
	// first evaluation stores a new baseline and mails nothing.
	admin.Role = users.RoleAdmin
	in.Accounts[evAdmin] = admin
	res = evaluateNotifications(in)
	if len(res.Changes) != 0 || len(res.Drop) != 0 {
		t.Fatalf("re-promoted admin: changes %+v, drop %+v", res.Changes, res.Drop)
	}
	for _, c := range []struct{ event, subject, want string }{
		{users.NotifyForeignNew, foreignBaselineSubject, users.NotifyGood},
		{users.NotifyForeignNew, evPkG, users.NotifyTold},
		{users.NotifyObserverOffline, "OBS1", users.NotifyBad},
	} {
		if got, ok := evState(res, evAdmin, c.event, c.subject); !ok || got != c.want {
			t.Errorf("%s %s = %q (stored %v); want %q", c.event, c.subject, got, ok, c.want)
		}
	}
}

// A user who watches nodes but has no preferences row (never opened the
// settings, or created before the notification schema) gets the defaults:
// enabled, node events on, admin events off.
func TestEvaluateWatcherWithoutPrefsGetsDefaults(t *testing.T) {
	in := evInput()
	in.Prefs = nil
	in.Watches = []users.NotifyWatch{{UserID: evAdmin, Pubkey: evPkA}}
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(48 * time.Hour), BatteryMv: evMv(3000)}
	in.Nodes[evPkF] = notifyNode{Pubkey: evPkF, Name: "Foxtrot", Foreign: true}
	in.Observers = []notifyObserver{{ID: "OBS1", LastSeen: evAgo(time.Hour)}}
	in.States = []users.NotifyState{
		evStateRow(evAdmin, users.NotifyNodeOffline, evPkA, users.NotifyGood),
		evStateRow(evAdmin, users.NotifyNodeBattery, evPkA, users.NotifyGood),
	}
	res := evaluateNotifications(in)
	var got []string
	for _, c := range res.Changes[evAdmin] {
		got = append(got, c.Event+" "+c.To)
	}
	if want := "node.offline bad|node.battery bad"; strings.Join(got, "|") != want {
		t.Fatalf("changes = %q; want %q", strings.Join(got, "|"), want)
	}
	for _, s := range res.States {
		if users.IsAdminNotifyEvent(s.Event) {
			t.Errorf("admin event evaluated without opting in: %+v", s)
		}
	}
	in.Accounts = map[int64]users.User{}
	if res := evaluateNotifications(in); len(res.States) != 0 {
		t.Fatalf("watcher without an account evaluated: %+v", res.States)
	}
}

// A disabled user keeps being evaluated: states follow the node, nothing is
// mailed, and re-enabling does not mail what changed while off.
func TestEvaluateDisabledUserUpdatesStatesSilently(t *testing.T) {
	in := evInput()
	in.Prefs[0].Enabled = false
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin, Enabled: false, Events: []string{users.NotifyForeignNew}})
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(time.Hour)}
	in.Nodes[evPkF] = notifyNode{Pubkey: evPkF, Name: "Foxtrot", Foreign: true}
	in.States = evaluateNotifications(in).States

	// While off: Alpha goes offline and Golf appears.
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(48 * time.Hour)}
	in.Nodes[evPkG] = notifyNode{Pubkey: evPkG, Name: "Golf", Foreign: true}
	res := evaluateNotifications(in)
	if len(res.Changes) != 0 {
		t.Fatalf("disabled users mailed: %+v", res.Changes)
	}
	if s, ok := evState(res, evUser, users.NotifyNodeOffline, evPkA); !ok || s != users.NotifyBad {
		t.Fatalf("disabled user's offline state = %q, %v; want bad stored", s, ok)
	}
	if s, ok := evState(res, evAdmin, users.NotifyForeignNew, evPkG); !ok || s != users.NotifyTold {
		t.Fatalf("disabled admin's foreign state = %q, %v; want told stored", s, ok)
	}
	in.States = append(in.States, res.States...)

	// Re-enabled: the next evaluation has nothing to send.
	in.Prefs[0].Enabled = true
	in.Prefs[1].Enabled = true
	if res := evaluateNotifications(in); len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("re-enabling mailed old changes: %+v", res)
	}
}

// While ingest is stale nobody can tell a silent node from a silent
// MQTT feed: offline comparisons pause with their states unchanged;
// battery and foreign nodes go on.
func TestEvaluateIngestStalePausesOfflineChecks(t *testing.T) {
	in := evInput()
	in.IngestStale = true
	in.Watches = append(in.Watches, users.NotifyWatch{UserID: evUser, Pubkey: evPkB})
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin, Enabled: true, Events: []string{users.NotifyForeignNew, users.NotifyObserverOffline}})
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(48 * time.Hour), BatteryMv: evMv(3100)}
	in.Nodes[evPkF] = notifyNode{Pubkey: evPkF, Name: "Foxtrot", Foreign: true}
	in.Observers = []notifyObserver{{ID: "OBS1", Name: "Roof", LastSeen: evAgo(48 * time.Hour)}}
	in.States = []users.NotifyState{
		evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeBattery, evPkA, users.NotifyGood),
		evStateRow(evAdmin, users.NotifyObserverOffline, "OBS1", users.NotifyGood),
		evStateRow(evAdmin, users.NotifyForeignNew, foreignBaselineSubject, users.NotifyGood),
	}
	res := evaluateNotifications(in)
	for _, k := range []struct {
		uid            int64
		event, subject string
	}{{evUser, users.NotifyNodeOffline, evPkA}, {evUser, users.NotifyNodeOffline, evPkB}, {evAdmin, users.NotifyObserverOffline, "OBS1"}} {
		if s, ok := evState(res, k.uid, k.event, k.subject); ok {
			t.Errorf("%s %s written as %q while ingest is stale", k.event, k.subject, s)
		}
	}
	if ch := res.Changes[evUser]; len(ch) != 1 || ch[0].Event != users.NotifyNodeBattery || ch[0].To != users.NotifyBad {
		t.Fatalf("user changes = %+v; want only the battery", ch)
	}
	if ch := res.Changes[evAdmin]; len(ch) != 1 || ch[0].Event != users.NotifyForeignNew || ch[0].Subject != evPkF {
		t.Fatalf("admin changes = %+v; want only the foreign node", ch)
	}

	in.IngestStale = false
	res = evaluateNotifications(in)
	if s, _ := evState(res, evUser, users.NotifyNodeOffline, evPkA); s != users.NotifyBad {
		t.Fatalf("offline check after ingest resumed = %q; want bad", s)
	}
	if s, _ := evState(res, evAdmin, users.NotifyObserverOffline, "OBS1"); s != users.NotifyBad {
		t.Fatalf("observer check after ingest resumed = %q; want bad", s)
	}
}

// After a feed outage, evidence older than the recovery counts as heard at
// the recovery: no node or observer goes offline until a full silent window
// has passed since then. A state that is already bad is not lifted by it.
func TestEvaluateGraceAfterIngestResumes(t *testing.T) {
	in := evInput()
	in.Prefs = append(in.Prefs, users.NotifyPrefs{UserID: evAdmin, Enabled: true, Events: []string{users.NotifyObserverOffline}})
	in.Watches = append(in.Watches, users.NotifyWatch{UserID: evUser, Pubkey: evPkB})
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion", LastSeen: evAgo(25 * time.Hour)}
	in.Nodes[evPkB] = notifyNode{Pubkey: evPkB, Name: "Bravo", Role: "companion", LastSeen: evAgo(48 * time.Hour)}
	in.Observers = []notifyObserver{{ID: "OBS1", Name: "Roof", LastSeen: evAgo(25 * time.Hour)}, {ID: "OBS2", Name: "Mast", LastSeen: evAgo(48 * time.Hour)}}
	in.States = []users.NotifyState{
		evStateRow(evUser, users.NotifyNodeOffline, evPkA, users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeOffline, evPkB, users.NotifyBad),
		evStateRow(evAdmin, users.NotifyObserverOffline, "OBS1", users.NotifyGood),
		evStateRow(evAdmin, users.NotifyObserverOffline, "OBS2", users.NotifyBad),
	}
	in.IngestResumedAt = evNow.Add(-10 * time.Minute)
	if res := evaluateNotifications(in); len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("10 minutes after recovery: changes %+v, states %+v; want none", res.Changes, res.States)
	}

	// One silent window (24 h companions, observerStaleMinutes 1440) plus a
	// minute after recovery, still no new evidence: offline.
	in.Now = in.IngestResumedAt.Add(24*time.Hour + time.Minute)
	res := evaluateNotifications(in)
	if ch := res.Changes[evUser]; len(ch) != 1 || ch[0].Subject != evPkA || ch[0].To != users.NotifyBad {
		t.Fatalf("node changes after the window = %+v; want Alpha offline", ch)
	}
	if ch := res.Changes[evAdmin]; len(ch) != 1 || ch[0].Subject != "OBS1" || ch[0].To != users.NotifyBad {
		t.Fatalf("observer changes after the window = %+v; want Roof offline", ch)
	}

	// A node heard after the recovery behaves as without the grace.
	in.Now = evNow
	in.Nodes[evPkB] = notifyNode{Pubkey: evPkB, Name: "Bravo", Role: "companion", LastSeen: evAgo(5 * time.Minute)}
	in.Observers[1].LastSeen = evAgo(5 * time.Minute)
	res = evaluateNotifications(in)
	if ch := res.Changes[evUser]; len(ch) != 1 || ch[0].Subject != evPkB || ch[0].To != users.NotifyGood {
		t.Fatalf("node heard after recovery = %+v; want Bravo back online", ch)
	}
	if ch := res.Changes[evAdmin]; len(ch) != 1 || ch[0].Subject != "OBS2" || ch[0].To != users.NotifyGood {
		t.Fatalf("observer heard after recovery = %+v; want Mast back online", ch)
	}
}
