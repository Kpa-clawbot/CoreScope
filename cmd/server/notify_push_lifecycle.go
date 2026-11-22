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
	errRelayNoEnrollment      = errors.New("the push relay has no active enrollment")
	errRelayManualEnrollment  = errors.New("push relay enrollment is configured on the server")
	errRelayDisconnectPending = errors.New("push relay disconnect is pending")
	errRelayRevoke            = errors.New("could not revoke the enrollment at the relay")
)

type relayDeliveryPhase uint32

const (
	relayDeliveryActive relayDeliveryPhase = iota
	relayDeliveryPaused
	relayDeliveryDisconnecting
	relayDeliveryRevoked
)

// Lifecycle transitions take the write lock; queue commits and individual
// relay operations take the read lock. An epoch also invalidates work that
// began before the most recent transition, including ticks spanning a pause.
type relayDeliveryState struct {
	mu    sync.RWMutex
	epoch atomic.Uint64
	phase atomic.Uint32
}

type relayLifecycleRecord struct {
	PublicOrigin string             `json:"public_origin"`
	RelayOrigin  string             `json:"relay_origin"`
	AnalyzerID   string             `json:"analyzer_id"`
	Phase        relayDeliveryPhase `json:"phase"`
}

func (a *authService) relayDeliveryPaused() bool {
	return relayDeliveryPhase(a.relayDelivery.phase.Load()) != relayDeliveryActive
}

func (a *authService) relayDisconnectPending() bool {
	return relayDeliveryPhase(a.relayDelivery.phase.Load()) >= relayDeliveryDisconnecting
}

func (a *authService) relayRevoked() bool {
	return relayDeliveryPhase(a.relayDelivery.phase.Load()) == relayDeliveryRevoked
}

func (a *authService) relayLifecyclePath() string {
	return a.set.dbPath + ".push-relay-lifecycle.json"
}

func (a *authService) relayLifecycleRecord(settings notifySettings, phase relayDeliveryPhase) relayLifecycleRecord {
	publicOrigin := relayPublicOrigin(a.set.baseURL)
	if publicOrigin == "" {
		publicOrigin = strings.ToLower(a.set.baseURL.Scheme + "://" + a.set.baseURL.Host)
	}
	return relayLifecycleRecord{
		PublicOrigin: publicOrigin,
		RelayOrigin:  settings.relayOrigin, AnalyzerID: settings.relayAnalyzerID,
		Phase: phase,
	}
}

func (a *authService) restoreRelayLifecycle() {
	settings := a.relaySettings()
	if settings.relay == nil {
		return
	}
	record, err := readRelayLifecycle(a.relayLifecyclePath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		// Fail closed for delivery without disrupting the other destinations.
		log.Printf("[notify] load push delivery state: %v; delivery paused", err)
		a.relayDelivery.phase.Store(uint32(relayDeliveryPaused))
		return
	}
	if record != a.relayLifecycleRecord(settings, record.Phase) {
		return
	}
	a.relayDelivery.phase.Store(uint32(record.Phase))
}

func readRelayLifecycle(path string) (relayLifecycleRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return relayLifecycleRecord{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return relayLifecycleRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return relayLifecycleRecord{}, errors.New("push relay lifecycle file must be a private regular file")
	}
	const maxBytes = 8192
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return relayLifecycleRecord{}, err
	}
	var record relayLifecycleRecord
	if len(data) > maxBytes || json.Unmarshal(data, &record) != nil ||
		record.Phase < relayDeliveryPaused || record.Phase > relayDeliveryRevoked ||
		record.PublicOrigin == "" || record.RelayOrigin == "" || record.AnalyzerID == "" {
		return relayLifecycleRecord{}, errors.New("invalid push relay lifecycle file")
	}
	return record, nil
}

func (a *authService) setRelayPhaseLocked(settings notifySettings, phase relayDeliveryPhase) error {
	if phase == relayDeliveryActive {
		if err := removeRelayFile(a.relayLifecyclePath()); err != nil {
			return err
		}
	} else if err := writePrivateRelayJSON(a.relayLifecyclePath(), a.relayLifecycleRecord(settings, phase)); err != nil {
		return err
	}
	a.relayDelivery.phase.Store(uint32(phase))
	a.relayDelivery.epoch.Add(1)
	return nil
}

func removeRelayFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// setRelayDeliveryPaused is serialized with other admin mutations.
// Resume clears any remaining deliveries before allowing dispatch again.
func (a *authService) setRelayDeliveryPaused(paused bool) error {
	a.relayDelivery.mu.Lock()
	defer a.relayDelivery.mu.Unlock()
	settings := a.relaySettings()
	if settings.relay == nil {
		return errRelayNoEnrollment
	}
	if a.relayDisconnectPending() {
		return errRelayDisconnectPending
	}
	if paused && !a.relayDeliveryPaused() {
		if err := a.setRelayPhaseLocked(settings, relayDeliveryPaused); err != nil {
			return fmt.Errorf("save push pause: %w", err)
		}
	} else if !a.relayDeliveryPaused() {
		return nil
	}
	if err := a.st.DropPushDeliveries(settings.relayAnalyzerID); err != nil {
		return fmt.Errorf("clear queued push deliveries: %w", err)
	}
	if !paused {
		return a.setRelayPhaseLocked(settings, relayDeliveryActive)
	}
	return nil
}

// Disconnect is serialized with other admin mutations. A durable pending
// phase prevents delivery after an uncertain revoke response or local failure.
func (a *authService) disconnectRelayEnrollment(ctx context.Context) error {
	if a.set.notify.relay != nil {
		return errRelayManualEnrollment
	}
	settings := a.relaySettings()
	if settings.relay == nil {
		return errRelayNoEnrollment
	}
	a.relayDelivery.mu.Lock()
	if !a.relayDisconnectPending() {
		if err := a.setRelayPhaseLocked(settings, relayDeliveryDisconnecting); err != nil {
			a.relayDelivery.mu.Unlock()
			return fmt.Errorf("save pending push relay disconnect: %w", err)
		}
	}
	a.relayDelivery.mu.Unlock()

	if !a.relayRevoked() {
		if err := settings.relay.RevokeAnalyzer(ctx, settings.relayCredential); err != nil {
			return fmt.Errorf("%w: %w", errRelayRevoke, err)
		}
	}

	a.relayDelivery.mu.Lock()
	defer a.relayDelivery.mu.Unlock()
	if !a.relayRevoked() {
		if err := a.setRelayPhaseLocked(settings, relayDeliveryRevoked); err != nil {
			// The durable pending phase still prevents delivery on restart.
			a.relayDelivery.phase.Store(uint32(relayDeliveryRevoked))
			return fmt.Errorf("save confirmed relay revocation: %w", err)
		}
	}
	if err := pruneUnconfiguredPushState(a.st, ""); err != nil {
		return err
	}
	if err := removeRelayFile(relayPendingPath(a.set.dbPath, settings.relayOrigin)); err != nil {
		return fmt.Errorf("remove pending relay credential: %w", err)
	}
	if err := removeRelayFile(a.relayEnrollmentPath()); err != nil {
		return fmt.Errorf("remove relay credential: %w", err)
	}
	a.enrolledRelay.Store(nil)
	if err := a.setRelayPhaseLocked(settings, relayDeliveryActive); err != nil {
		// The marker can only apply to the former enrollment, whose
		// credential is gone. A new analyzer ID will ignore it.
		a.relayDelivery.phase.Store(uint32(relayDeliveryActive))
		a.relayDelivery.epoch.Add(1)
		log.Printf("[notify] remove retired push delivery state: %v", err)
	}
	return nil
}
