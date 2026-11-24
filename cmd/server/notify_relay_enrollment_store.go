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

type relayEnrollment struct {
	PublicOrigin     string   `json:"public_origin"`
	RelayOrigin      string   `json:"relay_origin"`
	AnalyzerID       string   `json:"analyzer_id"`
	SenderCredential string   `json:"sender_credential"`
	Platforms        []string `json:"platforms,omitempty"` // empty in files from before relay presets: iOS and Android
}

func (a *authService) relaySettings() notifySettings {
	if active := a.enrolledRelay.Load(); active != nil {
		return *active
	}
	return a.set.notify
}

func (a *authService) relayEnrollmentPath() string {
	return a.set.dbPath + ".push-relay.json"
}

func (a *authService) configuredRelayEnrollment(record relayEnrollment) (*notifySettings, error) {
	if !notifyPushIDPattern.MatchString(record.AnalyzerID) || !notifyLinkProofPattern.MatchString(record.SenderCredential) {
		return nil, errors.New("invalid push relay enrollment")
	}
	settings := a.set.notify
	platforms := record.Platforms
	if len(platforms) == 0 {
		platforms = defaultRelayPlatforms
	}
	cfg := &NotifyRelayConfig{Origin: record.RelayOrigin, AnalyzerID: record.AnalyzerID, Platforms: platforms}
	if err := settings.configureRelay(cfg, func(string) string { return record.SenderCredential }); err != nil {
		return nil, err
	}
	return &settings, nil
}

// activateRelayEnrollment is called only by the serialized admin enrollment handler.
func (a *authService) activateRelayEnrollment(origin string, platforms []string, analyzerID, senderCredential string) error {
	if !a.set.notify.enabled || a.relaySettings().relay != nil {
		return errors.New("push relay enrollment is not possible in this configuration")
	}
	publicOrigin := relayPublicOrigin(a.set.baseURL)
	if publicOrigin == "" {
		return errors.New("push relay enrollment requires a public HTTPS analyzer origin")
	}
	record := relayEnrollment{
		PublicOrigin: publicOrigin, RelayOrigin: origin,
		AnalyzerID: analyzerID, SenderCredential: senderCredential, Platforms: platforms,
	}
	settings, err := a.configuredRelayEnrollment(record)
	if err != nil {
		return err
	}
	if err := writePrivateRelayJSON(a.relayEnrollmentPath(), record); err != nil {
		return fmt.Errorf("save push relay enrollment: %w", err)
	}
	a.relayDelivery.mu.Lock()
	a.enrolledRelay.Store(settings)
	a.relayDelivery.epoch.Add(1)
	a.relayDelivery.mu.Unlock()
	return nil
}

// Restore the configured enrollment even when notification delivery is
// paused, and remove push state left by other analyzers.
func (a *authService) restoreAndPrunePushState(notifications *NotificationsConfig) {
	var keepAnalyzerID string
	if notifications != nil && notifications.PushRelay != nil && notifications.PushRelay.AnalyzerID != "" {
		keepAnalyzerID = notifications.PushRelay.AnalyzerID
	} else {
		var err error
		keepAnalyzerID, err = a.restoreRelayEnrollment()
		if err != nil {
			// A temporary sidecar read failure is not evidence that the enrollment went away.
			log.Printf("[notify] load push relay enrollment: %v; will retry on next start", err)
			return
		}
	}
	if keepAnalyzerID == "" {
		// No enrollment is not evidence that the links are stale: a config
		// slip or a moved enrollment file would otherwise unlink every
		// device. Delivery stays off; Disconnect is the way to remove them.
		return
	}
	if err := pruneUnconfiguredPushState(a.st, keepAnalyzerID); err != nil {
		log.Printf("[notify] push state cleanup failed: %v; will retry on next start", err)
	}
}

// restoreRelayEnrollment runs before the workers and only without a manual enrollment.
func (a *authService) restoreRelayEnrollment() (string, error) {
	path := a.relayEnrollmentPath()
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
		return "", errors.New("push relay enrollment file must be a private regular file")
	}
	const maxEnrollmentBytes = 8192
	raw, err := io.ReadAll(io.LimitReader(file, maxEnrollmentBytes+1))
	if err != nil {
		return "", err
	}
	var record relayEnrollment
	if len(raw) > maxEnrollmentBytes || json.Unmarshal(raw, &record) != nil {
		return "", errors.New("invalid push relay enrollment file")
	}
	if record.PublicOrigin == "" || record.PublicOrigin != relayPublicOrigin(a.set.baseURL) {
		return "", nil
	}
	settings, err := a.configuredRelayEnrollment(record)
	if err != nil {
		return "", err
	}
	if a.set.notify.enabled {
		a.enrolledRelay.Store(settings)
	}
	return record.AnalyzerID, nil
}

func writePrivateRelayJSON(path string, record any) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".push-relay-*")
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
