package mailer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/smtp"
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
	if got.ehlo != "example.org" {
		t.Errorf("EHLO name = %q, want the FromEmail domain", got.ehlo)
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded in the chain", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Send took %v", d)
	}
}

func TestSMTPSendCancelMidSession(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true, stallAfter: "MAIL"})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, err := s.Send(ctx, testSMTPMessage)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Send took %v after cancel", d)
	}
}

func TestSMTPCertificateNameMismatch(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "starttls", offerStartTLS: true, certDNSName: "other.example"})
	s := newTestSMTP(host, port, "starttls", "user", srv)
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil || !strings.Contains(err.Error(), "certificate") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err = %v, want a certificate name error for 127.0.0.1", err)
	}
	if hasCommand(srv.received().commands, "AUTH") {
		t.Error("AUTH sent to a server whose certificate names another host")
	}
}

// The plaintext-AUTH guard is net/smtp's: PlainAuth refuses an unencrypted
// connection unless the server is localhost. Pin that contract.
func TestSMTPPlainAuthRefusesUnencryptedRemote(t *testing.T) {
	if _, _, err := smtp.PlainAuth("", "u", "p", "mail.example.org").Start(&smtp.ServerInfo{Name: "mail.example.org", TLS: false}); err == nil {
		t.Error("PlainAuth allowed AUTH over plaintext to a remote host")
	}
	if _, _, err := smtp.PlainAuth("", "u", "p", "127.0.0.1").Start(&smtp.ServerInfo{Name: "127.0.0.1", TLS: false}); err != nil {
		t.Errorf("PlainAuth refused localhost: %v", err)
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

func TestSMTPNonASCIIRecipientWithoutSMTPUTF8(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "plain"})
	s := newTestSMTP(host, port, "none", "", srv)
	m := testSMTPMessage
	m.To = "jürgen@example.org"
	_, err := s.Send(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "SMTPUTF8") || !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("err = %v, want one naming the recipient and SMTPUTF8", err)
	}
	if strings.Contains(err.Error(), "jürgen") {
		t.Errorf("err carries the address: %v", err)
	}
	if got := srv.received(); hasCommand(got.commands, "MAIL") {
		t.Errorf("MAIL sent: %v", got.commands)
	}
}

func TestSMTPNonASCIISenderWithoutSMTPUTF8(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "plain"})
	s := newTestSMTP(host, port, "none", "", srv)
	s.cfg.FromEmail = "noreply@exämple.org"
	_, err := s.Send(context.Background(), testSMTPMessage)
	if err == nil || !strings.Contains(err.Error(), "SMTPUTF8") || !strings.Contains(err.Error(), "sender") {
		t.Fatalf("err = %v, want one naming the sender and SMTPUTF8", err)
	}
	if got := srv.received(); hasCommand(got.commands, "MAIL") {
		t.Errorf("MAIL sent: %v", got.commands)
	}
}

func TestSMTPNonASCIIRecipientWithSMTPUTF8(t *testing.T) {
	host, port, srv := newFakeSMTP(t, fakeSMTPOpts{mode: "plain", smtpUTF8: true})
	s := newTestSMTP(host, port, "none", "", srv)
	m := testSMTPMessage
	m.To = "jürgen@exämple.org"
	m.ToName = "Jürgen"
	if _, err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := srv.received()
	if got.mailFrom != "MAIL FROM:<noreply@example.org> SMTPUTF8" {
		t.Errorf("MAIL line = %q, want the SMTPUTF8 parameter", got.mailFrom)
	}
	if got.rcptTo != "RCPT TO:<jürgen@exämple.org>" {
		t.Errorf("RCPT line = %q", got.rcptTo)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	to, err := mail.ParseAddress(parsed.Header.Get("To"))
	if err != nil {
		t.Fatalf("To header %q: %v", parsed.Header.Get("To"), err)
	}
	if to.Address != "jürgen@exämple.org" || to.Name != "Jürgen" {
		t.Errorf("To = %+v", to)
	}
}
