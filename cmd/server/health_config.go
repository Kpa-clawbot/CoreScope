package main

type HealthCheckConfig struct {
	TestChannelName      string                `json:"testChannelName"`
	// TestChannelSecret is the hex-encoded channel secret (same value as
	// TEST_CHANNEL_SECRET in the source project). The AES key is the first
	// 16 raw bytes; the channel-hash filter byte is SHA-256(rawBytes)[0].
	TestChannelSecret    string                `json:"testChannelSecret"`
	// CodePrefix is prepended to each generated health-check code, e.g. "MHC"
	// produces codes like "MHC-A3F7Z2". Defaults to "MHC". Set to "" for no
	// prefix (codes will be plain 6-char alphanumeric strings).
	CodePrefix           string                `json:"codePrefix"`
	SessionTTLSeconds    int                   `json:"sessionTTLSeconds"`
	MaxUsesPerSession    int                   `json:"maxUsesPerSession"`
	ResultRetentionSeconds  int               `json:"resultRetentionSeconds"`
	// ObserverActiveWindowSeconds: an observer is "active" if it sent a
	// packet within this window (default 900 s = 15 min).
	ObserverActiveWindowSeconds int            `json:"observerActiveWindowSeconds"`
	// ObserverRetentionSeconds: drop observers from the directory after this
	// idle period. 0 = keep forever (default 14400 s = 4 h).
	ObserverRetentionSeconds    int            `json:"observerRetentionSeconds"`
	RateLimit            HealthRateLimitConfig `json:"rateLimit"`
	Turnstile            HealthTurnstileConfig `json:"turnstile"`
	KnownObservers       []string              `json:"knownObservers"`
}

type HealthRateLimitConfig struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

type HealthTurnstileConfig struct {
	Enabled   bool   `json:"enabled"`
	SiteKey   string `json:"siteKey"`
	SecretKey string `json:"secretKey"`
}

func (c *HealthCheckConfig) applyDefaults() {
	if c.TestChannelName == "" {
		c.TestChannelName = "test"
	}
	if c.SessionTTLSeconds == 0 {
		c.SessionTTLSeconds = 600
	}
	if c.MaxUsesPerSession == 0 {
		c.MaxUsesPerSession = 5
	}
	if c.ResultRetentionSeconds == 0 {
		c.ResultRetentionSeconds = 604800
	}
	if c.ObserverActiveWindowSeconds == 0 {
		c.ObserverActiveWindowSeconds = 900
	}
	if c.ObserverRetentionSeconds == 0 {
		c.ObserverRetentionSeconds = 14400
	}
	if c.RateLimit.WindowSeconds == 0 {
		c.RateLimit.WindowSeconds = 60
	}
	if c.RateLimit.MaxRequests == 0 {
		c.RateLimit.MaxRequests = 5
	}
}
