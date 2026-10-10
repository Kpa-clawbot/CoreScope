package main

import (
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// Node notification evaluator (docs/specs/2026-10-07-node-notifications-design.md).
// evaluateNotifications is a pure function: everything it reads is in
// notifyInput, the time included, so it is tested without a clock or a
// database. The notifier (notify_service.go) gathers the input and acts on
// the result.

// notifyNode is one analyzer node row the evaluator needs.
type notifyNode struct {
	Pubkey    string // lowercase hex
	Name      string
	Role      string // as stored; compared lowercase
	LastSeen  string // nodes.last_seen, "" when NULL
	BatteryMv *int   // latest advert telemetry; nil when the node reports none
	Foreign   bool   // nodes.foreign_advert = 1
}

// notifyObserver is one active (not soft-deleted) observer.
type notifyObserver struct {
	ID       string
	Name     string
	LastSeen string // observers.last_seen, "" when NULL
}

// notifyInput is everything one evaluation reads.
type notifyInput struct {
	Now       time.Time
	Health    HealthThresholds // resolved, as Config.GetHealthThresholds returns it
	LowMv     int              // Config.LowBatteryMv
	Accounts  map[int64]users.User
	Prefs     []users.NotifyPrefs
	Watches   []users.NotifyWatch
	States    []users.NotifyState
	Nodes     map[string]notifyNode // watched nodes, plus every foreign node when an admin chose foreign.new
	Heard     map[string]time.Time  // newest packet involving the node in the packet store (node page "Last Heard")
	Relayed   map[string]time.Time  // last relay hop, used for repeaters and rooms only (#1598)
	Observers []notifyObserver      // nil unless an admin chose observer.offline
	// IngestStale: the newest packet in the store is old, so a silent node
	// and a silent feed look the same. node.offline and observer.offline
	// are then not compared (states unchanged, no changes).
	IngestStale bool
	// IngestResumedAt: when the last stale period ended (zero: none since
	// startup). For a node or observer whose stored state is good, evidence
	// older than this counts as heard at this time, so nothing goes offline
	// until a full silent window has passed after the feed came back.
	IngestResumedAt time.Time
	// External is the fresh externalAlerts feed; nil when it is not
	// configured, nobody chose node.external, or the feed is stale. nil
	// leaves every node.external state unchanged.
	External *externalFeed
}

// notifyChange is one transition for one user.
type notifyChange struct {
	Event     string
	Subject   string // node pubkey or observer id
	Name      string // display name at evaluation time
	To        string // users.NotifyGood, users.NotifyBad or users.NotifyTold
	BatteryMv *int   // node.battery only
	Text      string // node.external only: the alert text ("" when resolved)
	URL       string // node.external only: the alert's link, "" for the node page
	At        time.Time
}

// notifyResult: Changes per user, ordered by event then name; States are
// the rows to write (new or changed), disabled users included; Drop are the
// rows to delete (admin-event rows of users who are no longer admin).
type notifyResult struct {
	Changes map[int64][]notifyChange
	States  []users.NotifyState
	Drop    []users.NotifyKey
}

const (
	// batteryHysteresisMv: a low battery is good again only at lowMv + 100.
	batteryHysteresisMv = 100
	// foreignBaselineSubject marks that an admin's foreign.new baseline
	// exists: the first evaluation after opting in stores every current
	// foreign node as told without mailing; afterwards a foreign node
	// without a row is new.
	foreignBaselineSubject = "*"
)

var notifyEventRank = map[string]int{
	users.NotifyNodeOffline: 0, users.NotifyNodeBattery: 1, users.NotifyNodeExternal: 2,
	users.NotifyForeignNew: 3, users.NotifyObserverOffline: 4,
}

// notifyInfra reports the roles whose relay activity counts as heard
// (GetHealthMs and roles.js use the same two).
func notifyInfra(role string) bool {
	r := strings.ToLower(role)
	return r == "repeater" || r == "room"
}

// nodeLabel is the node's name made safe for a mail line (mailSafeText),
// or the first 8 hex characters of its key.
func nodeLabel(n notifyNode, known bool, pk string) string {
	if name := mailSafeText(n.Name); known && name != "" {
		return name
	}
	if len(pk) > 8 {
		return pk[:8]
	}
	return pk
}

// nodeOnlineState is good when the node was heard within its role's silent
// window, like roles.js getNodeStatus: the latest of last_seen, the packet
// store's last heard and, for repeaters and rooms, the last relay hop. A
// node missing from the analyzer database is bad. While prev is good, the
// latest evidence is never older than in.IngestResumedAt (the outage grace).
func nodeOnlineState(pk string, n notifyNode, known bool, prev string, in *notifyInput) string {
	if !known {
		return users.NotifyBad
	}
	last, _ := parseRelayTS(n.LastSeen)
	if t, ok := in.Heard[pk]; ok && t.After(last) {
		last = t
	}
	if notifyInfra(n.Role) {
		if t, ok := in.Relayed[pk]; ok && t.After(last) {
			last = t
		}
	}
	if prev == users.NotifyGood && last.Before(in.IngestResumedAt) {
		last = in.IngestResumedAt
	}
	_, silentMs := in.Health.GetHealthMs(strings.ToLower(n.Role))
	if last.IsZero() || in.Now.Sub(last) >= time.Duration(silentMs)*time.Millisecond {
		return users.NotifyBad
	}
	return users.NotifyGood
}

// batteryState applies the low-battery threshold with hysteresis.
func batteryState(mv, lowMv int, prev string) string {
	if prev == users.NotifyBad {
		if mv >= lowMv+batteryHysteresisMv {
			return users.NotifyGood
		}
		return users.NotifyBad
	}
	if mv < lowMv {
		return users.NotifyBad
	}
	return users.NotifyGood
}

// observerState: bad when last_seen is older than observerStaleMinutes,
// good again within observerOnlineMinutes. While prev is good, last_seen
// is never older than resumed (the outage grace, zero for none).
func observerState(o notifyObserver, h HealthThresholds, now, resumed time.Time, prev string) string {
	t, ok := parseRelayTS(o.LastSeen)
	if prev == users.NotifyGood && !resumed.IsZero() && (!ok || t.Before(resumed)) {
		t, ok = resumed, true
	}
	if !ok {
		return users.NotifyBad
	}
	age := now.Sub(t)
	if prev == users.NotifyBad {
		if age <= time.Duration(h.ObserverOnlineMinutes)*time.Minute {
			return users.NotifyGood
		}
		return users.NotifyBad
	}
	if age > time.Duration(h.ObserverStaleMinutes)*time.Minute {
		return users.NotifyBad
	}
	return users.NotifyGood
}

// evalExternal compares one watched node's current external alerts with the
// stored node.external states of one user. The first fresh feed after the
// node is watched (no baseline row) stores the current alerts silently;
// afterwards a new or returning alert is a bad change and a stored bad alert
// missing from the feed is a good (resolved) change.
func evalExternal(res *notifyResult, prev map[users.NotifyKey]string, bad []string, current map[string]externalAlert,
	uid int64, pk, name string, mailing bool, now time.Time) {
	base := users.NotifyKey{UserID: uid, Event: users.NotifyNodeExternal, Subject: pk + "/" + externalBaselineKey}
	_, baselined := prev[base]
	if !baselined {
		res.States = append(res.States, users.NotifyState{NotifyKey: base, State: users.NotifyGood, ChangedAt: now})
	}
	emit := func(key, state string, a externalAlert) {
		k := users.NotifyKey{UserID: uid, Event: users.NotifyNodeExternal, Subject: pk + "/" + key}
		res.States = append(res.States, users.NotifyState{NotifyKey: k, State: state, ChangedAt: now})
		if baselined && mailing {
			res.Changes[uid] = append(res.Changes[uid], notifyChange{Event: users.NotifyNodeExternal, Subject: k.Subject,
				Name: name, To: state, Text: a.Text, URL: a.URL, At: now})
		}
	}
	for _, key := range sortedExternalKeys(current) {
		if prev[users.NotifyKey{UserID: uid, Event: users.NotifyNodeExternal, Subject: pk + "/" + key}] != users.NotifyBad {
			emit(key, users.NotifyBad, current[key])
		}
	}
	sort.Strings(bad)
	for _, key := range bad {
		if _, still := current[key]; !still {
			emit(key, users.NotifyGood, externalAlert{})
		}
	}
}

// evaluateNotifications computes every chosen (user, event, subject) state
// and compares it with the stored one. A subject without a stored state is
// stored without a change (a restart or a new watch never mails); a stored
// state that differs is a change. Users missing from Accounts are skipped.
// A user with watches but no preferences row gets the defaults (enabled,
// node events). A disabled user's states are kept current without changes,
// so turning notifications on again never mails what happened while off.
// Admin events are evaluated only for admins; a non-admin's admin-event
// rows are dropped, like opting out, so a re-promotion starts silently.
func evaluateNotifications(in notifyInput) notifyResult {
	prev := make(map[users.NotifyKey]string, len(in.States))
	// extBad: per user and node, the node.external keys stored as bad.
	extBad := map[int64]map[string][]string{}
	for _, s := range in.States {
		prev[s.NotifyKey] = s.State
		if s.Event == users.NotifyNodeExternal && s.State == users.NotifyBad {
			if pk, key, ok := strings.Cut(s.Subject, "/"); ok && key != externalBaselineKey {
				if extBad[s.UserID] == nil {
					extBad[s.UserID] = map[string][]string{}
				}
				extBad[s.UserID][pk] = append(extBad[s.UserID][pk], key)
			}
		}
	}
	watched := map[int64][]string{}
	for _, w := range in.Watches {
		watched[w.UserID] = append(watched[w.UserID], w.Pubkey)
	}
	var foreign []string
	for pk, n := range in.Nodes {
		if n.Foreign {
			foreign = append(foreign, pk)
		}
	}
	sort.Strings(foreign)

	res := notifyResult{Changes: map[int64][]notifyChange{}}
	for _, s := range in.States {
		if u, ok := in.Accounts[s.UserID]; ok && u.Role != users.RoleAdmin && users.IsAdminNotifyEvent(s.Event) {
			res.Drop = append(res.Drop, s.NotifyKey)
		}
	}
	prefs := make([]users.NotifyPrefs, 0, len(in.Prefs)+len(watched))
	hasPrefs := make(map[int64]bool, len(in.Prefs))
	for _, p := range in.Prefs {
		prefs = append(prefs, p)
		hasPrefs[p.UserID] = true
	}
	var defaulted []int64
	for uid := range watched {
		if !hasPrefs[uid] {
			defaulted = append(defaulted, uid)
		}
	}
	sort.Slice(defaulted, func(i, j int) bool { return defaulted[i] < defaulted[j] })
	for _, uid := range defaulted {
		prefs = append(prefs, users.DefaultNotifyPrefs(uid))
	}
	mailing := false // Enabled of the user being evaluated
	store := func(k users.NotifyKey, state string) {
		res.States = append(res.States, users.NotifyState{NotifyKey: k, State: state, ChangedAt: in.Now})
	}
	compare := func(k users.NotifyKey, name, state string, mv *int) {
		old, had := prev[k]
		if had && old == state {
			return
		}
		store(k, state)
		if had && mailing {
			res.Changes[k.UserID] = append(res.Changes[k.UserID],
				notifyChange{Event: k.Event, Subject: k.Subject, Name: name, To: state, BatteryMv: mv, At: in.Now})
		}
	}

	for _, p := range prefs {
		u, ok := in.Accounts[p.UserID]
		if !ok {
			continue
		}
		mailing = p.Enabled
		for _, pk := range watched[p.UserID] {
			n, known := in.Nodes[pk]
			name := nodeLabel(n, known, pk)
			if p.Has(users.NotifyNodeOffline) && !in.IngestStale {
				k := users.NotifyKey{UserID: p.UserID, Event: users.NotifyNodeOffline, Subject: pk}
				compare(k, name, nodeOnlineState(pk, n, known, prev[k], &in), nil)
			}
			if p.Has(users.NotifyNodeBattery) && known && n.BatteryMv != nil {
				k := users.NotifyKey{UserID: p.UserID, Event: users.NotifyNodeBattery, Subject: pk}
				compare(k, name, batteryState(*n.BatteryMv, in.LowMv, prev[k]), n.BatteryMv)
			}
			if p.Has(users.NotifyNodeExternal) && in.External != nil {
				evalExternal(&res, prev, extBad[p.UserID][pk], in.External.Alerts[pk], p.UserID, pk, name, mailing, in.Now)
			}
		}
		if u.Role != users.RoleAdmin {
			continue
		}
		if p.Has(users.NotifyForeignNew) {
			base := users.NotifyKey{UserID: p.UserID, Event: users.NotifyForeignNew, Subject: foreignBaselineSubject}
			_, baselined := prev[base]
			if !baselined {
				store(base, users.NotifyGood)
			}
			for _, pk := range foreign {
				k := users.NotifyKey{UserID: p.UserID, Event: users.NotifyForeignNew, Subject: pk}
				if _, told := prev[k]; told {
					continue
				}
				store(k, users.NotifyTold)
				if baselined && mailing {
					res.Changes[p.UserID] = append(res.Changes[p.UserID], notifyChange{Event: users.NotifyForeignNew, Subject: pk,
						Name: nodeLabel(in.Nodes[pk], true, pk), To: users.NotifyTold, At: in.Now})
				}
			}
		}
		if p.Has(users.NotifyObserverOffline) && !in.IngestStale {
			for _, o := range in.Observers {
				k := users.NotifyKey{UserID: p.UserID, Event: users.NotifyObserverOffline, Subject: o.ID}
				name := mailSafeText(o.Name)
				if name == "" {
					name = o.ID
				}
				compare(k, name, observerState(o, in.Health, in.Now, in.IngestResumedAt, prev[k]), nil)
			}
		}
	}
	for _, list := range res.Changes {
		sort.SliceStable(list, func(i, j int) bool {
			if a, b := notifyEventRank[list[i].Event], notifyEventRank[list[j].Event]; a != b {
				return a < b
			}
			if list[i].Name != list[j].Name {
				return list[i].Name < list[j].Name
			}
			return list[i].Subject < list[j].Subject
		})
	}
	return res
}
