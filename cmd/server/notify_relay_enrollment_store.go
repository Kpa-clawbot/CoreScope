package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

type nodeScopeEnrollment struct {
	PublicOrigin     string `json:"public_origin"`
	RelayOrigin      string `json:"relay_origin"`
	AnalyzerID       string `json:"analyzer_id"`
	SenderCredential string `json:"sender_credential"`
}

func (a *authService) nodeScopeRelaySettings() notifySettings {
	if active := a.nodeScopeRelay.Load(); active != nil {
		return *active
	}
	return a.set.notify
}

func (a *authService) nodeScopeEnrollmentPath() string {
	return a.set.dbPath + ".nodescope-relay.json"
}

func (a *authService) configuredNodeScopeEnrollment(record nodeScopeEnrollment) (*notifySettings, error) {
	if !notifyPushIDPattern.MatchString(record.AnalyzerID) || !notifyLinkProofPattern.MatchString(record.SenderCredential) {
		return nil, errors.New("invalid NodeScope relay enrollment")
	}
	settings := a.set.notify
	cfg := &NotifyRelayConfig{
		Origin: record.RelayOrigin, AnalyzerID: record.AnalyzerID, Platforms: []string{"ios", "android"},
	}
	if err := settings.configureRelay(cfg, func(string) string { return record.SenderCredential }); err != nil {
		return nil, err
	}
	return &settings, nil
}

// activateNodeScopeEnrollment is called only by the serialized admin enrollment handler.
func (a *authService) activateNodeScopeEnrollment(origin, analyzerID, senderCredential string) error {
	if !a.set.notify.enabled || a.nodeScopeRelaySettings().relay != nil {
		return errors.New("NodeScope push cannot be enrolled in this configuration")
	}
	publicOrigin := nodeScopePublicOrigin(a.set.baseURL)
	if publicOrigin == "" {
		return errors.New("NodeScope push requires a public HTTPS analyzer origin")
	}
	record := nodeScopeEnrollment{
		PublicOrigin: publicOrigin, RelayOrigin: origin,
		AnalyzerID: analyzerID, SenderCredential: senderCredential,
	}
	settings, err := a.configuredNodeScopeEnrollment(record)
	if err != nil {
		return err
	}
	if err := writePrivateNodeScopeJSON(a.nodeScopeEnrollmentPath(), record); err != nil {
		return fmt.Errorf("save NodeScope relay enrollment: %w", err)
	}
	a.nodeScopeDelivery.mu.Lock()
	a.nodeScopeRelay.Store(settings)
	a.nodeScopeDelivery.epoch.Add(1)
	a.nodeScopeDelivery.mu.Unlock()
	return nil
}

// Restore the configured enrollment even when notification delivery is paused.
func (a *authService) restoreAndPrunePushState(notifications *NotificationsConfig) {
	var keepAnalyzerID string
	if notifications != nil && notifications.NodeScopePush != nil {
		keepAnalyzerID = notifications.NodeScopePush.AnalyzerID
	} else {
		var err error
		keepAnalyzerID, err = a.restoreNodeScopeEnrollment()
		if err != nil {
			// A temporary sidecar read failure is not evidence that the enrollment went away.
			log.Printf("[notify] load NodeScope enrollment: %v; will retry on next start", err)
			return
		}
	}
	if err := pruneUnconfiguredPushState(a.st, keepAnalyzerID); err != nil {
		log.Printf("[notify] push state cleanup failed: %v; will retry on next start", err)
	}
}

// restoreNodeScopeEnrollment runs before the workers and only if no manual config exists.
func (a *authService) restoreNodeScopeEnrollment() (string, error) {
	path := a.nodeScopeEnrollmentPath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("NodeScope relay enrollment file must be a private regular file")
	}
	const maxEnrollmentBytes = 8192
	raw, err := io.ReadAll(io.LimitReader(file, maxEnrollmentBytes+1))
	if err != nil {
		return "", err
	}
	var record nodeScopeEnrollment
	if len(raw) > maxEnrollmentBytes || json.Unmarshal(raw, &record) != nil {
		return "", errors.New("invalid NodeScope relay enrollment file")
	}
	if record.PublicOrigin == "" || record.PublicOrigin != nodeScopePublicOrigin(a.set.baseURL) {
		return "", nil
	}
	settings, err := a.configuredNodeScopeEnrollment(record)
	if err != nil {
		return "", err
	}
	if a.set.notify.enabled {
		a.nodeScopeRelay.Store(settings)
	}
	return record.AnalyzerID, nil
}

func writePrivateNodeScopeJSON(path string, record any) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".nodescope-relay-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
