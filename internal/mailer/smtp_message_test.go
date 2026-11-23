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

func textPart(t *testing.T, msg []byte, boundary string) string {
	t.Helper()
	s := string(msg)
	start := strings.Index(s, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	if start < 0 {
		t.Fatal("no text part")
	}
	start += len("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
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
	if !strings.Contains(s, "From: ") || strings.Contains(strings.SplitN(s, "\r\n\r\n", 2)[0], "Réseau") {
		t.Fatalf("From not encoded: %q", s)
	}
	if !strings.Contains(s, mime.QEncoding.Encode("UTF-8", "Réseau Mesh")) {
		t.Fatalf("From name not Q-encoded: %q", s)
	}
	if g := textPart(t, got, "b1"); g != body {
		t.Fatalf("body mismatch: %q vs %q", g, body)
	}
}

func TestBuildSMTPMessageRejectsCRLF(t *testing.T) {
	ok := Message{To: "a@example.org", ToName: "Ann", Subject: "Hi", Text: "t", HTML: "h"}
	cases := map[string]func() (string, Message){
		"subject cr":   func() (string, Message) { m := ok; m.Subject = "a\rb"; return "N", m },
		"subject lf":   func() (string, Message) { m := ok; m.Subject = "a\nBcc: x"; return "N", m },
		"to":           func() (string, Message) { m := ok; m.To = "a@example.org\r\nBcc: x@y"; return "N", m },
		"toname":       func() (string, Message) { m := ok; m.ToName = "A\nB"; return "N", m },
		"fromname":     func() (string, Message) { return "N\r\nX: y", ok },
		"header key":   func() (string, Message) { m := ok; m.Headers = map[string]string{"X-A\nB": "v"}; return "N", m },
		"header value": func() (string, Message) { m := ok; m.Headers = map[string]string{"X-A": "v\r\nBcc: x"}; return "N", m },
	}
	for name, f := range cases {
		fromName, m := f()
		got, err := buildSMTPMessage("noreply@example.org", fromName, m, "<abc@example.org>", testDate, "b1")
		if err == nil || !strings.Contains(err.Error(), "header") {
			t.Errorf("%s: want error containing %q, got %v", name, "header", err)
		}
		if got != nil {
			t.Errorf("%s: want nil bytes, got %q", name, got)
		}
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
	if g := textPart(t, got, "b1"); g != body {
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
