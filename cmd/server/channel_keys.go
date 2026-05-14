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
