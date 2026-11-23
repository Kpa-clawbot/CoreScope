package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// subscribeTimeout bounds a single SUBSCRIBE round-trip.
const subscribeTimeout = 10 * time.Second

// subscribeRetryInitial / subscribeRetryMax bound the backoff between
// retries of a rejected SUBSCRIBE. Variables so tests can shorten them.
var (
	subscribeRetryInitial = 2 * time.Second
	subscribeRetryMax     = 60 * time.Second
)

// subscribeClient is the subset of mqtt.Client used here, so tests can
// supply a fake.
type subscribeClient interface {
	Subscribe(topic string, qos byte, callback mqtt.MessageHandler) mqtt.Token
	IsConnectionOpen() bool
}

// subackResulter is implemented by *mqtt.SubscribeToken. Matching on the
// method instead of the concrete type keeps subscribeOnce testable.
type subackResulter interface {
	Result() map[string]byte
}

// subscribeOnce sends one SUBSCRIBE and reports failure, including a
// broker-side rejection. paho does NOT surface a SUBACK failure return code
// (0x80) via token.Error(): the token completes "successfully" and the
// client stays connected with zero subscriptions. That happens e.g. when
// EMQX has just restarted and its authorization sources are not loaded yet
// (authorization.no_match = deny). Without this check the ingestor logged
// "subscribed" and then silently received nothing until restarted.
//
// Every return code in the result is checked rather than looking the topic
// up by name: paho strips "$share/<group>/" and "$queue/" before keying the
// result, so a lookup by the configured topic would miss a rejection. Each
// SUBSCRIBE carries exactly one topic, so all codes belong to it. An empty
// result means no SUBACK code was recorded and counts as failure.
func subscribeOnce(c subscribeClient, topic string) error {
	tok := c.Subscribe(topic, 0, nil)
	if !tok.WaitTimeout(subscribeTimeout) {
		return fmt.Errorf("timeout after %s", subscribeTimeout)
	}
	if err := tok.Error(); err != nil {
		return err
	}
	if st, ok := tok.(subackResulter); ok {
		res := st.Result()
		if len(res) == 0 {
			return fmt.Errorf("SUBACK carried no return code")
		}
		for _, rc := range res {
			if rc >= 0x80 {
				return fmt.Errorf("broker rejected subscription (SUBACK 0x%02x)", rc)
			}
		}
	}
	return nil
}

// nextSubscribeDelay doubles the backoff, capped at subscribeRetryMax.
func nextSubscribeDelay(d time.Duration) time.Duration {
	d *= 2
	if d > subscribeRetryMax {
		d = subscribeRetryMax
	}
	return d
}

// subscriptionManager owns the subscribe attempts of one MQTT source. Each
// OnConnect calls start, which cancels any retry loops from the previous
// connection before launching new ones, so at most one loop per topic is
// ever alive. The connection-lost handler calls stop.
//
// IsConnected() cannot be used to end the loops: with AutoReconnect or
// ConnectRetry set (see buildMQTTOpts), paho reports true while it is
// reconnecting. The loops therefore watch their context (cancelled on
// connection loss and on the next OnConnect) and IsConnectionOpen().
type subscriptionManager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
	active atomic.Int32 // live retry loops, for tests
}

// start subscribes to each topic. The first attempt per topic runs
// synchronously; failed topics are retried in the background with
// exponential backoff until they succeed or the connection ends.
func (m *subscriptionManager) start(c subscribeClient, tag string, topics []string) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = cancel
	m.mu.Unlock()

	for _, topic := range topics {
		err := subscribeOnce(c, topic)
		if err == nil {
			log.Printf("MQTT [%s] subscribed to %s", tag, topic)
			continue
		}
		log.Printf("MQTT [%s] subscribe error for %s: %v — retrying in background", tag, topic, err)
		m.wg.Add(1)
		go func(topic string) {
			defer m.wg.Done()
			m.retry(ctx, c, tag, topic)
		}(topic)
	}
}

// stop cancels all retry loops of the current connection.
func (m *subscriptionManager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *subscriptionManager) retry(ctx context.Context, c subscribeClient, tag, topic string) {
	m.active.Add(1)
	defer m.active.Add(-1)
	delay := subscribeRetryInitial
	for attempt := 2; ; attempt++ {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if !c.IsConnectionOpen() {
			log.Printf("MQTT [%s] connection not open while retrying subscribe to %s; next OnConnect will retry", tag, topic)
			return
		}
		if err := subscribeOnce(c, topic); err != nil {
			log.Printf("MQTT [%s] subscribe attempt #%d for %s failed: %v", tag, attempt, topic, err)
			delay = nextSubscribeDelay(delay)
			continue
		}
		if ctx.Err() != nil {
			// Superseded while the SUBSCRIBE was in flight; the newer
			// connection's start() owns logging.
			return
		}
		log.Printf("MQTT [%s] subscribed to %s (attempt #%d)", tag, topic, attempt)
		return
	}
}
