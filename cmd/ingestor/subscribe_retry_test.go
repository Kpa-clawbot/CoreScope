package main

import (
	"errors"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeSubToken is a completed SUBSCRIBE token carrying a SUBACK code.
type fakeSubToken struct {
	topic string
	rc    byte
	err   error
}

func (t *fakeSubToken) Wait() bool                     { return true }
func (t *fakeSubToken) WaitTimeout(time.Duration) bool { return true }
func (t *fakeSubToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (t *fakeSubToken) Error() error            { return t.err }
func (t *fakeSubToken) Result() map[string]byte { return map[string]byte{t.topic: t.rc} }

// fakeSubClient answers SUBSCRIBE with a scripted sequence of SUBACK codes;
// the last code repeats once the script is exhausted.
type fakeSubClient struct {
	mqtt.Client // nil; only the methods below are used
	mu          sync.Mutex
	codes       []byte
	calls       int
	connected   bool
	err         error
}

func (c *fakeSubClient) Subscribe(topic string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	if i >= len(c.codes) {
		i = len(c.codes) - 1
	}
	c.calls++
	return &fakeSubToken{topic: topic, rc: c.codes[i], err: c.err}
}

func (c *fakeSubClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *fakeSubClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSubscribeOnce_Granted(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, connected: true}
	if err := subscribeOnce(c, "meshcore/#"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSubscribeOnce_RejectedSubackIsError(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x80}, connected: true}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected error for SUBACK 0x80, got nil")
	}
}

func TestSubscribeOnce_TokenError(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, connected: true, err: errors.New("boom")}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected token error to be returned")
	}
}

func withFastRetry(t *testing.T) {
	t.Helper()
	oldI, oldM := subscribeRetryInitial, subscribeRetryMax
	subscribeRetryInitial, subscribeRetryMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { subscribeRetryInitial, subscribeRetryMax = oldI, oldM })
}

func waitForSub(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func TestSubscribeWithRetry_RecoversAfterRejection(t *testing.T) {
	withFastRetry(t)
	// Broker rejects twice (authz not loaded yet), then grants.
	c := &fakeSubClient{codes: []byte{0x80, 0x80, 0x00}, connected: true}
	subscribeWithRetry(c, "test", "meshcore/#")
	waitForSub(t, func() bool { return c.callCount() >= 3 })
	time.Sleep(60 * time.Millisecond)
	if n := c.callCount(); n != 3 {
		t.Fatalf("expected retries to stop after success at 3 calls, got %d", n)
	}
}

func TestSubscribeWithRetry_NoRetryOnSuccess(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x00}, connected: true}
	subscribeWithRetry(c, "test", "meshcore/#")
	time.Sleep(50 * time.Millisecond)
	if n := c.callCount(); n != 1 {
		t.Fatalf("expected exactly 1 subscribe, got %d", n)
	}
}

func TestSubscribeWithRetry_StopsWhenDisconnected(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x80}, connected: true}
	subscribeWithRetry(c, "test", "meshcore/#")
	waitForSub(t, func() bool { return c.callCount() >= 2 })
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	n := c.callCount()
	time.Sleep(60 * time.Millisecond)
	if c.callCount() != n {
		t.Fatal("retry loop kept subscribing after disconnect")
	}
}
