package main

import (
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestRenderPreBlock(t *testing.T) {
	a, _ := newTestAuthService(t)
	m := a.render("a@example.org", "A", "x", mailContent{subject: "S", greeting: "Hi",
		pre: []mailBlock{{title: "1. T<i>", note: "Do <this>.", body: "a = \"<b>&</b>\"\nb = 1"}}})
	if !strings.Contains(m.HTML, "<h3>1. T&lt;i&gt;</h3><p>Do &lt;this&gt;.</p><pre>a = &#34;&lt;b&gt;&amp;&lt;/b&gt;&#34;\nb = 1</pre>") {
		t.Fatalf("html = %s", m.HTML)
	}
	if !strings.Contains(m.Text, "=== 1. T<i> ===\nDo <this>.\n\na = \"<b>&</b>\"\nb = 1\n") {
		t.Fatalf("text = %q", m.Text)
	}
}

// Each format starts with a numbered title and one line saying where it goes,
// so a reader can tell where one block ends and which client it is for.
func TestObserverCredentialsMailBlockTitles(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 1883, transport: "tcp"}
	m := a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "pw")
	last := -1
	for _, w := range []string{
		"=== 1. meshcoretomqtt ",
		"=== 2. MeshCore observer firmware (Gessaman or Dutch-MeshCore) ===",
		"=== 3. Home Assistant (meshcore-ha) ===",
		"=== 4. OpenHop (repeater software) ===",
		"=== 5. Any other MQTT client ===",
	} {
		i := strings.Index(m.Text, w)
		if i <= last {
			t.Fatalf("title %q missing or out of order:\n%s", w, m.Text)
		}
		last = i
	}
	if strings.Count(m.HTML, "<h3>") != 5 {
		t.Fatalf("want 5 <h3> titles:\n%s", m.HTML)
	}
}

// OpenHop's broker form, with the field and option labels of its web UI.
// Its "openHop" format (value mqtt) publishes to meshcore/repeater/<name>,
// which a personal login may not use, so the note names the right formats.
func TestObserverCredentialsMailOpenHopBlock(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 1883, transport: "tcp"}
	m := a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "p<w>")
	for _, w := range []string{
		"Add Broker",
		`not "openHop"`,
		"Format: Letsmesh\nTransport: TCP\nHost: mqtt.example.org\nPort: 1883\nUsername: obs_abc\nPassword: p<w>\n" +
			"TLS: off\nUse JWT Auth: off\nRetain Status: on\nPublish Neighbours: on",
	} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("text lacks %q:\n%s", w, m.Text)
		}
	}
	if !strings.Contains(m.HTML, "Password: p&lt;w&gt;\nTLS: off") {
		t.Fatal("html does not escape the OpenHop block")
	}
	i3, i4, i5 := strings.Index(m.Text, "=== 3. "), strings.Index(m.Text, "Format: Letsmesh"), strings.Index(m.Text, "=== 5. ")
	if !(i3 < i4 && i4 < i5) {
		t.Fatalf("block order %d %d %d", i3, i4, i5)
	}

	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 8884, transport: "websockets", tls: true}
	m = a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "pw")
	if !strings.Contains(m.Text, "Transport: Websockets\nHost: mqtt.example.org\nPort: 8884\n") ||
		!strings.Contains(m.Text, "TLS: on\nUse JWT Auth: off\n") {
		t.Fatalf("websockets/TLS variant missing:\n%s", m.Text)
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
	for _, w := range []string{want, "set mqtt4.preset custom\nset mqtt4.server ws://mqtt.example.org\nset mqtt4.port 1883\nset mqtt4.username obs_abc\nset mqtt4.password p<w>\nset mqtt.iata <your IATA code>\nset mqtt.neighbors on",
		"Host: mqtt.example.org", "Port: 1883", "TLS: no", "Username: obs_abc", "Password: p<w>"} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("text lacks %q:\n%s", w, m.Text)
		}
	}
	if strings.Contains(m.HTML, "p<w>") || !strings.Contains(m.HTML, "p&lt;w&gt;") {
		t.Fatal("html does not escape the password")
	}
	i1, i2, i3 := strings.Index(m.Text, "[[broker]]"), strings.Index(m.Text, "set mqtt4.preset"), strings.Index(m.Text, "Host: ")
	if !(i1 < i2 && i2 < i3) {
		t.Fatalf("block order %d %d %d", i1, i2, i3)
	}
}

