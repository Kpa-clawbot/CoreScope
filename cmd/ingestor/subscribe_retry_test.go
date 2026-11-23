package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// fakeSubToken is a SUBSCRIBE token carrying a SUBACK result.
type fakeSubToken struct {
	result   map[string]byte
	err      error
	timedOut bool
}

func (t *fakeSubToken) Wait() bool                     { return !t.timedOut }
func (t *fakeSubToken) WaitTimeout(time.Duration) bool { return !t.timedOut }
func (t *fakeSubToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	if !t.timedOut {
		close(ch)
	}
	return ch
}
func (t *fakeSubToken) Error() error            { return t.err }
func (t *fakeSubToken) Result() map[string]byte { return t.result }

// fakeSubClient answers SUBSCRIBE with a scripted sequence of SUBACK codes;
// the last code repeats once the script is exhausted. Like paho, it keys the
// result by the topic with any "$share/<group>/" or "$queue/" prefix
// stripped.
type fakeSubClient struct {
	mu       sync.Mutex
	codes    []byte
	calls    int
	open     bool
	err      error
	timedOut bool
	noResult bool
}

func stripSharePrefix(topic string) string {
	if strings.HasPrefix(topic, "$share/") {
		return strings.Join(strings.Split(topic, "/")[2:], "/")
	}
	return strings.TrimPrefix(topic, "$queue/")
}

func (c *fakeSubClient) Subscribe(topic string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	if i >= len(c.codes) {
		i = len(c.codes) - 1
	}
	c.calls++
	res := map[string]byte{stripSharePrefix(topic): c.codes[i]}
	if c.noResult {
		res = map[string]byte{}
	}
	return &fakeSubToken{result: res, err: c.err, timedOut: c.timedOut}
}

// IsConnectionOpen mirrors paho: false while reconnecting. There is
// deliberately no IsConnected here — with AutoReconnect paho reports true
// during an outage, so the retry loop must not rely on it.
func (c *fakeSubClient) IsConnectionOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open
}

func (c *fakeSubClient) setOpen(v bool) {
	c.mu.Lock()
	c.open = v
	c.mu.Unlock()
}

func (c *fakeSubClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSubscribeOnce_Granted(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, open: true}
	if err := subscribeOnce(c, "meshcore/#"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSubscribeOnce_RejectedSubackIsError(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x80}, open: true}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected error for SUBACK 0x80, got nil")
	}
}

func TestSubscribeOnce_SharedTopicRejectedIsError(t *testing.T) {
	for _, topic := range []string{"$share/corescope/meshcore/#", "$queue/meshcore/#"} {
		c := &fakeSubClient{codes: []byte{0x80}, open: true}
		if err := subscribeOnce(c, topic); err == nil {
			t.Fatalf("%s: expected error for SUBACK 0x80, got nil", topic)
		}
	}
}

func TestSubscribeOnce_EmptyResultIsError(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, open: true, noResult: true}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected error for SUBACK without return code")
	}
}

func TestSubscribeOnce_TokenError(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, open: true, err: errors.New("boom")}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected token error to be returned")
	}
}

func TestSubscribeOnce_Timeout(t *testing.T) {
	c := &fakeSubClient{codes: []byte{0x00}, open: true, timedOut: true}
	if err := subscribeOnce(c, "meshcore/#"); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestNextSubscribeDelay_Capped(t *testing.T) {
	d := subscribeRetryInitial
	for i := 0; i < 20; i++ {
		d = nextSubscribeDelay(d)
		if d > subscribeRetryMax {
			t.Fatalf("delay %s exceeds cap %s", d, subscribeRetryMax)
		}
	}
	if d != subscribeRetryMax {
		t.Fatalf("expected delay to settle at %s, got %s", subscribeRetryMax, d)
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

func waitLoopsDone(t *testing.T, m *subscriptionManager) {
	t.Helper()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("retry loops still running (%d active)", m.active.Load())
	}
}

func TestSubscriptionManager_RecoversAfterRejection(t *testing.T) {
	withFastRetry(t)
	// Broker rejects twice (authz not loaded yet), then grants.
	c := &fakeSubClient{codes: []byte{0x80, 0x80, 0x00}, open: true}
	m := &subscriptionManager{}
	m.start(c, "test", []string{"meshcore/#"})
	waitLoopsDone(t, m)
	if n := c.callCount(); n != 3 {
		t.Fatalf("expected retries to stop after success at 3 calls, got %d", n)
	}
}

func TestSubscriptionManager_NoRetryOnSuccess(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x00}, open: true}
	m := &subscriptionManager{}
	m.start(c, "test", []string{"meshcore/#"})
	time.Sleep(50 * time.Millisecond)
	if n := c.callCount(); n != 1 {
		t.Fatalf("expected exactly 1 subscribe, got %d", n)
	}
}

// With AutoReconnect, paho's IsConnected stays true while reconnecting; the
// loop must end once the connection itself is no longer open.
func TestSubscriptionManager_StopsWhenConnectionNotOpen(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x80}, open: true}
	m := &subscriptionManager{}
	m.start(c, "test", []string{"meshcore/#"})
	waitForSub(t, func() bool { return c.callCount() >= 2 })
	c.setOpen(false)
	waitLoopsDone(t, m)
}

func TestSubscriptionManager_StopEndsLoop(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x80}, open: true}
	m := &subscriptionManager{}
	m.start(c, "test", []string{"meshcore/#"})
	waitForSub(t, func() bool { return c.callCount() >= 2 })
	m.stop() // connection-lost handler
	waitLoopsDone(t, m)
	n := c.callCount()
	time.Sleep(50 * time.Millisecond)
	if c.callCount() != n {
		t.Fatal("retry loop kept subscribing after stop")
	}
}

// A permanent ACL deny with a flapping connection must not accumulate one
// retry loop per reconnect: each OnConnect supersedes the previous loops.
func TestSubscriptionManager_ReconnectSupersedesOldLoops(t *testing.T) {
	withFastRetry(t)
	c := &fakeSubClient{codes: []byte{0x80}, open: true}
	m := &subscriptionManager{}
	for i := 0; i < 20; i++ {
		m.start(c, "test", []string{"meshcore/#"})
	}
	waitForSub(t, func() bool { return m.active.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := m.active.Load(); n != 1 {
		t.Fatalf("expected 1 live retry loop after 20 reconnects, got %d", n)
	}
	m.stop()
	waitLoopsDone(t, m)
}
