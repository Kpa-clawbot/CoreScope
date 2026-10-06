// Package mailer sends CoreScope's account mails and reads back delivery
// events. Brevo is the default provider; Fake is the in-memory test double.
package mailer

import (
	"context"
	"time"
)

// Message is one outgoing mail. HTML and Text carry the same content.
type Message struct {
	To      string
	ToName  string
	Subject string
	HTML    string
	Text    string
	Tag     string // provider tag, e.g. "activate"
}

// Canonical delivery event names stored by CoreScope, independent of provider.
const (
	EventSent         = "sent"
	EventDelivered    = "delivered"
	EventOpened       = "opened"
	EventClicked      = "clicked"
	EventSoftBounce   = "soft_bounce"
	EventHardBounce   = "hard_bounce"
	EventInvalidEmail = "invalid_email"
	EventDeferred     = "deferred"
	EventSpam         = "spam"
	EventBlocked      = "blocked"
	EventError        = "error"
	EventUnsubscribed = "unsubscribed"
)

// Event is one delivery event for a sent message.
type Event struct {
	MessageID string
	Email     string
	Event     string // canonical name
	Reason    string
	At        time.Time
}

// Mailer sends mail and can report delivery events for a message id.
type Mailer interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
	Events(ctx context.Context, messageID string) ([]Event, error)
}

// IsUndeliverable reports events after which an address should be flagged.
func IsUndeliverable(event string) bool {
	return event == EventHardBounce || event == EventInvalidEmail
}
