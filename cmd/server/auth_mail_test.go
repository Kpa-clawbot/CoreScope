package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestRedactAddrs(t *testing.T) {
	cases := map[string]string{
		`brevo 400 invalid_parameter: email "Bob.Smith+x@Mail.Example.org" is not valid`: `brevo 400 invalid_parameter: email "<addr>" is not valid`,
		"to a@b.co and c_d@e-f.example.net failed":                                       "to <addr> and <addr> failed",
		"brevo 503 service unavailable":                                                  "brevo 503 service unavailable",
		"rejected john.smith&co@example.org":                                             "rejected <addr>",
		"rejected jörg.müller@x.de":                                                      "rejected <addr>",
		"rejected bob@bücher.de":                                                         "rejected <addr>",
		"rejected bob+newsletter@example.org":                                            "rejected <addr>",
		"rejected o'neil!#$*/=?^{|}~@example.org, retry":                                 "rejected <addr>, retry",
		"recipient <ann@example.org>; cc (bea@example.org)":                              "recipient <<addr>>; cc (<addr>)",
	}
	for in, want := range cases {
		if got := redactAddrs(errors.New(in)); got != want {
			t.Errorf("redactAddrs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSendMailLogHasNoAddress(t *testing.T) {
	a, fake := newTestAuthService(t)
	fake.SetSendErr(errors.New(`brevo 400: invalid email victim@example.org`))
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	u := &users.User{ID: 7, Email: "victim@example.org", DisplayName: "Vic"}
	if err := a.sendMail(context.Background(), u, "reset", a.resetMail(u, "tok")); err == nil {
		t.Fatal("sendMail returned nil on a send error")
	}
	out := buf.String()
	if strings.Contains(out, "victim@example.org") || !strings.Contains(out, "<addr>") || !strings.Contains(out, "#7") {
		t.Fatalf("log line = %q", out)
	}
}

// Activation needs the account password (handleActivate), so the mail says so.
func TestActivationMailMentionsPassword(t *testing.T) {
	a, _ := newTestAuthService(t)
	u := &users.User{ID: 1, Email: "new@example.org", DisplayName: "New"}
	m := a.activationMail(u, "tok")
	for _, body := range []string{m.Text, m.HTML} {
		if !strings.Contains(body, "enter the password you chose") {
			t.Fatalf("activation mail does not mention the password: %q", body)
		}
	}
}

func TestLogStartupRedactsDatabaseURL(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.databaseURL = "postgresql://private:secret@host/accounts?password=hidden"
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	a.logStartup()
	if !strings.Contains(buf.String(), "PostgreSQL") || strings.Contains(buf.String(), "secret") || strings.Contains(buf.String(), "hidden") {
		t.Fatalf("unsafe startup log: %q", buf.String())
	}
}

func TestRenderLinesAndFooter(t *testing.T) {
	a, _ := newTestAuthService(t)
	m := a.render("x@example.org", "X", "notify", mailContent{subject: "S", greeting: "Hi",
		lines:  []mailLine{{text: "<b>n</b>: offline", url: "https://e.org/#/nodes/ab"}},
		footer: &mailLine{text: "Stop these mails:", url: "https://e.org/#/account/unsubscribe?token=t"}})
	if !strings.Contains(m.HTML, `<ul><li><a href="https://e.org/#/nodes/ab">&lt;b&gt;n&lt;/b&gt;: offline</a></li></ul>`) ||
		!strings.Contains(m.Text, "- <b>n</b>: offline\n  https://e.org/#/nodes/ab\n") ||
		!strings.Contains(m.HTML, `Stop these mails: <a href="https://e.org/#/account/unsubscribe?token=t">`) ||
		!strings.Contains(m.Text, "Stop these mails:\nhttps://e.org/#/account/unsubscribe?token=t\n") ||
		strings.Contains(m.Text, "If that was not you") {
		t.Fatalf("rendered = %q / %q", m.HTML, m.Text)
	}
}
