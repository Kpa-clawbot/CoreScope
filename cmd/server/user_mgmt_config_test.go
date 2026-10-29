package main

import (
	"bytes"
	"encoding/base64"
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

func validPostalUM() *UserManagementConfig {
	u := validUM()
	u.Mail = UserMailConfig{Provider: "Postal", PostalBaseURL: "https://postal.example.org/",
		PostalAPIKey: "postal-key", FromEmail: "noreply@example.org", WebhookSecret: "AbCd0123456789xyz"}
	return u
}

func TestResolveUserManagementPostal(t *testing.T) {
	set, err := resolveUserManagement(validPostalUM(), "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.provider != "postal" || set.postalBaseURL != "https://postal.example.org" ||
		set.postalAPIKey != "postal-key" || set.webhookSecret != "AbCd0123456789xyz" {
		t.Fatalf("postal settings = %q %q %q %q", set.provider, set.postalBaseURL, set.postalAPIKey, set.webhookSecret)
	}

	env := map[string]string{
		"CORESCOPE_POSTAL_API_KEY": "env-key", "CORESCOPE_POSTAL_WEBHOOK_SECRET": "env-secret-0123456789",
		"CORESCOPE_BREVO_API_KEY": "brevo-env", "CORESCOPE_BREVO_WEBHOOK_SECRET": "brevo-secret-0123456789",
	}
	set, err = resolveUserManagement(validPostalUM(), "meshcore.db", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if set.postalAPIKey != "env-key" || set.webhookSecret != "env-secret-0123456789" {
		t.Fatalf("postal env not applied: %q %q", set.postalAPIKey, set.webhookSecret)
	}

	u := validPostalUM()
	u.Mail.PostalBaseURL = "http://127.0.0.1:5000"
	if set, err := resolveUserManagement(u, "meshcore.db", noEnv); err != nil || set.postalBaseURL != "http://127.0.0.1:5000" {
		t.Fatalf("loopback http: %v", err)
	}
}

func TestResolveUserManagementPostalErrors(t *testing.T) {
	cases := map[string]func(u *UserManagementConfig){
		"Postal API key":          func(u *UserManagementConfig) { u.Mail.PostalAPIKey = "" },
		"postalBaseUrl must be":   func(u *UserManagementConfig) { u.Mail.PostalBaseURL = "" },
		"postalBaseUrl must use":  func(u *UserManagementConfig) { u.Mail.PostalBaseURL = "http://postal.example.org" },
		"postalBaseUrl must not":  func(u *UserManagementConfig) { u.Mail.PostalBaseURL = "https://user:pw@postal.example.org" },
		"postalBaseUrl must not ": func(u *UserManagementConfig) { u.Mail.PostalBaseURL = "https://postal.example.org/?x=1" },
		"letters, digits":         func(u *UserManagementConfig) { u.Mail.WebhookSecret = "has:colon-0123456789" },
		"webhookSecret":           func(u *UserManagementConfig) { u.Mail.WebhookSecret = "short" },
	}
	for want, mutate := range cases {
		u := validPostalUM()
		mutate(u)
		_, err := resolveUserManagement(u, "meshcore.db", noEnv)
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(want)) {
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

func validObserverUM() *UserManagementConfig {
	u := validUM()
	u.ObserverLogins = &ObserverLoginsConfig{
		Enabled:       true,
		HookSecret:    "config-secret-0123456789",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		Broker:        ObserverBrokerConfig{Host: "mqtt.example.org"},
	}
	return u
}

func TestResolveObserverLogins_OffByDefault(t *testing.T) {
	set, err := resolveUserManagement(validUM(), "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if set.observerLogins.enabled {
		t.Fatal("observerLogins enabled without a block")
	}
}

func TestResolveObserverLogins_EnvOverrides(t *testing.T) {
	envKey := bytes.Repeat([]byte{9}, 32)
	env := map[string]string{
		"CORESCOPE_OBSERVER_HOOK_SECRET": "env-hook-secret-0123456789",
		"CORESCOPE_OBSERVER_KEY":         base64.StdEncoding.EncodeToString(envKey),
	}
	set, err := resolveUserManagement(validObserverUM(), "meshcore.db", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	o := set.observerLogins
	if !o.enabled || o.hookSecret != "env-hook-secret-0123456789" || !bytes.Equal(o.key, envKey) {
		t.Fatalf("env not applied: %+v", o)
	}
}

func TestResolveObserverLogins_ConfigValues(t *testing.T) {
	set, err := resolveUserManagement(validObserverUM(), "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	o := set.observerLogins
	if o.hookSecret != "config-secret-0123456789" || !bytes.Equal(o.key, bytes.Repeat([]byte{7}, 32)) || o.host != "mqtt.example.org" {
		t.Fatalf("config not applied: %+v", o)
	}
}

func TestResolveObserverLogins_RefusesMissingKeyOrSecret(t *testing.T) {
	cases := map[string]func(c *ObserverLoginsConfig){
		"observerLogins.encryptionKey": func(c *ObserverLoginsConfig) { c.EncryptionKey = "" },
		"observerLogins.hookSecret":    func(c *ObserverLoginsConfig) { c.HookSecret = "" },
	}
	for want, mut := range cases {
		u := validObserverUM()
		mut(u.ObserverLogins)
		if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	for name, key := range map[string]string{
		"16 bytes":   base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"not base64": "!!!not base64!!!",
	} {
		u := validObserverUM()
		u.ObserverLogins.EncryptionKey = key
		if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), "observerLogins.encryptionKey") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestResolveObserverLogins_Defaults(t *testing.T) {
	set, err := resolveUserManagement(validObserverUM(), "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if o := set.observerLogins; o.port != 1883 || o.transport != "tcp" || o.tls {
		t.Fatalf("defaults: port=%d transport=%q tls=%v", o.port, o.transport, o.tls)
	}
	u := validObserverUM()
	u.ObserverLogins.Broker = ObserverBrokerConfig{Port: 8883, Transport: "websockets", TLS: true, Host: "h"}
	set, err = resolveUserManagement(u, "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if o := set.observerLogins; o.port != 8883 || o.transport != "websockets" || !o.tls {
		t.Fatalf("explicit values lost: %+v", o)
	}
	u = validObserverUM()
	u.ObserverLogins.Broker.Host = ""
	if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), "observerLogins.broker.host") {
		t.Fatalf("empty host: err = %v", err)
	}
}

func TestResolveObserverLogins_DisabledSkipsValidation(t *testing.T) {
	u := validUM()
	u.ObserverLogins = &ObserverLoginsConfig{Enabled: false}
	set, err := resolveUserManagement(u, "meshcore.db", noEnv)
	if err != nil || set.observerLogins.enabled {
		t.Fatalf("err=%v enabled=%v", err, set.observerLogins.enabled)
	}
}

func TestResolveObserverLogins_HookSecretMinimumLength(t *testing.T) {
	u := validObserverUM()
	u.ObserverLogins.HookSecret = "short-secret-15c"[:15]
	if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), "observerLogins.hookSecret must be at least 16") {
		t.Fatalf("15 chars: err = %v", err)
	}
	u.ObserverLogins.HookSecret = "exactly-16-chars"
	if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err != nil {
		t.Fatalf("16 chars: %v", err)
	}
	// The environment value wins and is the one checked.
	u = validObserverUM()
	env := map[string]string{"CORESCOPE_OBSERVER_HOOK_SECRET": "short"}
	if _, err := resolveUserManagement(u, "meshcore.db", func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "must be at least 16") {
		t.Fatalf("short env over long config: err = %v", err)
	}
	u.ObserverLogins.HookSecret = "short"
	env["CORESCOPE_OBSERVER_HOOK_SECRET"] = "env-hook-secret-0123456789"
	if _, err := resolveUserManagement(u, "meshcore.db", func(k string) string { return env[k] }); err != nil {
		t.Fatalf("long env over short config: %v", err)
	}
}

func TestResolveObserverLoginsSharedUsernames(t *testing.T) {
	set, err := resolveUserManagement(validObserverUM(), "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.observerLogins.sharedUsernames) != 0 {
		t.Fatalf("default shared = %v", set.observerLogins.sharedUsernames)
	}
	u := validObserverUM()
	u.ObserverLogins.SharedUsernames = []string{" meshcore_on8ar ", " ", ""}
	set, err = resolveUserManagement(u, "meshcore.db", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := set.observerLogins.sharedUsernames; len(got) != 1 || !got["meshcore_on8ar"] {
		t.Fatalf("shared = %v", got)
	}
	u = validObserverUM()
	u.ObserverLogins.SharedUsernames = []string{"meshcore_on8ar", "obs_abc"}
	if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), `sharedUsernames: "obs_abc" is a personal login name`) {
		t.Fatalf("obs_ name: err = %v", err)
	}
}

func TestResolveObserverLogins_TransportValues(t *testing.T) {
	for _, tr := range []string{"tcp", "websockets"} {
		u := validObserverUM()
		u.ObserverLogins.Broker.Transport = tr
		if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err != nil {
			t.Errorf("%s: %v", tr, err)
		}
	}
	for _, tr := range []string{"ws", "wss", "TCP", "mqtt"} {
		u := validObserverUM()
		u.ObserverLogins.Broker.Transport = tr
		if _, err := resolveUserManagement(u, "meshcore.db", noEnv); err == nil || !strings.Contains(err.Error(), "broker.transport") {
			t.Errorf("%s: err = %v", tr, err)
		}
	}
}
