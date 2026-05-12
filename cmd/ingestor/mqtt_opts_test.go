package main

import (
	"testing"
	"time"
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
