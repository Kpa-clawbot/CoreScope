package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #1337: paho client misconfigured — ingestor receives 200× fewer
// messages than mosquitto_sub on the same broker/creds/topics. Root cause
// (hypothesis 1+5): paho defaults — CleanSession=true, empty ClientID
// (auto-random per reconnect), Order=true (handler serialized) — combined
// with the reconnect-every-5min watchdog meant the broker dropped queued
// messages on every reconnect AND the handler couldn't keep up under load.
//
// These tests pin the four paho options that fix the gap:
//   1. CleanSession=false       — broker keeps the subscription state across
//                                  reconnects instead of treating each dial
//                                  as a brand-new session.
//   2. ClientID = persistent    — broker recognizes the returning session.
//                                  Empty ClientID makes paho generate a fresh
//                                  random one on every reconnect, which is
//                                  treated as a new client by the broker.
//   3. KeepAlive  = 30s         — half-open TCP detected at the paho layer
//                                  instead of waiting for OS keepalive.
//   4. Order = false            — handler dispatch is parallel; one slow
//                                  packet does not block all the others.
//
// All four must be set in buildMQTTOpts. This test fails on master.

func TestBuildMQTTOpts_PersistentSession_Issue1337(t *testing.T) {
	source := MQTTSource{
		Broker: "ssl://broker.example:8883",
		Name:   "sjc-test",
	}
	opts := buildMQTTOpts(source)

	if opts.CleanSession {
		t.Error("CleanSession must be false (#1337): broker drops queued msgs across reconnects when true")
	}

	host, _ := os.Hostname()
	if opts.ClientID == "" {
		t.Fatal("ClientID must be set to a persistent value (#1337): empty = paho generates random per reconnect, broker treats every reconnect as new session")
	}
	if !strings.Contains(opts.ClientID, "sjc-test") {
		t.Errorf("ClientID must embed source name for uniqueness across sources, got %q", opts.ClientID)
	}
	if host != "" && !strings.Contains(opts.ClientID, host) {
		t.Errorf("ClientID must embed hostname for uniqueness across deployments, got %q (host=%q)", opts.ClientID, host)
	}

	if opts.KeepAlive != int64((30 * time.Second).Seconds()) {
		t.Errorf("KeepAlive must be 30s (#1337): got %ds — needed so paho detects half-open TCP", opts.KeepAlive)
	}

	if opts.Order {
		t.Error("Order must be false (#1337): default true serializes handler dispatch; a slow packet stalls all others")
	}
}

// Stability: ClientID must be deterministic for a given (hostname, source)
// across two builds. Otherwise reconnect = new session = lost backlog.
func TestBuildMQTTOpts_ClientIDStableAcrossBuilds_Issue1337(t *testing.T) {
	source := MQTTSource{Broker: "ssl://broker.example:8883", Name: "stable-test"}
	a := buildMQTTOpts(source).ClientID
	b := buildMQTTOpts(source).ClientID
	if a == "" {
		t.Fatal("ClientID empty")
	}
	if a != b {
		t.Errorf("ClientID must be stable across buildMQTTOpts calls (#1337): %q vs %q — random = broker drops session on reconnect", a, b)
	}
}

// Distinct sources must NOT share a ClientID — broker disconnects the older
// session whenever a duplicate ClientID connects, causing flapping.
func TestBuildMQTTOpts_ClientIDUniquePerSource_Issue1337(t *testing.T) {
	a := buildMQTTOpts(MQTTSource{Broker: "ssl://a:8883", Name: "alpha"}).ClientID
	b := buildMQTTOpts(MQTTSource{Broker: "ssl://b:8883", Name: "beta"}).ClientID
	if a == b {
		t.Errorf("distinct sources must get distinct ClientIDs (#1337): both got %q — duplicate IDs cause broker to disconnect the older one, infinite flap", a)
	}
}
