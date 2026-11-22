package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushRelayEnrollmentPersistsAndRestoresOnlyForItsPublicOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	serverOrigin, err := url.Parse("https://analyzer.example.org")
	if err != nil {
		t.Fatal(err)
	}
	relay := "https://relay.example.org"
	sender := strings.Repeat("s", 43)
	newService := func(base *url.URL, enabled bool) *authService {
		return &authService{set: &userMgmtSettings{dbPath: path, baseURL: base, notify: notifySettings{enabled: enabled}}}
	}
	first := newService(serverOrigin, true)
	if err := first.activateRelayEnrollment(relay, []string{"android"}, "an_123", sender); err != nil {
		t.Fatal(err)
	}
	if active := first.relaySettings(); active.relay == nil || active.relayAnalyzerID != "an_123" || active.relayCredential != sender ||
		strings.Join(active.relayPlatforms, ",") != "android" {
		t.Fatal("enrollment did not immediately activate")
	}
	info, err := os.Stat(first.relayEnrollmentPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode is %o", info.Mode().Perm())
	}

	for _, tc := range []struct {
		name       string
		origin     string
		enabled    bool
		wantID     string
		wantActive bool
	}{
		{"restored", "https://ANALYZER.example.org:443", true, "an_123", true},
		{"paused", "https://analyzer.example.org", false, "an_123", false},
		{"other origin", "https://new.example.org", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := url.Parse(tc.origin)
			if err != nil {
				t.Fatal(err)
			}
			restarted := newService(base, tc.enabled)
			id, err := restarted.restoreRelayEnrollment()
			if err != nil || id != tc.wantID {
				t.Fatalf("restored enrollment ID = %q, error = %v", id, err)
			}
			if active := restarted.relaySettings(); (active.relay != nil) != tc.wantActive {
				t.Fatalf("restored active state = %t", active.relay != nil)
			}
		})
	}
}
