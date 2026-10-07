package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// notifier evaluates node notifications on a timer and mails the changes
// (docs/specs/2026-10-07-node-notifications-design.md). authService.notify
// is nil unless userManagement.notifications.enabled.
type notifier struct {
	a   *authService
	src notifySource
	now func() time.Time
	// ingestStale is the last tick's verdict, so the pause and the resume
	// are each logged once. Only the loop goroutine touches it.
	ingestStale bool
}

func newNotifier(a *authService, src notifySource, now func() time.Time) *notifier {
	return &notifier{a: a, src: src, now: now}
}

// notifySendTimeout bounds one mail send inside a tick.
const notifySendTimeout = 30 * time.Second

// notifyIngestStaleAfter: when the newest packet in the store is older,
// offline checks pause (an MQTT or ingestor outage would otherwise mail
// every watcher "offline" and later "back online").
const notifyIngestStaleAfter = 30 * time.Minute

// checkIngest reports whether ingest is stale at now and logs when that
// verdict changes.
func (n *notifier) checkIngest(now time.Time) bool {
	newest := n.src.newestPacket()
	stale := newest.IsZero() || now.Sub(newest) > notifyIngestStaleAfter
	switch {
	case stale && !n.ingestStale:
		since := "(no packets)"
		if !newest.IsZero() {
			since = newest.UTC().Format(time.RFC3339)
		}
		log.Printf("[notify] ingest stale since %s; offline checks paused", since)
	case !stale && n.ingestStale:
		log.Printf("[notify] ingest fresh again; offline checks resumed")
	}
	n.ingestStale = stale
	return stale
}

// loop evaluates every interval until stop closes. The first evaluation
// runs one interval after startup. A tick in flight sees its context
// cancelled when stop closes, so a slow mail provider does not hold up
// the shutdown.
func (n *notifier) loop(stop <-chan struct{}, every time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			n.safeTick(ctx)
		}
	}
}

// safeTick runs one evaluation; a panic is logged and the next tick runs.
func (n *notifier) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[notify] evaluation panicked, the next one runs as planned: %v", r)
		}
	}()
	n.tick(ctx)
}

// tick reads all preferences, watches and states (one query each), the
// accounts and the analyzer snapshot they need, evaluates, deletes the
// dropped states and writes the new ones, and then mails each user's
// changes. Users without a preferences row and users with notifications
// off are evaluated too (the evaluator gives the first the defaults and
// keeps the second's states current without changes). A failed read or
// state write ends the tick without mail; the next tick starts over.
func (n *notifier) tick(ctx context.Context) {
	if !n.src.ready() {
		return
	}
	now := n.now()
	stale := n.checkIngest(now)
	st := n.a.st
	prefs, err := st.AllNotifyPrefs()
	if err != nil {
		log.Printf("[notify] read preferences: %v", err)
		return
	}
	stored, err := st.AllWatches()
	if err != nil {
		log.Printf("[notify] read watches: %v", err)
		return
	}
	if len(prefs) == 0 && len(stored) == 0 {
		return
	}
	states, err := st.AllNotifyStates()
	if err != nil {
		log.Printf("[notify] read states: %v", err)
		return
	}
	// The analyzer snapshot is keyed by lowercase pubkey; normalise the
	// watches once so every lookup and state subject uses that form.
	watches := make([]users.NotifyWatch, 0, len(stored))
	watchSeen := make(map[users.NotifyKey]bool, len(stored))
	seen := map[string]bool{}
	var pubkeys []string
	for _, w := range stored {
		w.Pubkey = strings.ToLower(w.Pubkey)
		k := users.NotifyKey{UserID: w.UserID, Subject: w.Pubkey}
		if watchSeen[k] {
			continue
		}
		watchSeen[k] = true
		watches = append(watches, w)
		if !seen[w.Pubkey] {
			seen[w.Pubkey] = true
			pubkeys = append(pubkeys, w.Pubkey)
		}
	}
	byUser := make(map[int64]users.NotifyPrefs, len(prefs))
	var ids []int64
	for _, p := range prefs {
		byUser[p.UserID] = p
		ids = append(ids, p.UserID)
	}
	noPrefs := map[int64]bool{}
	for _, w := range watches {
		if _, ok := byUser[w.UserID]; !ok && !noPrefs[w.UserID] {
			noPrefs[w.UserID] = true
			ids = append(ids, w.UserID)
		}
	}
	accounts, err := st.UsersByID(ids)
	if err != nil {
		log.Printf("[notify] read accounts: %v", err)
		return
	}
	wantForeign, wantObservers := false, false
	for _, p := range prefs {
		if accounts[p.UserID].Role != users.RoleAdmin {
			continue
		}
		wantForeign = wantForeign || p.Has(users.NotifyForeignNew)
		wantObservers = wantObservers || p.Has(users.NotifyObserverOffline)
	}
	nodes, err := n.src.nodes(pubkeys, wantForeign)
	if err != nil {
		log.Printf("[notify] read nodes: %v", err)
		return
	}
	var infra []string
	for _, pk := range pubkeys {
		if nd, ok := nodes[pk]; ok && notifyInfra(nd.Role) {
			infra = append(infra, pk)
		}
	}
	var observers []notifyObserver
	if wantObservers {
		if observers, err = n.src.observers(); err != nil {
			log.Printf("[notify] read observers: %v", err)
			return
		}
	}
	res := evaluateNotifications(notifyInput{Now: now, Health: n.src.health(), LowMv: n.src.lowBatteryMv(),
		Accounts: accounts, Prefs: prefs, Watches: watches, States: states, Nodes: nodes,
		Heard: n.src.lastHeard(pubkeys), Relayed: n.src.lastRelayed(infra), Observers: observers, IngestStale: stale})
	if err := st.DeleteNotifyStates(res.Drop); err != nil {
		log.Printf("[notify] delete dropped states, nothing mailed this time: %v", err)
		return
	}
	if len(res.States) > 0 {
		if err := st.WriteNotifyStates(res.States); err != nil {
			log.Printf("[notify] write states, nothing mailed this time: %v", err)
			return
		}
	}
	n.deliver(ctx, now, res.Changes, accounts, byUser)
}

