package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkedCompanionFilterStartup(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	usersPath := filepath.Join(t.TempDir(), "users.db")
	setLinks(t, usersPath, testCompanionPK)
	um := func(on bool) *UserManagementConfig { return &UserManagementConfig{Enabled: on, DBPath: usersPath} }

	off := &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}, UserManagement: um(true)}
	if f := newLinkedCompanionFilter(off); f != nil {
		t.Fatal("filter installed with the setting off")
	}
	if buf.Len() != 0 {
		t.Fatalf("logged with the setting off:\n%s", buf.String())
	}

	ignored := &Config{ClientRxCoverage: &ClientRxCoverageConfig{RequireLinkedCompanion: true}, UserManagement: um(false)}
	if f := newLinkedCompanionFilter(ignored); f != nil {
		t.Fatal("filter installed with user management off")
	}
	if !strings.Contains(buf.String(), "requireLinkedCompanion is ignored") {
		t.Fatalf("no startup warning for the ignored setting:\n%s", buf.String())
	}

	on := &Config{ClientRxCoverage: &ClientRxCoverageConfig{RequireLinkedCompanion: true}, UserManagement: um(true)}
	f := newLinkedCompanionFilter(on)
	if f == nil {
		t.Fatal("no filter with the setting and user management on")
	}
	defer f.Close()
	if !f.Allow(testCompanionPK) {
		t.Fatal("startup did not read the linked set")
	}
	if !strings.Contains(buf.String(), "only linked companions") {
		t.Fatalf("no startup line naming the filter:\n%s", buf.String())
	}
}
