package mailer

import (
	"context"
	"fmt"
	"sync"
)

// Fake records sent messages in memory. Safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	sent    []Message
	ids     []string
	sendErr error
	events  map[string][]Event
}

func (f *Fake) Send(_ context.Context, m Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	id := fmt.Sprintf("<fake-%d@corescope.test>", len(f.sent)+1)
	f.sent = append(f.sent, m)
	f.ids = append(f.ids, id)
	return id, nil
}

func (f *Fake) Events(_ context.Context, messageID string) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Event(nil), f.events[messageID]...), nil
}

// SetSendErr makes subsequent Sends fail with err (nil restores success).
func (f *Fake) SetSendErr(err error) {
	f.mu.Lock()
	f.sendErr = err
	f.mu.Unlock()
}

// SetEvents sets what Events returns for messageID.
func (f *Fake) SetEvents(messageID string, evs []Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string][]Event{}
	}
	f.events[messageID] = evs
}

// Sent returns a copy of all successfully sent messages.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Last returns the most recent message and its id.
func (f *Fake) Last() (Message, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return Message{}, "", false
	}
	return f.sent[len(f.sent)-1], f.ids[len(f.ids)-1], true
}
