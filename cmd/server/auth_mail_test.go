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
