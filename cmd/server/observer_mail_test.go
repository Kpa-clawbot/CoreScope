package main

import (
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestRenderPreBlock(t *testing.T) {
	a, _ := newTestAuthService(t)
	m := a.render("a@example.org", "A", "x", mailContent{subject: "S", greeting: "Hi",
		pre: []string{"a = \"<b>&</b>\"\nb = 1"}})
	if !strings.Contains(m.HTML, "<pre>a = &#34;&lt;b&gt;&amp;&lt;/b&gt;&#34;\nb = 1</pre>") {
		t.Fatalf("html = %s", m.HTML)
	}
	if !strings.Contains(m.Text, "a = \"<b>&</b>\"\nb = 1\n") {
		t.Fatalf("text = %q", m.Text)
	}
}

func TestObserverCredentialsMailBlocks(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 1883, transport: "websockets", tls: false}
	m := a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "p<w>")
	if m.Tag != "observer-credentials" {
		t.Fatalf("tag = %q", m.Tag)
	}
	want := `[[broker]]
name = "mqtt.example.org"
enabled = true
server = "mqtt.example.org"
port = 1883
transport = "websockets"
keepalive = 60
qos = 0
retain = true
[broker.tls]
enabled = false
[broker.auth]
method = "password"
username = "obs_abc"
password = "p<w>"`
	for _, w := range []string{want, "set mqtt1.preset custom\nset mqtt1.server mqtt.example.org\nset mqtt1.port 1883\nset mqtt1.username obs_abc\nset mqtt1.password p<w>\nset mqtt.iata <your IATA code>",
		"Host: mqtt.example.org", "Port: 1883", "TLS: no", "Username: obs_abc", "Password: p<w>"} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("text lacks %q:\n%s", w, m.Text)
		}
	}
	if strings.Contains(m.HTML, "p<w>") || !strings.Contains(m.HTML, "p&lt;w&gt;") {
		t.Fatal("html does not escape the password")
	}
	i1, i2, i3 := strings.Index(m.Text, "[[broker]]"), strings.Index(m.Text, "set mqtt1.preset"), strings.Index(m.Text, "Host: ")
	if !(i1 < i2 && i2 < i3) {
		t.Fatalf("block order %d %d %d", i1, i2, i3)
	}
}
