package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestCompanionTransfer(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.auth.set.notify.enabled = true
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	aliceTok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	bobTok := f.deviceToken(t, "bob@example.org", pw, "iPhone").Token
	pk := pubHex(companionKey)

	expectStatus(t, f.linkCompanion(t, aliceTok, companionKey, "Car"), http.StatusOK)
	sent := len(f.fake.Sent())
	expectStatus(t, f.linkCompanion(t, bobTok, companionKey, "Bike"), http.StatusOK)

	if l, err := f.st.GetCompanionLink(pk); err != nil || l.UserID != bob.me.ID {
		t.Fatalf("after transfer: %+v, %v", l, err)
	}
	for _, uid := range []int64{alice.me.ID, bob.me.ID} {
		if !hasCompanionAudit(t, f, uid, "companion.transfer", pk) {
			t.Fatalf("no companion.transfer audit row for user #%d", uid)
		}
	}
	msgs := f.fake.Sent()
	if len(msgs) != sent+1 {
		t.Fatalf("mails sent for the transfer = %d, want 1", len(msgs)-sent)
	}
	if m := msgs[len(msgs)-1]; m.To != "alice@example.org" || !strings.Contains(m.Text, "Car") || !strings.Contains(m.Text, pk[:12]) {
		t.Fatalf("transfer mail = to %q, text %q", m.To, m.Text)
	}
	if list, _ := f.st.ListCompanionLinks(alice.me.ID); len(list) != 0 {
		t.Fatalf("previous owner still lists it: %+v", list)
	}
}

func TestCompanionTransferMailNeedsNotifications(t *testing.T) {
	for _, tc := range []struct {
		name           string
		instance, user bool
	}{
		{"instance off", false, true},
		{"user opted out", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.srv.auth.set.notify.enabled = tc.instance
			alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
			f.registerAndActivate(t, "bob@example.org", "Bob", pw)
			if !tc.user {
				if _, err := f.st.SetNotifyPrefs(alice.me.ID, false, users.NodeNotifyEvents); err != nil {
					t.Fatal(err)
				}
			}
			aliceTok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
			bobTok := f.deviceToken(t, "bob@example.org", pw, "iPhone").Token
			expectStatus(t, f.linkCompanion(t, aliceTok, companionKey, "Car"), http.StatusOK)
			sent := len(f.fake.Sent())
			expectStatus(t, f.linkCompanion(t, bobTok, companionKey, "Bike"), http.StatusOK)
			if n := len(f.fake.Sent()) - sent; n != 0 {
				t.Fatalf("%d transfer mail(s) sent", n)
			}
			if !hasCompanionAudit(t, f, alice.me.ID, "companion.transfer", pubHex(companionKey)) {
				t.Fatal("audit row missing without a mail")
			}
		})
	}
}
