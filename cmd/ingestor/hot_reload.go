package main

import (
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// hotKeys holds the channel-decryption and region-scope keys behind atomic
// pointers so a SIGHUP can safely swap in freshly-loaded values while MQTT
// message handlers are concurrently reading the current ones. Neither
// loadChannelKeys nor loadRegionKeys re-runs automatically otherwise —
// hashChannels/hashRegions additions in config.json require a full ingestor
// restart to take effect without this.
type hotKeys struct {
	channelKeys atomic.Pointer[map[string]string]
	regionKeys  atomic.Pointer[map[string][]byte]
}

// newHotKeys wraps the initial startup-loaded key maps.
func newHotKeys(channelKeys map[string]string, regionKeys map[string][]byte) *hotKeys {
	hk := &hotKeys{}
	hk.channelKeys.Store(&channelKeys)
	hk.regionKeys.Store(&regionKeys)
	return hk
}

// Channels returns the current channel-decryption key snapshot. Safe to
// call from any goroutine; cheap (one atomic load, no lock).
func (hk *hotKeys) Channels() map[string]string {
	return *hk.channelKeys.Load()
}

// Regions returns the current region-scope key snapshot. Safe to call from
// any goroutine; cheap (one atomic load, no lock).
func (hk *hotKeys) Regions() map[string][]byte {
	return *hk.regionKeys.Load()
}

// reload re-reads configPath from disk and atomically swaps in freshly
// derived channel/region keys. On any error the previous keys are left in
// place — a malformed config.json during a live reload must not blank out
// a working ingestor.
func (hk *hotKeys) reload(configPath string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	ck := loadChannelKeys(cfg, configPath)
	rk := loadRegionKeys(cfg)
	hk.channelKeys.Store(&ck)
	hk.regionKeys.Store(&rk)
	log.Printf("[hot-reload] reloaded %d channel key(s), %d region key(s) from %s", len(ck), len(rk), configPath)
	return nil
}

// startSIGHUPReload spawns a goroutine that reloads hashChannels/hashRegions
// (via hk.reload) every time the process receives SIGHUP — the standard
// Unix "reload config without restart" convention:
//
//	kill -HUP <ingestor-pid>
//
// Returns a stop func that unregisters the signal and lets the goroutine
// exit; safe to defer from main().
func startSIGHUPReload(hk *hotKeys, configPath string) (stop func()) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sighup:
				log.Printf("[hot-reload] SIGHUP received, reloading hashChannels/hashRegions from %s", configPath)
				if err := hk.reload(configPath); err != nil {
					log.Printf("[hot-reload] reload failed, keeping previous keys: %v", err)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sighup)
		close(done)
	}
}
