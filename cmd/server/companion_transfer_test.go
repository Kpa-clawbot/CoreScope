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

// The transfer mail is a security notice: it goes out whatever the node
// notification settings are, only not to an inactive or bouncing address.
func TestCompanionTransferMailIgnoresNotificationSettings(t *testing.T) {
	for _, tc := range []struct {
		name           string
		instance, user bool
		bouncing       bool
		wantMails      int
	}{
		{"instance notifications off", false, true, false, 1},
		{"user opted out", true, false, false, 1},
		{"both off", false, false, false, 1},
		{"address bouncing", true, true, true, 0},
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
			if tc.bouncing {
				if err := f.st.SetEmailBouncing(alice.me.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			sent := len(f.fake.Sent())
			expectStatus(t, f.linkCompanion(t, bobTok, companionKey, "Bike"), http.StatusOK)
			if !hasCompanionAudit(t, f, alice.me.ID, "companion.transfer", pubHex(companionKey)) {
				t.Fatal("no companion.transfer audit row")
			}
			if n := len(f.fake.Sent()) - sent; n != tc.wantMails {
				t.Fatalf("%d transfer mail(s) sent, want %d", n, tc.wantMails)
			}
		})
	}
}
