package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// NotifyRelayConfig configures the push relay. With analyzerId it enables a
// pre-enrolled sender (manual enrollment); with only an origin it locks
// self-service enrollment to that relay. The credential
// comes from the environment so it never appears in the public configuration.
type NotifyRelayConfig struct {
	Origin     string   `json:"origin"`
	AnalyzerID string   `json:"analyzerId"`
	Platforms  []string `json:"platforms"`
}

func validRelayPlatforms(platforms []string) error {
	if len(platforms) < 1 || len(platforms) > 2 {
		return errors.New("pushRelay.platforms must contain ios or android")
	}
	seen := make(map[string]bool, len(platforms))
	for _, platform := range platforms {
		if (platform != "ios" && platform != "android") || seen[platform] {
			return errors.New("pushRelay.platforms must contain distinct ios or android entries")
		}
		seen[platform] = true
	}
	return nil
}

// resolveSelfEnrollRelay reads a pushRelay block without an analyzerId: the
// admin enrolls from the admin page, but only with this relay. Platforms
// default to the matching preset's, or iOS and Android.
func resolveSelfEnrollRelay(cfg *NotifyRelayConfig) (string, []string, error) {
	origin, err := canonicalRelayOrigin(cfg.Origin)
	if err != nil {
		return "", nil, fmt.Errorf("pushRelay.origin: %w", err)
	}
	platforms := cfg.Platforms
	if len(platforms) == 0 {
		platforms = defaultRelayPlatforms
		if preset, ok := relayPresetFor(origin); ok {
			platforms = preset.Platforms
		}
	}
	if err := validRelayPlatforms(platforms); err != nil {
		return "", nil, err
	}
	return origin, append([]string(nil), platforms...), nil
}

func (ns *notifySettings) configureRelay(cfg *NotifyRelayConfig, getenv func(string) string) error {
	if cfg == nil {
		return nil
	}
	credential := strings.TrimSpace(getenv("CORESCOPE_PUSH_RELAY_CREDENTIAL"))
	if credential == "" || strings.TrimSpace(cfg.AnalyzerID) == "" {
		return errors.New("pushRelay requires analyzerId and CORESCOPE_PUSH_RELAY_CREDENTIAL")
	}
	if err := validRelayPlatforms(cfg.Platforms); err != nil {
		return err
	}
	origin := strings.TrimSuffix(strings.TrimSpace(cfg.Origin), "/")
	client, err := newNotifyRelayClient(origin, nil)
	if err != nil {
		return fmt.Errorf("pushRelay.origin: %w", err)
	}
	ns.relay, ns.relayOrigin = client, origin
	ns.relayAnalyzerID, ns.relayCredential = cfg.AnalyzerID, credential
	ns.relayPlatforms = append([]string(nil), cfg.Platforms...)
	return nil
}

// Prune at user management startup, before any notification workers run.
func pruneUnconfiguredPushState(st *users.Store, analyzerID string) error {
	counts, err := st.PrunePushState(analyzerID)
	if err != nil {
		return fmt.Errorf("prune push state for unconfigured analyzers: %w", err)
	}
	if counts.Bindings != 0 || counts.Intents != 0 || counts.Jobs != 0 {
		log.Printf("[notify] removed %d push binding(s), %d intent(s), %d job(s) for analyzers no longer configured",
			counts.Bindings, counts.Intents, counts.Jobs)
	}
	return nil
}

func notifyPushKind(event, state string) string {
	switch event {
	case users.NotifyNodeOffline:
		switch state {
		case users.NotifyBad:
			return "node.not_heard"
		case users.NotifyGood:
			return "node.heard_again"
		}
	case users.NotifyNodeBattery:
		switch state {
		case users.NotifyBad:
			return "node.battery_low"
		case users.NotifyGood:
			return "node.battery_recovered"
		}
	}
	return ""
}

func notifyPushEvents(changes map[int64][]notifyChange, accounts map[int64]users.User, nodes map[string]notifyNode) []users.PushEvent {
	var events []users.PushEvent
	for uid, transitions := range changes {
		if accounts[uid].Status != users.StatusActive {
			continue
		}
		for _, change := range transitions {
			kind := notifyPushKind(change.Event, change.To)
			if kind == "" || !notifyPubkeyRE.MatchString(change.Subject) {
				continue
			}
			at := change.At.UTC()
			events = append(events, users.PushEvent{
				EventID: rand.Text(), UserID: uid, Event: change.Event,
				Kind: kind, NodePubkey: change.Subject, NodeName: mailSafeText(nodes[change.Subject].Name),
				OccurredAt: at, ExpiresAt: at.Add(time.Hour),
			})
		}
	}
	return events
}

