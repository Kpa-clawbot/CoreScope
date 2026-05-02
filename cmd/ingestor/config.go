package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/meshcore-analyzer/geofilter"
)

// MQTTSource represents a single MQTT broker connection.
type MQTTSource struct {
	Name               string   `json:"name"`
	Broker             string   `json:"broker"`
	Username           string   `json:"username,omitempty"`
	Password           string   `json:"password,omitempty"`
	RejectUnauthorized *bool    `json:"rejectUnauthorized,omitempty"`
	Topics             []string `json:"topics"`
	IATAFilter         []string `json:"iataFilter,omitempty"`
}

// MQTTLegacy is the old single-broker config format.
type MQTTLegacy struct {
	Broker string `json:"broker"`
	Topic  string `json:"topic"`
}

// Config holds the ingestor configuration, compatible with the Node.js config.json format.
type Config struct {
	DBPath          string            `json:"dbPath"`
	MQTT            *MQTTLegacy       `json:"mqtt,omitempty"`
	MQTTSources     []MQTTSource      `json:"mqttSources,omitempty"`
	LogLevel        string            `json:"logLevel,omitempty"`
	ChannelKeysPath string            `json:"channelKeysPath,omitempty"`
	ChannelKeys     map[string]string `json:"channelKeys,omitempty"`
	HashChannels    []string          `json:"hashChannels,omitempty"`
	Retention       *RetentionConfig  `json:"retention,omitempty"`
	Metrics         *MetricsConfig    `json:"metrics,omitempty"`
	GeoFilter            *GeoFilterConfig  `json:"geo_filter,omitempty"`
	ValidateSignatures   *bool             `json:"validateSignatures,omitempty"`
	DB                   *DBConfig         `json:"db,omitempty"`

	// MQTTConnectTimeoutSeconds overrides the MQTT connect timeout (default 10s).
	MQTTConnectTimeoutSeconds int `json:"mqttConnectTimeoutSeconds,omitempty"`

	// ObserverBlacklist is a list of observer public keys to drop at ingest.
	// Messages from blacklisted observers are silently discarded — no DB writes,
	// no UpsertObserver, no observations, no metrics.
	ObserverBlacklist []string `json:"observerBlacklist,omitempty"`

	// obsBlacklistSetCached is the lazily-built lowercase set for O(1) lookups.
	obsBlacklistSetCached map[string]bool
	obsBlacklistOnce      sync.Once
}

// GeoFilterConfig is an alias for the shared geofilter.Config type.
type GeoFilterConfig = geofilter.Config

// RetentionConfig controls how long stale nodes are kept before being moved to inactive_nodes.
type RetentionConfig struct {
	NodeDays      int `json:"nodeDays"`
	ObserverDays  int `json:"observerDays"`
	MetricsDays   int `json:"metricsDays"`
}

// MetricsConfig controls observer metrics collection.
type MetricsConfig struct {
	SampleIntervalSec int `json:"sampleIntervalSec"`
}

// DBConfig controls SQLite vacuum and maintenance behavior (#919).
type DBConfig struct {
	VacuumOnStartup        bool `json:"vacuumOnStartup"`        // one-time full VACUUM on startup if auto_vacuum is not INCREMENTAL
	IncrementalVacuumPages int  `json:"incrementalVacuumPages"` // pages returned to OS per reaper cycle (default 1024)
}

// IncrementalVacuumPages returns the configured pages per vacuum or 1024 default.
func (c *Config) IncrementalVacuumPages() int {
	if c.DB != nil && c.DB.IncrementalVacuumPages > 0 {
		return c.DB.IncrementalVacuumPages
	}
	return 1024
}

// ShouldValidateSignatures returns true (default) unless explicitly disabled.
func (c *Config) ShouldValidateSignatures() bool {
	if c.ValidateSignatures != nil {
		return *c.ValidateSignatures
	}
	return true
}

// MetricsSampleInterval returns the configured sample interval or 300s default.
func (c *Config) MetricsSampleInterval() int {
	if c.Metrics != nil && c.Metrics.SampleIntervalSec > 0 {
		return c.Metrics.SampleIntervalSec
	}
	return 300
}

