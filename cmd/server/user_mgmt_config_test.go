package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validUM() *UserManagementConfig {
	return &UserManagementConfig{
		Enabled:       true,
		AdminEmails:   []string{" Boss@Example.org "},
		PublicBaseURL: "https://scope.example.org/",
		Mail:          UserMailConfig{BrevoAPIKey: "xkeysib-abc", FromEmail: "noreply@example.org"},
	}
}

func noEnv(string) string { return "" }

func TestResolveUserManagementDefaults(t *testing.T) {
	set, err := resolveUserManagement(validUM(), filepath.Join("data", "meshcore.db"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.dbPath != filepath.Join("data", "users.db") {
		t.Errorf("dbPath = %q", set.dbPath)
	}
	if !set.adminEmails["boss@example.org"] {
		t.Errorf("adminEmails not normalized: %v", set.adminEmails)
	}
	if set.baseURL.String() != "https://scope.example.org" || !set.secureCookie {
		t.Errorf("baseURL = %q secure=%v", set.baseURL, set.secureCookie)
	}
	if set.sessionTTL != 30*24*time.Hour || set.provider != "brevo" || set.fromName != "CoreScope" {
		t.Errorf("defaults: ttl=%v provider=%q fromName=%q", set.sessionTTL, set.provider, set.fromName)
	}
}

func TestResolveUserManagementEnvWins(t *testing.T) {
	env := map[string]string{"CORESCOPE_BREVO_API_KEY": "from-env", "CORESCOPE_BREVO_WEBHOOK_SECRET": "env-secret-0123456789"}
	set, err := resolveUserManagement(validUM(), "meshcore.db", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if set.brevoAPIKey != "from-env" || set.webhookSecret != "env-secret-0123456789" {
		t.Fatalf("env not applied: %q %q", set.brevoAPIKey, set.webhookSecret)
	}
}

func TestResolveUserManagementErrors(t *testing.T) {
	cases := map[string]func(u *UserManagementConfig){
		"publicBaseUrl":  func(u *UserManagementConfig) { u.PublicBaseURL = "scope.example.org" },
		"fromEmail":      func(u *UserManagementConfig) { u.Mail.FromEmail = "" },
		"Brevo API key":  func(u *UserManagementConfig) { u.Mail.BrevoAPIKey = "" },
		"adminEmails":    func(u *UserManagementConfig) { u.AdminEmails = []string{"not-an-address"} },
		"not supported":  func(u *UserManagementConfig) { u.Mail.Provider = "smtp" },
		"e2etest builds": func(u *UserManagementConfig) { u.Mail.Provider = "fake" },
		"webhookSecret":  func(u *UserManagementConfig) { u.Mail.WebhookSecret = "short" },
	}
	for want, mutate := range cases {
		if want == "e2etest builds" && fakeMailerAllowed {
			continue // the e2etest build accepts the fake provider by design
		}
		u := validUM()
		mutate(u)
		_, err := resolveUserManagement(u, "meshcore.db", noEnv)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestUserManagementEnabled(t *testing.T) {
	var nilCfg *Config
	if nilCfg.UserManagementEnabled() || (&Config{}).UserManagementEnabled() ||
		(&Config{UserManagement: &UserManagementConfig{}}).UserManagementEnabled() {
		t.Fatal("off states reported as enabled")
	}
	if !(&Config{UserManagement: &UserManagementConfig{Enabled: true}}).UserManagementEnabled() {
		t.Fatal("enabled not reported")
	}
}