func notifyPushRetry(err error, attempts int) time.Duration {
	if err == nil {
		return 0
	}
	var relayErr *notifyRelayError
	if errors.As(err, &relayErr) {
		switch {
		case relayErr.Status == http.StatusTooManyRequests, relayErr.Status >= 500:
			if relayErr.RetryAfter > 0 {
				return relayErr.RetryAfter
			}
		default:
			return 0
		}
	}
	if attempts >= 6 {
		return 5 * time.Minute
	}
	return (5 * time.Second) << attempts
}

// relayDestination queues push jobs for the enrolled relay with each
// evaluation's states, and drains that queue on its own loop (run).
type relayDestination struct {
	a   *authService
	now func() time.Time
	// tickEpoch is the enrollment epoch when the current tick began. Only
	// the notifier loop goroutine touches it.
	tickEpoch uint64
}

func (d *relayDestination) beginTick() { d.tickEpoch = d.a.relayDelivery.epoch.Load() }

// stage queues push jobs for the enrolled relay unless delivery is paused
// or the enrollment changed since the tick began. It holds the delivery
// state's read lock until the states and jobs are written, so a disconnect
// or pause cannot slip between the check and the write.
func (d *relayDestination) stage(c notifyChanges, out *users.NotifyOutbox) func() {
	state := &d.a.relayDelivery
	state.mu.RLock()
	settings := d.a.relaySettings()
	if settings.relay == nil {
		state.mu.RUnlock()
		return nil
	}
	if !d.a.relayDeliveryPaused() && d.tickEpoch == state.epoch.Load() {
		out.PushAnalyzerID = settings.relayAnalyzerID
		out.PushEvents = notifyPushEvents(c.byUser, c.accounts, c.nodes)
	}
	return state.mu.RUnlock
}

// deliver sends nothing within the tick: run drains the queue.
func (d *relayDestination) deliver(context.Context, time.Time, notifyChanges) int { return 0 }
func (d *relayDestination) logKey() string                                        { return "" }

func (d *relayDestination) run(stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.dispatch(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[notify] process push queue: %v", err)
			}
		}
	}
}

func (d *relayDestination) dispatch(ctx context.Context) error {
	state := &d.a.relayDelivery
	state.mu.RLock()
	settings := d.a.relaySettings()
	epoch := state.epoch.Load()
	if settings.relay == nil || d.a.relayDeliveryPaused() {
		state.mu.RUnlock()
		return nil
	}
	jobs, err := d.a.st.DuePushJobs(settings.relayAnalyzerID, d.now(), 25)
	state.mu.RUnlock()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.dispatchJob(ctx, settings, job, epoch); err != nil {
			return err
		}
	}
	return nil
}

func (d *relayDestination) dispatchJob(ctx context.Context, settings notifySettings, job users.PushJob, epoch uint64) error {
	state := &d.a.relayDelivery
	state.mu.RLock()
	defer state.mu.RUnlock()
	if epoch != state.epoch.Load() || d.a.relayDeliveryPaused() {
		return nil
	}
	st := d.a.st
	eligible, err := st.PushJobEligible(job, d.now())
	if err != nil {
		return err
	}
	if eligible {
		switch job.Operation {
		case "deliver":
			_, err = settings.relay.Deliver(ctx, settings.relayCredential, notifyRelayDeliveryRequest{
				EventID: job.EventID, SubscriptionID: job.SubscriptionID, Kind: job.Kind,
				NodePubkey: job.NodePubkey, NodeName: job.NodeName, OccurredAt: job.OccurredAt, ExpiresAt: job.ExpiresAt,
			})
		case "revoke":
			err = settings.relay.RevokeSubscription(ctx, settings.relayCredential, job.SubscriptionID)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := d.now()
	delay := notifyPushRetry(err, job.Attempts)
	if delay > 0 && now.Add(delay).Before(job.ExpiresAt) {
		return st.RetryPushJob(job.ID, now.Add(delay))
	}
	if err != nil {
		log.Printf("[notify] drop push %s job %d: %v", job.Operation, job.ID, err)
	}
	return st.CompletePushJob(job.ID)
}
