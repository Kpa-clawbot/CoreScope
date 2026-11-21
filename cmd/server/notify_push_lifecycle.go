package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	errNodeScopeNoEnrollment      = errors.New("NodeScope has no active enrollment")
	errNodeScopeManualEnrollment  = errors.New("NodeScope enrollment is configured on the server")
	errNodeScopeDisconnectPending = errors.New("NodeScope disconnect is pending")
	errNodeScopeRelayRevoke       = errors.New("could not revoke NodeScope enrollment at the relay")
)

type nodeScopeDeliveryPhase uint32

const (
	nodeScopeDeliveryActive nodeScopeDeliveryPhase = iota
	nodeScopeDeliveryPaused
	nodeScopeDeliveryDisconnecting
	nodeScopeDeliveryRevoked
)

// Lifecycle transitions take the write lock; queue commits and individual
// relay operations take the read lock. An epoch also invalidates work that
// began before the most recent transition, including ticks spanning a pause.
type nodeScopeDeliveryState struct {
	mu    sync.RWMutex
	epoch atomic.Uint64
	phase atomic.Uint32
}

type nodeScopeLifecycleRecord struct {
	PublicOrigin string                 `json:"public_origin"`
	RelayOrigin  string                 `json:"relay_origin"`
	AnalyzerID   string                 `json:"analyzer_id"`
	Phase        nodeScopeDeliveryPhase `json:"phase"`
}

func (a *authService) nodeScopeDeliveryPaused() bool {
	return nodeScopeDeliveryPhase(a.nodeScopeDelivery.phase.Load()) != nodeScopeDeliveryActive
}

func (a *authService) nodeScopeDisconnectPending() bool {
	return nodeScopeDeliveryPhase(a.nodeScopeDelivery.phase.Load()) >= nodeScopeDeliveryDisconnecting
}

func (a *authService) nodeScopeRelayRevoked() bool {
	return nodeScopeDeliveryPhase(a.nodeScopeDelivery.phase.Load()) == nodeScopeDeliveryRevoked
}

func (a *authService) nodeScopeLifecyclePath() string {
	return a.set.dbPath + ".nodescope-lifecycle.json"
}

func (a *authService) nodeScopeLifecycleRecord(settings notifySettings, phase nodeScopeDeliveryPhase) nodeScopeLifecycleRecord {
	publicOrigin := nodeScopePublicOrigin(a.set.baseURL)
	if publicOrigin == "" {
		publicOrigin = strings.ToLower(a.set.baseURL.Scheme + "://" + a.set.baseURL.Host)
	}
	return nodeScopeLifecycleRecord{
		PublicOrigin: publicOrigin,
		RelayOrigin:  settings.relayOrigin, AnalyzerID: settings.relayAnalyzerID,
		Phase: phase,
	}
}

func (a *authService) restoreNodeScopeLifecycle() {
	settings := a.nodeScopeRelaySettings()
	if settings.relay == nil {
		return
	}
	record, err := readNodeScopeLifecycle(a.nodeScopeLifecyclePath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		// Fail closed for delivery without disrupting the other destinations.
		log.Printf("[notify] load NodeScope delivery state: %v; delivery paused", err)
		a.nodeScopeDelivery.phase.Store(uint32(nodeScopeDeliveryPaused))
		return
	}
	if record != a.nodeScopeLifecycleRecord(settings, record.Phase) {
		return
	}
	a.nodeScopeDelivery.phase.Store(uint32(record.Phase))
}

func readNodeScopeLifecycle(path string) (nodeScopeLifecycleRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nodeScopeLifecycleRecord{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nodeScopeLifecycleRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nodeScopeLifecycleRecord{}, errors.New("NodeScope lifecycle file must be a private regular file")
	}
	const maxBytes = 8192
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nodeScopeLifecycleRecord{}, err
	}
	var record nodeScopeLifecycleRecord
	if len(data) > maxBytes || json.Unmarshal(data, &record) != nil ||
		record.Phase < nodeScopeDeliveryPaused || record.Phase > nodeScopeDeliveryRevoked ||
		record.PublicOrigin == "" || record.RelayOrigin == "" || record.AnalyzerID == "" {
		return nodeScopeLifecycleRecord{}, errors.New("invalid NodeScope lifecycle file")
	}
	return record, nil
}

