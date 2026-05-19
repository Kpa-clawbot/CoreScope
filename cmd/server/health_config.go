package main

type HealthCheckConfig struct {
	TestChannelName       string                `json:"testChannelName"`
	TestChannelSecret     string                `json:"testChannelSecret"`
	SessionTTLSeconds     int                   `json:"sessionTTLSeconds"`
	MaxUsesPerSession     int                   `json:"maxUsesPerSession"`
	ResultRetentionSeconds   int                   `json:"resultRetentionSeconds"`
	ObserverRetentionSeconds int                   `json:"observerRetentionSeconds"`
	RateLimit             HealthRateLimitConfig `json:"rateLimit"`
	Turnstile             HealthTurnstileConfig `json:"turnstile"`
	KnownObservers        []string              `json:"knownObservers"`
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
	if c.SessionTTLSeconds == 0 {
		c.SessionTTLSeconds = 600
	}
	if c.MaxUsesPerSession == 0 {
		c.MaxUsesPerSession = 5
	}
	if c.ResultRetentionSeconds == 0 {
		c.ResultRetentionSeconds = 604800
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
