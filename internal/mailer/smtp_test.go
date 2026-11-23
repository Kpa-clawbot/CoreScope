package mailer

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

const testSMTPPassword = "s3cret-pass"

var testSMTPMessage = Message{
	To: "a@example.org", ToName: "Ann", Subject: "Hello",
	Text: "plain body", HTML: "<p>html body</p>",
	Headers: map[string]string{"List-Unsubscribe": "<https://x/u>"},
}

func newTestSMTP(host string, port int, tlsMode, user string, srv *fakeSMTP) *SMTP {
	s := NewSMTP(SMTPConfig{
		Host: host, Port: port, TLS: tlsMode,
		User: user, Password: testSMTPPassword,
		FromEmail: "noreply@example.org", FromName: "CoreScope",
	})
	s.rootCAs = srv.pool
	return s
}

func hasCommand(cmds []string, verb string) bool {
	return indexOf(cmds, verb) >= 0
}

func indexOf(cmds []string, verb string) int {
	for i, c := range cmds {
		if c == verb {
			return i
		}
	}
	return -1
}

// assertDelivered checks the envelope and that DATA equals buildSMTPMessage
// output for the returned id, rebuilt with the Date and boundary the
// message carried.
func assertDelivered(t *testing.T, got fakeReceived, id string, m Message) {
	t.Helper()
	if got.mailFrom != "MAIL FROM:<noreply@example.org>" {
		t.Errorf("MAIL line = %q", got.mailFrom)
	}
	if got.rcptTo != "RCPT TO:<"+m.To+">" {
		t.Errorf("RCPT line = %q", got.rcptTo)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(got.data))
	if err != nil {
		t.Fatalf("parse DATA: %v", err)
	}
	if mid := parsed.Header.Get("Message-ID"); mid != id {
		t.Errorf("Message-ID header = %q, Send returned %q", mid, id)
	}
	date, err := parsed.Header.Date()
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := buildSMTPMessage("noreply@example.org", "CoreScope", m, id, date, params["boundary"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.data, want) {
		t.Errorf("DATA differs from buildSMTPMessage output\ngot:\n%s\nwant:\n%s", got.data, want)
	}
}

func TestSMTPSendStartTLS(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	id, err := s.Send(context.Background(), testSMTPMessage)
	if err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	st, auth := indexOf(got.commands, "STARTTLS"), indexOf(got.commands, "AUTH")
	if st < 0 || auth < 0 || st > auth {
		t.Errorf("want STARTTLS before AUTH, commands %v", got.commands)
	}
	if got.authPlain != "\x00user\x00"+testSMTPPassword {
		t.Errorf("AUTH PLAIN payload = %q", got.authPlain)
	}
	if !strings.HasSuffix(id, "@example.org>") || !strings.HasPrefix(id, "<") {
		t.Errorf("id = %q", id)
	}
	assertDelivered(t, got, id, testSMTPMessage)
}

func TestSMTPSendImplicitTLS(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "implicit"})
	s := newTestSMTP(host, port, "tls", "user", srv)
	id, err := s.Send(context.Background(), testSMTPMessage)
	if err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if hasCommand(got.commands, "STARTTLS") {
		t.Errorf("STARTTLS sent on implicit TLS: %v", got.commands)
	}
	if got.authPlain != "\x00user\x00"+testSMTPPassword {
		t.Errorf("AUTH PLAIN payload = %q", got.authPlain)
	}
	assertDelivered(t, got, id, testSMTPMessage)
}

func TestSMTPSendNoAuthWhenNoUser(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "", srv)
	id, err := s.Send(context.Background(), testSMTPMessage)
	if err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if hasCommand(got.commands, "AUTH") {
		t.Errorf("AUTH sent without a user: %v", got.commands)
	}
	assertDelivered(t, got, id, testSMTPMessage)
}

func TestSMTPSendPlainLocalhost(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "plain"})
	s := newTestSMTP(host, port, "none", "user", srv)
	id, err := s.Send(context.Background(), testSMTPMessage)
	if err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if got.authPlain != "\x00user\x00"+testSMTPPassword {
		t.Errorf("AUTH PLAIN payload = %q", got.authPlain)
	}
	assertDelivered(t, got, id, testSMTPMessage)
}

func TestSMTPStartTLSNotOffered(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: false})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want one naming STARTTLS", err)
	}
	got := srv.received()
	if hasCommand(got.commands, "AUTH") || hasCommand(got.commands, "MAIL") {
		t.Errorf("AUTH or MAIL sent without TLS: %v", got.commands)
	}
}

func TestSMTPAuthFailureHidesPassword(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true, authReply: "535 5.7.8 authentication failed"})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v, want the 535 reply", err)
	}
	if strings.Contains(err.Error(), testSMTPPassword) {
		t.Errorf("error leaks the password: %v", err)
	}
	if hasCommand(srv.received().commands, "MAIL") {
		t.Error("MAIL sent after failed AUTH")
	}
}

func TestSMTPCertificateVerified(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	s.rootCAs = nil
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate error", err)
	}
	if hasCommand(srv.received().commands, "AUTH") {
		t.Error("AUTH sent over an unverified connection")
	}
}

func TestSMTPSendServerStallsHitsDeadline(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true, stallAfter: "DATA"})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	s.timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil {
		t.Fatal("want an error from a stalled server")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Send took %v", d)
	}
}

func TestSMTPSendContextCancel(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Send(ctx, testSMTPMessage); err == nil {
		t.Fatal("want an error for a cancelled context")
	}
	if hasCommand(srv.received().commands, "MAIL") {
		t.Error("MAIL sent with a cancelled context")
	}
}

func TestSMTPCheckSendsNothing(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	for _, verb := range []string{"EHLO", "STARTTLS", "AUTH", "QUIT"} {
		if !hasCommand(got.commands, verb) {
			t.Errorf("missing %s in %v", verb, got.commands)
		}
	}
	if hasCommand(got.commands, "MAIL") || hasCommand(got.commands, "DATA") {
		t.Errorf("Check sent mail: %v", got.commands)
	}
}

func TestSMTPEventsEmpty(t *testing.T) {
	s := NewSMTP(SMTPConfig{Host: "mail.example.org", Port: 587, TLS: "starttls"})
	ev, err := s.Events(context.Background(), "<x@example.org>")
	if ev != nil || err != nil {
		t.Errorf("Events = %v, %v; want nil, nil", ev, err)
	}
}

func TestSMTPDescribe(t *testing.T) {
	s := NewSMTP(SMTPConfig{Host: "mail.example.org", Port: 587, TLS: "starttls", Password: testSMTPPassword})
	if got := s.Describe(); got != "mail.example.org:587 (starttls)" {
		t.Errorf("Describe = %q", got)
	}
}

func TestSMTPRoundTripLongLineAndDot(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	m := testSMTPMessage
	m.Text = "first\n.hidden\n" + strings.Repeat("x", 2000) + "\nend"
	if _, err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(srv.received().data))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(parsed.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	if want := normalizeCRLF(m.Text); string(body) != want {
		t.Errorf("text part = %q, want %q", body, want)
	}
}
