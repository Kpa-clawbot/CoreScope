package main

import (
	"bytes"
	"log"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/mailer"
)

func TestDeliveryStatusFalseOnlyForSMTP(t *testing.T) {
	for provider, want := range map[string]bool{"brevo": true, "postal": true, "fake": true, "smtp": false} {
		a := &authService{set: &userMgmtSettings{provider: provider}}
		if got := a.deliveryStatus(); got != want {
			t.Errorf("%s: deliveryStatus = %v, want %v", provider, got, want)
		}
	}
}

// closedPort returns a local port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func captureLogBuf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestStartupSMTPCheckLogsAndContinues(t *testing.T) {
	const secret = "s3cret-smtp-pw"
	port := closedPort(t)
	buf := captureLogBuf(t)

	// the check itself, synchronous
	srv := &Server{cfg: &Config{}}
	srv.checkSMTPAtStartup(mailer.NewSMTP(mailer.SMTPConfig{
		Host: "127.0.0.1", Port: port, TLS: "none", User: "u", Password: secret,
		FromEmail: "noreply@example.org", FromName: "CoreScope",
	}))
	if out := buf.String(); !strings.Contains(out, "[mail] SMTP check failed:") || strings.Contains(out, secret) {
		t.Fatalf("log = %q", out)
	}

	// initUserManagement still returns nil and the check goroutine is tracked
	buf.Reset()
	srv = &Server{cfg: &Config{UserManagement: &UserManagementConfig{
		Enabled: true, PublicBaseURL: testBase,
		Mail: UserMailConfig{Provider: "smtp", SMTPHost: "127.0.0.1", SMTPPort: port, SMTPTLS: "none",
			SMTPUser: "u", SMTPPassword: secret, FromEmail: "noreply@example.org"},
	}}}
	if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil {
		t.Fatal(err)
	}
	srv.closeUserManagement() // waits for the check goroutine
	if out := buf.String(); !strings.Contains(out, "[mail] SMTP check failed:") || strings.Contains(out, secret) {
		t.Fatalf("log after init = %q", out)
	}
}