func (a *authService) setNodeScopePhaseLocked(settings notifySettings, phase nodeScopeDeliveryPhase) error {
	if phase == nodeScopeDeliveryActive {
		if err := removeNodeScopeFile(a.nodeScopeLifecyclePath()); err != nil {
			return err
		}
	} else if err := writePrivateNodeScopeJSON(a.nodeScopeLifecyclePath(), a.nodeScopeLifecycleRecord(settings, phase)); err != nil {
		return err
	}
	a.nodeScopeDelivery.phase.Store(uint32(phase))
	a.nodeScopeDelivery.epoch.Add(1)
	return nil
}

func removeNodeScopeFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// setNodeScopeDeliveryPaused is serialized with other admin mutations.
// Resume clears any remaining deliveries before allowing dispatch again.
func (a *authService) setNodeScopeDeliveryPaused(paused bool) error {
	a.nodeScopeDelivery.mu.Lock()
	defer a.nodeScopeDelivery.mu.Unlock()
	settings := a.nodeScopeRelaySettings()
	if settings.relay == nil {
		return errNodeScopeNoEnrollment
	}
	if a.nodeScopeDisconnectPending() {
		return errNodeScopeDisconnectPending
	}
	if paused && !a.nodeScopeDeliveryPaused() {
		if err := a.setNodeScopePhaseLocked(settings, nodeScopeDeliveryPaused); err != nil {
			return fmt.Errorf("save NodeScope pause: %w", err)
		}
	} else if !a.nodeScopeDeliveryPaused() {
		return nil
	}
	if err := a.st.DropPushDeliveries(settings.relayAnalyzerID); err != nil {
		return fmt.Errorf("clear queued NodeScope deliveries: %w", err)
	}
	if !paused {
		return a.setNodeScopePhaseLocked(settings, nodeScopeDeliveryActive)
	}
	return nil
}

// Disconnect is serialized with other admin mutations. A durable pending
// phase prevents delivery after an uncertain revoke response or local failure.
func (a *authService) disconnectNodeScopeEnrollment(ctx context.Context) error {
	if a.set.notify.relay != nil {
		return errNodeScopeManualEnrollment
	}
	settings := a.nodeScopeRelaySettings()
	if settings.relay == nil {
		return errNodeScopeNoEnrollment
	}
	a.nodeScopeDelivery.mu.Lock()
	if !a.nodeScopeDisconnectPending() {
		if err := a.setNodeScopePhaseLocked(settings, nodeScopeDeliveryDisconnecting); err != nil {
			a.nodeScopeDelivery.mu.Unlock()
			return fmt.Errorf("save pending NodeScope disconnect: %w", err)
		}
	}
	a.nodeScopeDelivery.mu.Unlock()

	if !a.nodeScopeRelayRevoked() {
		if err := settings.relay.RevokeAnalyzer(ctx, settings.relayCredential); err != nil {
			return fmt.Errorf("%w: %w", errNodeScopeRelayRevoke, err)
		}
	}

	a.nodeScopeDelivery.mu.Lock()
	defer a.nodeScopeDelivery.mu.Unlock()
	if !a.nodeScopeRelayRevoked() {
		if err := a.setNodeScopePhaseLocked(settings, nodeScopeDeliveryRevoked); err != nil {
			// The durable pending phase still prevents delivery on restart.
			a.nodeScopeDelivery.phase.Store(uint32(nodeScopeDeliveryRevoked))
			return fmt.Errorf("save confirmed NodeScope revocation: %w", err)
		}
	}
	if err := pruneUnconfiguredPushState(a.st, ""); err != nil {
		return err
	}
	if err := removeNodeScopeFile(nodeScopePendingPath(a.set.dbPath, settings.relayOrigin)); err != nil {
		return fmt.Errorf("remove pending NodeScope credential: %w", err)
	}
	if err := removeNodeScopeFile(a.nodeScopeEnrollmentPath()); err != nil {
		return fmt.Errorf("remove NodeScope credential: %w", err)
	}
	a.nodeScopeRelay.Store(nil)
	if err := a.setNodeScopePhaseLocked(settings, nodeScopeDeliveryActive); err != nil {
		// The marker can only apply to the former enrollment, whose
		// credential is gone. A new analyzer ID will ignore it.
		a.nodeScopeDelivery.phase.Store(uint32(nodeScopeDeliveryActive))
		a.nodeScopeDelivery.epoch.Add(1)
		log.Printf("[notify] remove retired NodeScope delivery state: %v", err)
	}
	return nil
}
