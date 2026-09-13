package main

import (
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestBuildMQTTOpts_ReconnectSettings(t *testing.T) {
	source := MQTTSource{
		Broker: "tcp://localhost:1883",
		Name:   "test",
	}
	opts := buildMQTTOpts(source)

	if opts.MaxReconnectInterval != 30*time.Second {
		t.Errorf("MaxReconnectInterval = %v, want 30s", opts.MaxReconnectInterval)
	}
	if opts.ConnectTimeout != 10*time.Second {
		t.Errorf("ConnectTimeout = %v, want 10s", opts.ConnectTimeout)
	}
	if opts.WriteTimeout != 10*time.Second {
		t.Errorf("WriteTimeout = %v, want 10s", opts.WriteTimeout)
	}
	if !opts.AutoReconnect {
		t.Error("AutoReconnect should be true")
	}
	if !opts.ConnectRetry {
		t.Error("ConnectRetry should be true")
	}
}

func TestBuildMQTTOpts_Credentials(t *testing.T) {
	source := MQTTSource{
		Broker:   "tcp://broker:1883",
		Username: "user1",
		Password: "pass1",
	}
	opts := buildMQTTOpts(source)

	if opts.Username != "user1" {
		t.Errorf("Username = %q, want %q", opts.Username, "user1")
	}
	if opts.Password != "pass1" {
		t.Errorf("Password = %q, want %q", opts.Password, "pass1")
	}
}

// #2013: without SetClientID paho connects with a zero-length ClientID and
// the ingestor's identity depends on what the broker does with that.
func TestBuildMQTTOpts_ClientIDDefaultsToNonEmpty(t *testing.T) {
	opts := buildMQTTOpts(MQTTSource{Broker: "tcp://broker:1883", Name: "local feed/1"})

	if opts.ClientID == "" {
		t.Fatal("ClientID must not be empty when no clientId is configured")
	}
	if !strings.HasPrefix(opts.ClientID, "corescope-local-feed-1-") {
		t.Errorf("ClientID = %q, want prefix %q", opts.ClientID, "corescope-local-feed-1-")
	}
	for _, r := range opts.ClientID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			t.Errorf("ClientID = %q contains %q, want only [0-9A-Za-z-]", opts.ClientID, r)
		}
	}
}

func TestBuildMQTTOpts_ClientIDFallsBackToBrokerHost(t *testing.T) {
	opts := buildMQTTOpts(MQTTSource{Broker: "ssl://mqtt.example.com:8883"})

	if !strings.HasPrefix(opts.ClientID, "corescope-mqtt-example-com-") {
		t.Errorf("ClientID = %q, want prefix %q", opts.ClientID, "corescope-mqtt-example-com-")
	}
}

func TestBuildMQTTOpts_ClientIDUsesConfiguredValue(t *testing.T) {
	opts := buildMQTTOpts(MQTTSource{Broker: "tcp://broker:1883", Name: "local", ClientID: "my-ingestor"})

	if opts.ClientID != "my-ingestor" {
		t.Errorf("ClientID = %q, want %q", opts.ClientID, "my-ingestor")
	}
}

func TestBuildMQTTOpts_ClientIDDiffersBetweenSources(t *testing.T) {
	a := buildMQTTOpts(MQTTSource{Broker: "tcp://broker:1883", Name: "same"})
	b := buildMQTTOpts(MQTTSource{Broker: "tcp://broker:1883", Name: "same"})

	if a.ClientID == b.ClientID {
		t.Errorf("two unconfigured sources got the same ClientID %q", a.ClientID)
	}
}

// The ID is generated once per buildMQTTOpts call, which main makes once per
// source. paho copies the options into the client and reuses them for every
// reconnect and for the watchdog's force-reconnect on the same client, so the
// ID must already be fixed in the options rather than chosen per attempt.
func TestBuildMQTTOpts_ClientIDStableForTheClient(t *testing.T) {
	opts := buildMQTTOpts(MQTTSource{Broker: "tcp://broker:1883", Name: "local"})
	client := mqtt.NewClient(opts)

	r := client.OptionsReader()
	if got := r.ClientID(); got != opts.ClientID || got == "" {
		t.Errorf("client ClientID = %q, want the options' %q", got, opts.ClientID)
	}
}

func TestBuildMQTTOpts_TLS_InsecureSkipVerify(t *testing.T) {
	f := false
	source := MQTTSource{
		Broker:             "ssl://broker:8883",
		RejectUnauthorized: &f,
	}
	opts := buildMQTTOpts(source)

	if opts.TLSConfig == nil {
		t.Fatal("TLSConfig should be set")
	}
	if !opts.TLSConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true when RejectUnauthorized=false")
	}
}

func TestBuildMQTTOpts_TLS_SSL_Prefix(t *testing.T) {
	source := MQTTSource{
		Broker: "ssl://broker:8883",
	}
	opts := buildMQTTOpts(source)

	if opts.TLSConfig == nil {
		t.Fatal("TLSConfig should be set for ssl:// brokers")
	}
	if opts.TLSConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be false by default")
	}
}