// notifySkipReason says why a user's changes are not mailed; "" means send.
// Skipped changes are already stored and are never mailed later.
func notifySkipReason(u users.User, p users.NotifyPrefs, userMails, totalMails int, ns notifySettings) string {
	switch {
	case u.Status != users.StatusActive:
		return "account not active"
	case u.EmailBouncing:
		return "address bounces"
	case !p.Enabled:
		return "notifications off"
	case userMails >= ns.perUserPerDay:
		return "daily limit for this user reached"
	case totalMails >= ns.maxMailsPerDay:
		return "daily limit for this instance reached"
	}
	return ""
}

// deliver sends one mail per user with changes, within the limits counted
// from mail_log over the last 24 hours (this tick's mails included). A user
// with changes but no preferences row was evaluated with the defaults; the
// row (and so the unsubscribe token) is created before the first mail.
// Once ctx is cancelled (shutdown) the remaining users are not mailed;
// their changes are already stored, like any other skipped change.
func (n *notifier) deliver(ctx context.Context, now time.Time, changes map[int64][]notifyChange,
	accounts map[int64]users.User, prefs map[int64]users.NotifyPrefs) {
	if len(changes) == 0 {
		return
	}
	total, perUser, err := n.a.st.NotifyMailCounts(now.Add(-24 * time.Hour))
	if err != nil {
		log.Printf("[notify] count mails, nothing mailed this time: %v", err)
		return
	}
	ids := make([]int64, 0, len(changes))
	for uid := range changes {
		ids = append(ids, uid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, uid := range ids {
		if ctx.Err() != nil {
			log.Printf("[notify] shutting down: %d user(s) with changes not mailed", len(ids)-i)
			return
		}
		u := accounts[uid]
		p, ok := prefs[uid]
		if !ok {
			p = users.DefaultNotifyPrefs(uid)
		}
		if why := notifySkipReason(u, p, perUser[uid], total, n.a.set.notify); why != "" {
			log.Printf("[notify] user #%d: %d change(s) not mailed: %s", uid, len(changes[uid]), why)
			continue
		}
		if !ok {
			if p, err = n.a.st.NotifyPrefsFor(uid); err != nil {
				log.Printf("[notify] user #%d: create preferences, %d change(s) not mailed: %v", uid, len(changes[uid]), err)
				continue
			}
		}
		sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
		err := n.a.sendMail(sendCtx, &u, users.NotifyMailPurpose, n.a.notifyMail(u, p, changes[uid]))
		cancel()
		if err != nil {
			continue // logged by sendMail; the changes stay recorded
		}
		total++
		perUser[uid]++
	}
}

// notifyMail is the one mail for all of a user's changes in one evaluation.
func (a *authService) notifyMail(u users.User, p users.NotifyPrefs, changes []notifyChange) mailer.Message {
	base := a.set.baseURL.String()
	lines := make([]mailLine, 0, len(changes))
	for _, c := range changes {
		lines = append(lines, mailLine{text: notifyChangeText(c), url: notifySubjectURL(base, c)})
	}
	subject := fmt.Sprintf("%d changes on your watched nodes", len(changes))
	if len(changes) == 1 {
		subject = "1 change on your watched nodes"
	}
	msg := a.render(u.Email, u.DisplayName, "notify", mailContent{
		subject: subject, greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Changes since the last check:"},
		lines:       lines,
		actionLabel: "Manage notifications", actionURL: base + "/#/account?section=notifications",
		footer: &mailLine{text: "You get this mail because you asked " + a.set.baseURL.Host +
			" to watch these nodes. Turn all notification mails off with one click:", url: a.link("unsubscribe", p.UnsubToken)},
	})
	msg.Headers = map[string]string{
		"List-Unsubscribe":      "<" + base + "/api/notifications/unsubscribe?token=" + url.QueryEscape(p.UnsubToken) + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	}
	return msg
}

// notifyChangeText is one line of the mail: name, what happened, when (UTC).
func notifyChangeText(c notifyChange) string {
	bad := c.To == users.NotifyBad
	var what string
	switch c.Event {
	case users.NotifyNodeOffline:
		what = "back online"
		if bad {
			what = "offline"
		}
	case users.NotifyNodeBattery:
		what = "battery recovered"
		if bad {
			what = "battery low"
		}
		if c.BatteryMv != nil {
			what += fmt.Sprintf(" (%d mV)", *c.BatteryMv)
		}
	case users.NotifyForeignNew:
		what = "new foreign node"
	case users.NotifyObserverOffline:
		what = "observer back online"
		if bad {
			what = "observer offline"
		}
	}
	return c.Name + ": " + what + ", " + c.At.UTC().Format("2006-01-02 15:04 UTC")
}

// notifySubjectURL links a change to its node or observer page.
func notifySubjectURL(base string, c notifyChange) string {
	if c.Event == users.NotifyObserverOffline {
		return base + "/#/observers/" + url.PathEscape(c.Subject)
	}
	return base + "/#/nodes/" + url.PathEscape(c.Subject)
}
