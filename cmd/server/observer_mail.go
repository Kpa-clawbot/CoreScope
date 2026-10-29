package main

import (
	"strconv"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// observerCredentialsPurpose is the mail_log purpose and the mail tag of the
// credentials mail.
const observerCredentialsPurpose = "observer-credentials"

// observerCredentialsMail carries the login in five formats. It is the only
// place the password leaves the server, and it is never logged: mail_log
// keeps only the purpose and the provider message id.
func (a *authService) observerCredentialsMail(u *users.User, username, password string) mailer.Message {
	o := a.set.observerLogins
	port := strconv.Itoa(o.port)
	tlsOn, tlsWord := "false", "no"
	if o.tls {
		tlsOn, tlsWord = "true", "yes"
	}
	meshcoretomqtt := "[[broker]]\n" +
		"name = \"" + o.host + "\"\n" +
		"enabled = true\n" +
		"server = \"" + o.host + "\"\n" +
		"port = " + port + "\n" +
		"transport = \"" + o.transport + "\"\n" +
		"keepalive = 60\n" +
		"qos = 0\n" +
		"retain = true\n" +
		"[broker.tls]\n" +
		"enabled = " + tlsOn + "\n" +
		"[broker.auth]\n" +
		"method = \"password\"\n" +
		"username = \"" + username + "\"\n" +
		"password = \"" + password + "\""
	firmware := "set mqtt4.preset custom\n" +
		"set mqtt4.server " + firmwareServer(o) + "\n" +
		"set mqtt4.port " + port + "\n" +
		"set mqtt4.username " + username + "\n" +
		"set mqtt4.password " + password + "\n" +
		"set mqtt.iata <your IATA code>\n" +
		"set mqtt.neighbors on"
	tlsSwitch := "off"
	if o.tls {
		tlsSwitch = "on"
	}
	homeAssistant := "Enabled: on\n" +
		"Server: " + o.host + "\n" +
		"Port: " + port + "\n" +
		"Transport: " + o.transport + "\n" +
		"Use TLS: " + tlsSwitch + "\n" +
		"Use MeshCore Auth Token: off\n" +
		"Username: " + username + "\n" +
		"Password: " + password + "\n" +
		"Broker IATA Code: <your IATA code>"
	openHopTransport := "TCP"
	if o.transport == "websockets" {
		openHopTransport = "Websockets"
	}
	openHop := "Format: Letsmesh\n" +
		"Transport: " + openHopTransport + "\n" +
		"Host: " + o.host + "\n" +
		"Port: " + port + "\n" +
		"Username: " + username + "\n" +
		"Password: " + password + "\n" +
		"TLS: " + tlsSwitch + "\n" +
		"Use JWT Auth: off\n" +
		"Retain Status: on\n" +
		"Publish Neighbours: on"
	plain := "Host: " + o.host + "\n" +
		"Port: " + port + "\n" +
		"TLS: " + tlsWord + "\n" +
		"Username: " + username + "\n" +
		"Password: " + password
	return a.render(u.Email, u.DisplayName, observerCredentialsPurpose, mailContent{
		subject:  "Your observer MQTT login",
		greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{
			"Below is your personal MQTT login for " + a.set.baseURL.Host + ", in five formats: the meshcoretomqtt config, the MeshCore observer firmware commands (Gessaman or Dutch-MeshCore), the Home Assistant (meshcore-ha) form fields, the OpenHop broker form, and the plain values.",
			"One login serves all your observers. Each observer is linked to you the first time it publishes with this login. If you lose this mail, ask a new copy from your account page.",
		},
		pre: []mailBlock{
			{title: "1. meshcoretomqtt (uploader on a Raspberry Pi or Linux box)",
				note: "Add this block to your meshcoretomqtt config file, next to any brokers already in it, and restart the service.", body: meshcoretomqtt},
			{title: "2. MeshCore observer firmware (Gessaman or Dutch-MeshCore)",
				note: "Type these commands one by one on the serial console, replace <your IATA code> with your area code, then reboot. They use slot 4, which is empty on a fresh install; if get mqtt.status shows it in use, replace mqtt4 with an empty slot. The last command sends your node's neighbour table and region scopes once a day, which this analyzer reads; older firmware does not know it, skip it there.", body: firmware},
			{title: "3. Home Assistant (meshcore-ha)",
				note: "Settings > Devices & services > MeshCore > Configure > Manage MQTT Brokers > add a broker, then fill in these fields.", body: homeAssistant},
			{title: "4. OpenHop (repeater software)",
				note: "In the OpenHop web UI, Configuration > Add Broker, fill in these fields. Format Meshcoretomqtt works too, but not \"openHop\": that one publishes to a topic this broker refuses. Publish Neighbours sends your node's neighbour table and region scopes, which this analyzer reads.", body: openHop},
			{title: "5. Any other MQTT client",
				note: "The plain values.", body: plain},
		},
		actionLabel: "Your account", actionURL: a.set.baseURL.String() + "/#/account?section=observers",
	})
}

// firmwareServer is the mqttN.server value for the observer firmware.
// For a bare host the firmware picks the scheme from the port alone (8883
// mqtts, 443 wss, anything else mqtt) and appends mqttN.port itself. Firmware
// before the merge 32dd8ad (2026-10-19) does not accept a scheme in the
// server, so the host stays bare unless the port would pick the wrong scheme.
func firmwareServer(o observerLoginSettings) string {
	scheme := "mqtt"
	if o.transport == "websockets" {
		scheme = "ws"
	}
	if o.tls {
		scheme += "s"
	}
	inferred := "mqtt"
	switch o.port {
	case 8883:
		inferred = "mqtts"
	case 443:
		inferred = "wss"
	}
	if scheme == inferred {
		return o.host
	}
	return scheme + "://" + o.host
}
