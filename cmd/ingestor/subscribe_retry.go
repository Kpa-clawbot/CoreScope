package main

import (
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// subscribeTimeout bounds a single SUBSCRIBE round-trip inside OnConnect.
const subscribeTimeout = 10 * time.Second

// subscribeRetryInitial / subscribeRetryMax bound the backoff between
// retries of a rejected SUBSCRIBE. Variables so tests can shorten them.
var (
	subscribeRetryInitial = 2 * time.Second
	subscribeRetryMax     = 60 * time.Second
)

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
func subscribeOnce(c mqtt.Client, topic string) error {
	tok := c.Subscribe(topic, 0, nil)
	if !tok.WaitTimeout(subscribeTimeout) {
		return fmt.Errorf("timeout after %s", subscribeTimeout)
	}
	if err := tok.Error(); err != nil {
		return err
	}
	if st, ok := tok.(subackResulter); ok {
		if rc, found := st.Result()[topic]; found && rc >= 0x80 {
			return fmt.Errorf("broker rejected subscription (SUBACK 0x%02x)", rc)
		}
	}
	return nil
}

// subscribeWithRetry subscribes to topic and, if that fails, keeps retrying
// in the background with exponential backoff for as long as the client stays
// connected. If the connection drops, the retry loop ends; the next
// OnConnect starts a fresh attempt.
func subscribeWithRetry(c mqtt.Client, tag, topic string) {
	err := subscribeOnce(c, topic)
	if err == nil {
		log.Printf("MQTT [%s] subscribed to %s", tag, topic)
		return
	}
	log.Printf("MQTT [%s] subscribe error for %s: %v — retrying in background", tag, topic, err)
	go func() {
		delay := subscribeRetryInitial
		for attempt := 2; ; attempt++ {
			time.Sleep(delay)
			if !c.IsConnected() {
				log.Printf("MQTT [%s] connection lost while retrying subscribe to %s; OnConnect will retry", tag, topic)
				return
			}
			if err := subscribeOnce(c, topic); err != nil {
				log.Printf("MQTT [%s] subscribe attempt #%d for %s failed: %v", tag, attempt, topic, err)
				delay *= 2
				if delay > subscribeRetryMax {
					delay = subscribeRetryMax
				}
				continue
			}
			log.Printf("MQTT [%s] subscribed to %s (attempt #%d)", tag, topic, attempt)
			return
		}
	}()
}
