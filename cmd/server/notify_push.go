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

// NotifyRelayConfig enables a pre-enrolled NodeScope sender. The credential
// comes from the environment so it never appears in the public configuration.
type NotifyRelayConfig struct {
	Origin     string   `json:"origin"`
	AnalyzerID string   `json:"analyzerId"`
	Platforms  []string `json:"platforms"`
}

func (ns *notifySettings) configureRelay(cfg *NotifyRelayConfig, getenv func(string) string) error {
	if cfg == nil {
		return nil
	}
	credential := strings.TrimSpace(getenv("CORESCOPE_NODESCOPE_PUSH_CREDENTIAL"))
	if credential == "" || strings.TrimSpace(cfg.AnalyzerID) == "" {
		return errors.New("nodeScopePush requires analyzerId and CORESCOPE_NODESCOPE_PUSH_CREDENTIAL")
	}
	if len(cfg.Platforms) < 1 || len(cfg.Platforms) > 2 {
		return errors.New("nodeScopePush.platforms must contain ios or android")
	}
	seen := make(map[string]bool, len(cfg.Platforms))
	for _, platform := range cfg.Platforms {
		if (platform != "ios" && platform != "android") || seen[platform] {
			return errors.New("nodeScopePush.platforms must contain distinct ios or android entries")
		}
		seen[platform] = true
	}
	origin := strings.TrimSuffix(strings.TrimSpace(cfg.Origin), "/")
	client, err := newNotifyRelayClient(origin, nil)
	if err != nil {
		return fmt.Errorf("nodeScopePush.origin: %w", err)
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

func (n *notifier) pushLoop(stop <-chan struct{}) {
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
			if err := n.dispatchPush(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[notify] process NodeScope push queue: %v", err)
			}
		}
	}
}

func (n *notifier) dispatchPush(ctx context.Context) error {
	state := &n.a.nodeScopeDelivery
	state.mu.RLock()
	settings := n.a.nodeScopeRelaySettings()
	epoch := state.epoch.Load()
	if settings.relay == nil || n.a.nodeScopeDeliveryPaused() {
		state.mu.RUnlock()
		return nil
	}
	jobs, err := n.a.st.DuePushJobs(settings.relayAnalyzerID, n.now(), 25)
	state.mu.RUnlock()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := n.dispatchPushJob(ctx, settings, job, epoch); err != nil {
			return err
		}
	}
	return nil
}

func (n *notifier) dispatchPushJob(ctx context.Context, settings notifySettings, job users.PushJob, epoch uint64) error {
	state := &n.a.nodeScopeDelivery
	state.mu.RLock()
	defer state.mu.RUnlock()
	if epoch != state.epoch.Load() || n.a.nodeScopeDeliveryPaused() {
		return nil
	}
	st := n.a.st
	eligible, err := st.PushJobEligible(job, n.now())
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
	now := n.now()
	delay := notifyPushRetry(err, job.Attempts)
	if delay > 0 && now.Add(delay).Before(job.ExpiresAt) {
		return st.RetryPushJob(job.ID, now.Add(delay))
	}
	if err != nil {
		log.Printf("[notify] drop NodeScope push %s job %d: %v", job.Operation, job.ID, err)
	}
	return st.CompletePushJob(job.ID)
}
