package mailer

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRecordsAndFails(t *testing.T) {
	f := &Fake{}
	id, err := f.Send(context.Background(), Message{To: "a@example.org", Subject: "Hi", Text: "link"})
	if err != nil || id == "" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	m, gotID, ok := f.Last()
	if !ok || m.To != "a@example.org" || gotID != id {
		t.Fatalf("Last = %+v %q %v", m, gotID, ok)
	}
	f.SetEvents(id, []Event{{MessageID: id, Event: EventDelivered}})
	evs, _ := f.Events(context.Background(), id)
	if len(evs) != 1 || evs[0].Event != EventDelivered {
		t.Fatalf("Events = %+v", evs)
	}
	boom := errors.New("down")
	f.SetSendErr(boom)
	if _, err := f.Send(context.Background(), Message{To: "b@example.org"}); !errors.Is(err, boom) {
		t.Fatalf("Send with error = %v", err)
	}
	if len(f.Sent()) != 1 {
		t.Fatalf("failed send was recorded: %d", len(f.Sent()))
	}
	if !IsUndeliverable(EventHardBounce) || !IsUndeliverable(EventInvalidEmail) || IsUndeliverable(EventSoftBounce) {
		t.Fatal("IsUndeliverable classification wrong")
	}
}
