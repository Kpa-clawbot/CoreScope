package main

import (
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/eclipse/paho.mqtt.golang/packets"
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
func TestBuildMQTTOpts_ClientIDDefaultShape(t *testing.T) {
	cases := []struct {
		name   string
		source MQTTSource
		want   string
	}{
		{"sanitized source name", MQTTSource{Broker: "tcp://broker:1883", Name: "local feed/1"}, `^corescope-local-feed-1-[0-9a-f]{6}$`},
		{"broker host without port", MQTTSource{Broker: "ssl://mqtt.example.com:8883"}, `^corescope-mqtt-example-com-[0-9a-f]{6}$`},
		{"no name and no broker host", MQTTSource{Broker: "tcp://:1883"}, `^corescope-[0-9a-f]{6}$`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildMQTTOpts(tc.source).ClientID
			if !regexp.MustCompile(tc.want).MatchString(got) {
				t.Errorf("ClientID = %q, want match for %s", got, tc.want)
			}
		})
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

// The ID is chosen once in buildMQTTOpts, so every CONNECT the client sends
// must carry it: the first connect, paho's auto-reconnect after the broker
// drops the socket, and the watchdog's force-reconnect on the same client.
func TestBuildMQTTOpts_ClientIDSurvivesReconnects(t *testing.T) {
	const wait = 5 * time.Second
	broker, connects, drop := fakeMQTTBroker(t)

	opts := buildMQTTOpts(MQTTSource{Broker: broker, Name: "fake broker"})
	up := make(chan struct{}, 8)
	opts.SetOnConnectHandler(func(mqtt.Client) {
		select {
		case up <- struct{}{}:
		default:
		}
	})
	client := mqtt.NewClient(opts)
	t.Cleanup(func() { client.Disconnect(250) })

	var ids []string
	awaitConnect := func(step string) {
		t.Helper()
		select {
		case id := <-connects:
			ids = append(ids, id)
		case <-time.After(wait):
			t.Fatalf("%s: no CONNECT within %v (seen %q)", step, wait, ids)
		}
		select {
		case <-up:
		case <-time.After(wait):
			t.Fatalf("%s: client not connected within %v", step, wait)
		}
	}

	if tok := client.Connect(); !tok.WaitTimeout(wait) || tok.Error() != nil {
		t.Fatalf("connect: timed out or failed: %v", tok.Error())
	}
	awaitConnect("first connect")

	drop()
	awaitConnect("auto-reconnect")

	buildForceReconnectFn(client, "fake broker")()
	awaitConnect("watchdog force-reconnect")

	for i, id := range ids {
		if id == "" {
			t.Errorf("CONNECT %d carried an empty ClientID (all: %q)", i+1, ids)
		} else if id != opts.ClientID {
			t.Errorf("CONNECT %d ClientID = %q, want %q (all: %q)", i+1, id, opts.ClientID, ids)
		}
	}
}

// fakeMQTTBroker accepts connections on a loopback port, answers every
// CONNECT with an accepted CONNACK and reports its ClientID on connects. drop
// closes every connection accepted so far, as a broker restart would.
func fakeMQTTBroker(t *testing.T) (broker string, connects <-chan string, drop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ids := make(chan string, 8)
	done := make(chan struct{})
	var mu sync.Mutex
	var conns []net.Conn
	closed := false

	drop = func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
		conns = nil
	}
	t.Cleanup(func() {
		close(done)
		ln.Close()
		mu.Lock()
		closed = true
		mu.Unlock()
		drop()
	})

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				c.Close()
				return
			}
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				for {
					cp, err := packets.ReadPacket(c)
					if err != nil {
						return
					}
					p, ok := cp.(*packets.ConnectPacket)
					if !ok {
						continue
					}
					select {
					case ids <- p.ClientIdentifier:
					case <-done:
						return
					}
					ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
					if ack.Write(c) != nil {
						return
					}
				}
			}()
		}
	}()
	return "tcp://" + ln.Addr().String(), ids, drop
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
