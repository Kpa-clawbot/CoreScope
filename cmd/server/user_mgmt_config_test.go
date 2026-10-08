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

func TestResolveChannelProposals(t *testing.T) {
	u := validUM()
	set, err := resolveUserManagement(u, "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.proposals != (proposalSettings{}) {
		t.Fatalf("absent block = %+v; want off", set.proposals)
	}
	u.ChannelProposals = &ChannelProposalsConfig{Enabled: true}
	set, _ = resolveUserManagement(u, "meshcore.db", noEnv)
	if set.proposals != (proposalSettings{enabled: true, maxPending: 100, maxApproved: 128, perUserPerDay: 5}) {
		t.Fatalf("defaults = %+v", set.proposals)
	}
	u.ChannelProposals = &ChannelProposalsConfig{Enabled: true, MaxPending: 3, MaxApproved: 7, PerUserPerDay: 1}
	set, _ = resolveUserManagement(u, "meshcore.db", noEnv)
	if set.proposals != (proposalSettings{enabled: true, maxPending: 3, maxApproved: 7, perUserPerDay: 1}) {
		t.Fatalf("explicit = %+v", set.proposals)
	}
	u.ChannelProposals = &ChannelProposalsConfig{Enabled: true, MaxPending: -1, MaxApproved: -1, PerUserPerDay: -1}
	set, _ = resolveUserManagement(u, "meshcore.db", noEnv)
	if set.proposals != (proposalSettings{enabled: true, maxPending: 100, maxApproved: 128, perUserPerDay: 5}) {
		t.Fatalf("negative values = %+v; want defaults", set.proposals)
	}
	u.ChannelProposals = &ChannelProposalsConfig{Enabled: false, MaxApproved: 7}
	set, _ = resolveUserManagement(u, "meshcore.db", noEnv)
	if set.proposals.enabled {
		t.Fatal("enabled false resolved as on")
	}
}

func TestResolveNotifications(t *testing.T) {
	u := validUM()
	set, err := resolveUserManagement(u, "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.notify != (notifySettings{}) {
		t.Fatalf("absent block = %+v; want off", set.notify)
	}
	defaults := notifySettings{enabled: true, interval: 5 * time.Minute, perUserPerDay: 20, maxMailsPerDay: 100, maxWatchesPerUser: 50}
	u.Notifications = &NotificationsConfig{Enabled: true}
	if set, _ = resolveUserManagement(u, "meshcore.db", noEnv); set.notify != defaults {
		t.Fatalf("defaults = %+v", set.notify)
	}
	u.Notifications = &NotificationsConfig{Enabled: true, IntervalMinutes: 1, PerUserPerDay: 3, MaxMailsPerDay: 9, MaxWatchesPerUser: 2}
	if set, _ = resolveUserManagement(u, "meshcore.db", noEnv); set.notify != (notifySettings{enabled: true, interval: time.Minute, perUserPerDay: 3, maxMailsPerDay: 9, maxWatchesPerUser: 2}) {
		t.Fatalf("explicit = %+v", set.notify)
	}
	u.Notifications = &NotificationsConfig{Enabled: true, IntervalMinutes: -5, PerUserPerDay: -1, MaxWatchesPerUser: -2}
	if set, _ = resolveUserManagement(u, "meshcore.db", noEnv); set.notify != defaults {
		t.Fatalf("zero and negative values = %+v; want defaults", set.notify)
	}
	u.Notifications = &NotificationsConfig{Enabled: false, PerUserPerDay: 3}
	if set, _ = resolveUserManagement(u, "meshcore.db", noEnv); set.notify.enabled {
		t.Fatal("enabled false resolved as on")
	}
}

func TestResolveUsersBackup(t *testing.T) {
	u := validUM()
	meas := filepath.Join("data", "meshcore.db")
	defaults := backupSettings{enabled: true, dir: filepath.Join("data", "backups"), keep: 7}
	set, err := resolveUserManagement(u, meas, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.backup != defaults {
		t.Fatalf("absent block = %+v; want %+v", set.backup, defaults)
	}
	u.DBPath = filepath.Join("accounts", "users.db")
	if set, _ = resolveUserManagement(u, meas, noEnv); set.backup.dir != filepath.Join("accounts", "backups") {
		t.Fatalf("dir follows dbPath: %q", set.backup.dir)
	}
	u.DBPath = ""
	off, on := false, true
	u.Backup = &UsersBackupConfig{Enabled: &off, Dir: "x", Keep: 3}
	if set, _ = resolveUserManagement(u, meas, noEnv); set.backup != (backupSettings{}) {
		t.Fatalf("enabled false = %+v; want off", set.backup)
	}
	u.Backup = &UsersBackupConfig{Dir: " bk ", Keep: 3}
	if set, _ = resolveUserManagement(u, meas, noEnv); set.backup != (backupSettings{enabled: true, dir: "bk", keep: 3}) {
		t.Fatalf("enabled omitted = %+v; want on with dir bk, keep 3", set.backup)
	}
	u.Backup = &UsersBackupConfig{Enabled: &on, Dir: "  ", Keep: -1}
	if set, _ = resolveUserManagement(u, meas, noEnv); set.backup != defaults {
		t.Fatalf("blank dir and negative keep = %+v; want defaults", set.backup)
	}
	u.Backup = &UsersBackupConfig{Keep: 0}
	if set, _ = resolveUserManagement(u, meas, noEnv); set.backup.keep != 7 {
		t.Fatalf("keep 0 = %d; want 7", set.backup.keep)
	}
}
