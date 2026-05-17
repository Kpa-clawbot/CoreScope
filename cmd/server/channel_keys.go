package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// deriveHashtagChannelKey derives an AES-128 key from a channel name.
// SHA-256(channelName) → first 32 hex chars (16 bytes). Matches ingestor + Node.js.
func deriveHashtagChannelKey(channelName string) string {
	h := sha256.Sum256([]byte(channelName))
	return hex.EncodeToString(h[:16])
}

// loadServerChannelKeys loads channel PSK keys using the same merge order as the
// ingestor: rainbow file (lowest) → derived from hashChannels → explicit channelKeys
// (highest). configDir is the directory containing config.json.
func loadServerChannelKeys(cfg *Config, configDir string) map[string]string {
	keys := make(map[string]string)

	// 1. Rainbow table file (lowest priority)
	keysPath := os.Getenv("CHANNEL_KEYS_PATH")
	if keysPath == "" {
		keysPath = cfg.ChannelKeysPath
	}
	if keysPath == "" {
		keysPath = filepath.Join(configDir, "channel-rainbow.json")
	}

	if data, err := os.ReadFile(keysPath); err == nil {
		var fileKeys map[string]string
		if err := json.Unmarshal(data, &fileKeys); err == nil {
			for k, v := range fileKeys {
				keys[k] = v
			}
			log.Printf("[channel-keys] loaded %d keys from %s", len(fileKeys), keysPath)
		} else {
			log.Printf("[channel-keys] warning: failed to parse %s: %v", keysPath, err)
		}
	}

	// 2. Derived from hashChannels (middle priority)
	for _, raw := range cfg.HashChannels {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		channelName := trimmed
		if !strings.HasPrefix(channelName, "#") {
			channelName = "#" + channelName
		}
		if _, exists := cfg.ChannelKeys[channelName]; exists {
			continue
		}
		keys[channelName] = deriveHashtagChannelKey(channelName)
	}

	// 3. Explicit config keys (highest priority)
	for k, v := range cfg.ChannelKeys {
		keys[k] = v
	}

	return keys
}

// publicChannelKeys returns the subset of channel keys that are safe to serve
// to UNAUTHENTICATED callers: hashtag-channel keys only, and always the
// publicly-derivable SHA-256 derivation — never an operator-configured value.
//
// Rationale (#security-audit): hashtag-channel PSKs are SHA-256(channelName)
// and therefore not secret — anyone who knows the channel name can derive
// them, so serving them keeps hashtag QR-share working. Explicitly-configured
// keys for private (non-#) channels are real secrets and are omitted here; if
// an operator configured a *different* key for a #-channel we still only
// expose the derivable value, so a private override is never leaked.
func publicChannelKeys(allKeys map[string]string) map[string]string {
	pub := make(map[string]string)
	for name := range allKeys {
		if !strings.HasPrefix(name, "#") {
			continue // private channel — never served unauthenticated
		}
		// Recompute the derivation rather than echoing the stored value,
		// so a configured override for a #-channel is not exposed.
		pub[name] = deriveHashtagChannelKey(name)
	}
	return pub
}