// The firmware infers the scheme of a bare host from the port alone (8883
// mqtts, 443 wss, else mqtt: MQTTBridge.cpp setupSlot), so the mail always
// names the scheme the configured transport and TLS need.
// Firmware before the Dutch-MeshCore merge 32dd8ad (2026-10-19) does not parse a
// scheme in mqttN.server and builds "<proto>://<host>:<port>" with proto taken
// from the port alone (8883 mqtts, 443 wss, else mqtt). A bare host works on
// both; the scheme is only written when the port would infer the wrong one.
func TestFirmwareServerScheme(t *testing.T) {
	for _, c := range []struct {
		port      int
		transport string
		tls       bool
		want      string
	}{
		{1883, "tcp", false, "mqtt.example.org"},
		{8883, "tcp", true, "mqtt.example.org"},
		{443, "websockets", true, "mqtt.example.org"},
		{8884, "tcp", false, "mqtt.example.org"},
		{8884, "tcp", true, "mqtts://mqtt.example.org"},
		{8884, "websockets", false, "ws://mqtt.example.org"},
		{8884, "websockets", true, "wss://mqtt.example.org"},
		{8883, "tcp", false, "mqtt://mqtt.example.org"},
	} {
		o := observerLoginSettings{host: "mqtt.example.org", port: c.port, transport: c.transport, tls: c.tls}
		if got := firmwareServer(o); got != c.want {
			t.Errorf("port %d %s tls=%v: got %q, want %q", c.port, c.transport, c.tls, got, c.want)
		}
	}
}

// meshcore-ha takes a bare host in "Server" (paho client.connect(server, port),
// mqtt_uploader.py:390) and offers transport tcp or websockets
// (config_flow.py:1200), so the block carries those values as they are.
func TestObserverCredentialsMailHomeAssistantBlock(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 8884, transport: "websockets", tls: true}
	m := a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "p<w>")
	for _, w := range []string{
		"Manage MQTT Brokers",
		"Enabled: on\nServer: mqtt.example.org\nPort: 8884\nTransport: websockets\nUse TLS: on\nUse MeshCore Auth Token: off\nUsername: obs_abc\nPassword: p<w>\nBroker IATA Code: <your IATA code>",
	} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("text lacks %q:\n%s", w, m.Text)
		}
	}
	if !strings.Contains(m.HTML, "Broker IATA Code: &lt;your IATA code&gt;") || strings.Contains(m.HTML, "p<w>") {
		t.Fatal("html does not escape the Home Assistant block")
	}
	i1, i2, i3, i4 := strings.Index(m.Text, "[[broker]]"), strings.Index(m.Text, "set mqtt4.preset"), strings.Index(m.Text, "Manage MQTT Brokers"), strings.Index(m.Text, "Host: ")
	if !(i1 < i2 && i2 < i3 && i3 < i4) {
		t.Fatalf("block order %d %d %d %d", i1, i2, i3, i4)
	}

	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 1883, transport: "tcp", tls: false}
	m = a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "pw")
	if !strings.Contains(m.Text, "Transport: tcp\nUse TLS: off\n") {
		t.Fatalf("tcp/off variant missing:\n%s", m.Text)
	}
}

// Slots 1-2 (Gessaman: Let's Mesh US/EU) and 1-3 (Dutch-MeshCore:
// dutchmeshcore-1/-2, meshcore-analyzer-eu) hold default presets on a fresh
// install, so the commands use slot 4, which is empty on both.
func TestObserverCredentialsMailFirmwareSlot(t *testing.T) {
	a, _ := newTestAuthService(t)
	a.set.observerLogins = observerLoginSettings{host: "mqtt.example.org", port: 1883, transport: "tcp"}
	m := a.observerCredentialsMail(&users.User{Email: "a@example.org", DisplayName: "A"}, "obs_abc", "pw")
	if strings.Contains(m.Text, "mqtt1.") {
		t.Fatalf("mail still uses slot 1:\n%s", m.Text)
	}
	for _, w := range []string{"Gessaman", "Dutch-MeshCore", "get mqtt.status", "set mqtt4.server mqtt.example.org", "set mqtt.neighbors on", "older firmware does not know it"} {
		if !strings.Contains(m.Text, w) {
			t.Fatalf("text lacks %q:\n%s", w, m.Text)
		}
	}
}