// MetricsRetentionDays returns configured metrics retention or 30 days default.
func (c *Config) MetricsRetentionDays() int {
	if c.Retention != nil && c.Retention.MetricsDays > 0 {
		return c.Retention.MetricsDays
	}
	return 30
}

// MQTTConnectTimeoutOrDefault returns the configured MQTT connect timeout in
// seconds, or 10 if not set (matching the paho default used historically).
func (c *Config) MQTTConnectTimeoutOrDefault() int {
	if c.MQTTConnectTimeoutSeconds > 0 {
		return c.MQTTConnectTimeoutSeconds
	}
	return 10
}

// NodeDaysOrDefault returns the configured retention.nodeDays or 7 if not set.
func (c *Config) NodeDaysOrDefault() int {
	if c.Retention != nil && c.Retention.NodeDays > 0 {
		return c.Retention.NodeDays
	}
	return 7
}

// ObserverDaysOrDefault returns the configured retention.observerDays or 14 if not set.
// A value of -1 means observers are never removed.
func (c *Config) ObserverDaysOrDefault() int {
	if c.Retention != nil && c.Retention.ObserverDays != 0 {
		return c.Retention.ObserverDays
	}
	return 14
}

// IsObserverBlacklisted returns true if the given observer ID is in the observerBlacklist.
func (c *Config) IsObserverBlacklisted(id string) bool {
	if c == nil || len(c.ObserverBlacklist) == 0 {
		return false
	}
	c.obsBlacklistOnce.Do(func() {
		m := make(map[string]bool, len(c.ObserverBlacklist))
		for _, pk := range c.ObserverBlacklist {
			trimmed := strings.ToLower(strings.TrimSpace(pk))
			if trimmed != "" {
				m[trimmed] = true
			}
		}
		c.obsBlacklistSetCached = m
	})
	return c.obsBlacklistSetCached[strings.ToLower(strings.TrimSpace(id))]
}

// LoadConfig reads configuration from a JSON file, with env var overrides.
// If the config file does not exist, sensible defaults are used (zero-config startup).
func LoadConfig(path string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
		// Config file doesn't exist — use defaults (zero-config mode)
		log.Printf("config file %s not found, using sensible defaults", path)
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	// Env var overrides
	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("MQTT_BROKER"); v != "" {
		// Single broker from env — create a source
		topic := os.Getenv("MQTT_TOPIC")
		if topic == "" {
			topic = "meshcore/#"
		}
		cfg.MQTTSources = []MQTTSource{{
			Name:   "env",
			Broker: v,
			Topics: []string{topic},
		}}
	}

	// Default DB path
	if cfg.DBPath == "" {
		cfg.DBPath = "data/meshcore.db"
	}

	// Normalize: convert legacy single mqtt config to mqttSources
	if len(cfg.MQTTSources) == 0 && cfg.MQTT != nil && cfg.MQTT.Broker != "" {
		cfg.MQTTSources = []MQTTSource{{
			Name:   "default",
			Broker: cfg.MQTT.Broker,
			Topics: []string{cfg.MQTT.Topic, "meshcore/#"},
		}}
	}

	// Default MQTT source: connect to localhost broker when no sources configured
	if len(cfg.MQTTSources) == 0 {
		cfg.MQTTSources = []MQTTSource{{
			Name:   "local",
			Broker: "mqtt://localhost:1883",
			Topics: []string{"meshcore/#"},
		}}
		log.Printf("no MQTT sources configured, defaulting to mqtt://localhost:1883")
	}

	return &cfg, nil
}

// ResolvedSources returns the final list of MQTT sources to connect to.
func (c *Config) ResolvedSources() []MQTTSource {
	for i := range c.MQTTSources {
		// paho uses tcp:// and ssl:// not mqtt:// and mqtts://
		b := c.MQTTSources[i].Broker
		if strings.HasPrefix(b, "mqtt://") {
			c.MQTTSources[i].Broker = "tcp://" + b[7:]
		} else if strings.HasPrefix(b, "mqtts://") {
			c.MQTTSources[i].Broker = "ssl://" + b[8:]
		}
	}
	return c.MQTTSources
}
