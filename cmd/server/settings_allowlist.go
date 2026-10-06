package main

import "strings"

// settingsDocMaxBytes caps one user's serialized settings document.
const settingsDocMaxBytes = 256 << 10

// settingsKey is one localStorage key the settings sync accepts.
type settingsKey struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`         // settingsKindSet or settingsKindScalar
	ID   string `json:"id,omitempty"` // set only: the item field that identifies an item; "" = the item itself
}

const (
	settingsKindSet    = "set"    // a JSON array, merged per item
	settingsKindScalar = "scalar" // any string, merged as a whole
)

func scalarSettings(keys ...string) []settingsKey {
	out := make([]settingsKey, len(keys))
	for i, k := range keys {
		out[i] = settingsKey{Key: k, Kind: settingsKindScalar}
	}
	return out
}

// settingsAllowlist is the single source of the synced keys
// (docs/specs/2026-10-06-user-settings-sync-design.md). GET
// /api/account/settings hands it to the browser. Device-specific state
// (panel and column sizes, collapsed panels, map positions) stays out on
// purpose. A var so tests can extend it.
var settingsAllowlist = append([]settingsKey{
	{Key: "meshcore-my-nodes", Kind: settingsKindSet, ID: "pubkey"},
	{Key: "meshcore-favorites", Kind: settingsKindSet},
	{Key: "corescope_saved_filters_v1", Kind: settingsKindSet, ID: "name"},
}, scalarSettings(
	// customizer, theme, units
	"cs-theme-overrides", "meshcore-theme", "meshcore-cb-preset", "mc-dark-tile-provider", "mc-light-tile-provider",
	"meshcore-distance-unit", "meshcore-heatmap-opacity", "meshcore-live-heatmap-opacity",
	// packets
	"meshcore-observer-filter", "meshcore-type-filter", "meshcore-time-window", "meshcore-hex-hashes",
	"meshcore-full-names", "meshcore-obs-sort", "meshcore-packets-sort",
	// tables
	"meshcore-nodes-sort", "meshcore-observers-sort", "meshcore-scope-audit-sort", "meshcore-channel-sort",
	// nodes
	"meshcore-nodes-last-heard", "meshcore-nodes-status-filter", "meshcore-nodes-silent-for",
	// region and area
	"meshcore-area-filter", "meshcore-region-filter", "mc-region-show-all-nodes", "meshcore-hide-1byte-hops",
	"channels-show-encrypted",
	// map
	"meshcore-map-clustering", "meshcore-map-heatmap", "meshcore-map-hash-labels", "meshcore-map-multibyte-overlay",
	"meshcore-map-scope-overlay", "meshcore-map-status-filter", "meshcore-map-scope-filter", "meshcore-map-byte-filter",
	"meshcore-map-region-filter", "meshcore-map-geo-filter", "meshcore-top-routes-axis", "meshcore-top-routes-n",
	// analytics
	"subpath-hide-collisions", "meshcore-repeater-scatter-x", "meshcore-repeater-scatter-y", "ng-min-score",
	// home
	"meshcore-user-level",
	// live
	"meshcore-live-heatmap", "live-ghost-hops", "live-realistic-propagation", "live-favorites-only",
	"live-multibyte-only", "live-matrix-mode", "live-matrix-rain", "meshcore-color-packets-by-hash",
	"live-node-filter", "live-vcr-speed", "live-audio-voice", "live-audio-enabled", "live-audio-bpm",
	"live-audio-volume",
)...)

// settingsDenied reports keys that must never leave the browser: channel
// keys, labels and decrypted-message caches (#725), the channel colour map
// (keyed by channel name, and a hashtag channel's name is its key) and the
// admin API key. It is checked before the allowlist, so adding one there
// cannot sync it.
func settingsDenied(key string) bool {
	return key == "meshcore-api-key" || key == "live-channel-colors" || strings.HasPrefix(key, "corescope_channel_")
}

// syncedSettingsKeys is the allowlist minus denied keys: what GET returns
// and what PUT accepts.
func syncedSettingsKeys() []settingsKey {
	out := make([]settingsKey, 0, len(settingsAllowlist))
	for _, k := range settingsAllowlist {
		if !settingsDenied(k.Key) {
			out = append(out, k)
		}
	}
	return out
}
