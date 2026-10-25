package main

import (
	"strconv"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// observerCredentialsPurpose is the mail_log purpose and the mail tag of the
// credentials mail.
const observerCredentialsPurpose = "observer-credentials"

// observerCredentialsMail carries the login in three formats. It is the only
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
	firmware := "set mqtt1.preset custom\n" +
		"set mqtt1.server " + o.host + "\n" +
		"set mqtt1.port " + port + "\n" +
		"set mqtt1.username " + username + "\n" +
		"set mqtt1.password " + password + "\n" +
		"set mqtt.iata <your IATA code>"
	plain := "Host: " + o.host + "\n" +
		"Port: " + port + "\n" +
		"TLS: " + tlsWord + "\n" +
		"Username: " + username + "\n" +
		"Password: " + password
	return a.render(u.Email, u.DisplayName, observerCredentialsPurpose, mailContent{
		subject:  "Your observer MQTT login",
		greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{
			"Below is your personal MQTT login for " + a.set.baseURL.Host + ", in three formats: the meshcoretomqtt config, the Dutch-MeshCore observer firmware commands, and the plain values.",
			"One login serves all your observers. Each observer is linked to you the first time it publishes with this login. If you lose this mail, ask a new copy from your account page.",
		},
		pre:         []string{meshcoretomqtt, firmware, plain},
		actionLabel: "Your account", actionURL: a.set.baseURL.String() + "/#/account?section=observers",
	})
}
