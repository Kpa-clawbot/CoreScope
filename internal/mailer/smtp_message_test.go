package mailer

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"mime/quotedprintable"
	"regexp"
	"strings"
	"testing"
	"time"
)

var testDate = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func TestBuildSMTPMessageExact(t *testing.T) {
	m := Message{
		To: "a@example.org", ToName: "Ann", Subject: "Hi",
		Text: "line1\nline2", HTML: "<p>x</p>",
		Headers: map[string]string{"List-Unsubscribe": "<https://x/u>"},
	}
	got, err := buildSMTPMessage("noreply@example.org", "CoreScope", m, "<abc@example.org>", testDate, "b1")
	if err != nil {
		t.Fatal(err)
	}
	want := "From: \"CoreScope\" <noreply@example.org>\r\n" +
		"To: \"Ann\" <a@example.org>\r\n" +
		"Subject: Hi\r\n" +
		"Date: Mon, 23 Nov 2026 10:00:00 +0000\r\n" +
		"Message-ID: <abc@example.org>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"List-Unsubscribe: <https://x/u>\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"line1\r\nline2\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"<p>x</p>\r\n" +
		"--b1--\r\n"
	if string(got) != want {
		t.Fatalf("message mismatch\n got: %q\nwant: %q", got, want)
	}
}

func partBody(t *testing.T, msg []byte, boundary, ctype string) string {
	t.Helper()
	s := string(msg)
	hdr := "Content-Type: " + ctype + "; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"
	start := strings.Index(s, hdr)
	if start < 0 {
		t.Fatal("no " + ctype + " part")
	}
	start += len(hdr)
	end := strings.Index(s[start:], "\r\n--"+boundary)
	if end < 0 {
		t.Fatal("no part end")
	}
	b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(s[start : start+end])))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildSMTPMessageEncodesNonASCII(t *testing.T) {
	body := "Réseau ✓ café\r\nsecond"
	m := Message{To: "a@example.org", Subject: "Activez ✓", Text: body, HTML: "<p>é</p>"}
	got, err := buildSMTPMessage("noreply@example.org", "Réseau Mesh", m, "<abc@example.org>", testDate, "b1")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if want := "Subject: " + mime.QEncoding.Encode("UTF-8", "Activez ✓") + "\r\n"; !strings.Contains(s, want) {
		t.Fatalf("subject not Q-encoded, want %q in %q", want, s)
	}
	if want := "From: =?UTF-8?q?R=C3=A9seau_Mesh?= <noreply@example.org>\r\n"; !strings.Contains(s, want) {
		t.Fatalf("From line, want %q in %q", want, s)
	}
	if g := partBody(t, got, "b1", "text/plain"); g != body {
		t.Fatalf("body mismatch: %q vs %q", g, body)
	}
	if g := partBody(t, got, "b1", "text/html"); g != "<p>é</p>" {
		t.Fatalf("html mismatch: %q", g)
	}
}

func TestBuildSMTPMessageRejectsCRLF(t *testing.T) {
	type args struct {
		fromEmail, fromName, messageID, boundary string
		m                                        Message
	}
	base := func() args {
		return args{"noreply@example.org", "N", "<abc@example.org>", "b1",
			Message{To: "a@example.org", ToName: "Ann", Subject: "Hi", Text: "t", HTML: "h"}}
	}
	cases := map[string]func(a *args){
		"subject cr":   func(a *args) { a.m.Subject = "a\rb" },
		"subject lf":   func(a *args) { a.m.Subject = "a\nBcc: x" },
		"to":           func(a *args) { a.m.To = "a@example.org\r\nBcc: x@y" },
		"toname":       func(a *args) { a.m.ToName = "A\nB" },
		"fromname":     func(a *args) { a.fromName = "N\r\nX: y" },
		"fromemail":    func(a *args) { a.fromEmail = "n@example.org\r\nX: y" },
		"messageid":    func(a *args) { a.messageID = "<a@b>\r\nX: y" },
		"boundary":     func(a *args) { a.boundary = "b\r\nX: y" },
		"header key":   func(a *args) { a.m.Headers = map[string]string{"X-A\nB": "v"} },
		"header value": func(a *args) { a.m.Headers = map[string]string{"X-A": "v\r\nBcc: x"} },
	}
	for name, mut := range cases {
		a := base()
		mut(&a)
		got, err := buildSMTPMessage(a.fromEmail, a.fromName, a.m, a.messageID, testDate, a.boundary)
		if err == nil || !strings.Contains(err.Error(), "header") {
			t.Errorf("%s: want error containing %q, got %v", name, "header", err)
		}
		if got != nil {
			t.Errorf("%s: want nil bytes, got %q", name, got)
		}
	}
}

func TestBuildSMTPMessageRejectsBadHeaderNames(t *testing.T) {
	for _, k := range []string{"", "X A", "X:A", "from", "TO", "Subject", "Date", "message-id", "MIME-Version", "Content-Type", "content-transfer-encoding"} {
		m := Message{To: "a@example.org", Subject: "Hi", Text: "t", HTML: "h", Headers: map[string]string{k: "secret-value"}}
		got, err := buildSMTPMessage("noreply@example.org", "N", m, "<abc@example.org>", testDate, "b1")
		if err == nil || !strings.Contains(err.Error(), "header name") {
			t.Errorf("key %q: want header name error, got %v", k, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("key %q: error leaks the value: %v", k, err)
		}
		if got != nil {
			t.Errorf("key %q: want nil bytes", k)
		}
	}
}

func TestBuildSMTPMessageNonASCIIToName(t *testing.T) {
	m := Message{To: "a b@example.org", ToName: "Zoë", Subject: "Hi", Text: "t", HTML: "h"}
	got, err := buildSMTPMessage("noreply@example.org", "N", m, "<abc@example.org>", testDate, "b1")
	if err != nil {
		t.Fatal(err)
	}
	want := "To: =?UTF-8?q?Zo=C3=AB?= <\"a b\"@example.org>\r\n"
	if !strings.Contains(string(got), want) {
		t.Fatalf("want %q in %q", want, got)
	}
}

func TestBuildSMTPMessageLongLinesAndDots(t *testing.T) {
	body := "https://example.org/" + strings.Repeat("a", 1980) + "\r\n.hidden\r\nend"
	m := Message{To: "a@example.org", Subject: "Hi", Text: body, HTML: "<p>x</p>"}
	got, err := buildSMTPMessage("noreply@example.org", "CoreScope", m, "<abc@example.org>", testDate, "b1")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(got))
	for sc.Scan() {
		if len(sc.Text()) > 76 {
			t.Fatalf("line of %d bytes: %q", len(sc.Text()), sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if g := partBody(t, got, "b1", "text/plain"); g != body {
		t.Fatalf("decoded body differs")
	}
}

func TestNewMessageID(t *testing.T) {
	re := regexp.MustCompile(`^<[0-9a-f]{32}@example\.org>$`)
	a, b := newMessageID("noreply@example.org"), newMessageID("noreply@example.org")
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("bad ids %q %q", a, b)
	}
	if a == b {
		t.Fatal("ids equal")
	}
}

func TestNewBoundary(t *testing.T) {
	a, b := newBoundary(), newBoundary()
	if a == "" || a == b {
		t.Fatalf("bad boundaries %q %q", a, b)
	}
}
