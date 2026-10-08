package users

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDeviceSessionKindLabelScopes(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "dev@example.org", "Dev")
	_, web, err := st.CreateSession(u.ID, time.Hour, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if web.Kind != SessionKindWeb || web.Label != "" || web.Scopes != nil || !web.HasScope("anything") {
		t.Fatalf("web session = %+v", web)
	}

	raw, dev, err := st.CreateDeviceSession(u.ID, "  Pixel\x07 8  ", []string{"rx", "account"}, "CoreDriveRX/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || dev.Kind != SessionKindDevice || dev.Label != "Pixel 8" || dev.CSRFToken == "" ||
		!reflect.DeepEqual(dev.Scopes, []string{"rx", "account"}) || !dev.ExpiresAt.Equal(clk.Now().Add(DeviceSessionTTL)) {
		t.Fatalf("CreateDeviceSession = %+v", dev)
	}

	got, err := st.LookupSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != dev.ID || got.Kind != SessionKindDevice || got.Label != "Pixel 8" || got.UserAgent != "CoreDriveRX/1.0" ||
		!reflect.DeepEqual(got.Scopes, []string{"rx", "account"}) {
		t.Fatalf("LookupSession = %+v", got)
	}
	if !got.HasScope("rx") || !got.HasScope("account") || got.HasScope("admin") || got.HasScope("") {
		t.Fatalf("HasScope wrong for %v", got.Scopes)
	}

	list, err := st.ListSessions(u.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}
	kinds := map[string]string{}
	for _, s := range list {
		kinds[s.Kind] = s.Label
	}
	if l, ok := kinds[SessionKindDevice]; !ok || l != "Pixel 8" {
		t.Fatalf("device row missing from list: %+v", list)
	}
	if l, ok := kinds[SessionKindWeb]; !ok || l != "" {
		t.Fatalf("web row missing from list: %+v", list)
	}
}

func TestDeviceSessionSlidingExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "slide@example.org", "Slide")
	raw, dev, err := st.CreateDeviceSession(u.ID, "Phone", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(DeviceSessionTTL - time.Hour)
	if err := st.ExtendSession(dev.ID, DeviceSessionTTL); err != nil {
		t.Fatal(err)
	}
	clk.Advance(DeviceSessionTTL - time.Hour) // well past the original expiry
	if _, err := st.LookupSession(raw); err != nil {
		t.Fatalf("extended device session expired early: %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired device session err = %v", err)
	}
}

func TestDeviceSessionRevoke(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "rev@example.org", "Rev")
	other := mustCreate(t, st, "oth@example.org", "Oth")
	raw1, d1, _ := st.CreateDeviceSession(u.ID, "One", []string{"rx"}, "")
	raw2, _, _ := st.CreateDeviceSession(u.ID, "Two", []string{"rx"}, "")

	if err := st.DeleteSession(other.ID, d1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking someone else's device = %v", err)
	}
	if err := st.DeleteSession(u.ID, d1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupSession(raw1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device token still resolves: %v", err)
	}
	if err := st.DeleteSessionByToken(raw2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupSession(raw2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logged-out device token still resolves: %v", err)
	}
}

func TestDeviceSessionRejectsBadScopes(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "scope@example.org", "Scope")
	if _, _, err := st.CreateDeviceSession(u.ID, "x", nil, ""); !errors.Is(err, ErrNoScopes) {
		t.Fatalf("nil scopes err = %v", err)
	}
	for _, bad := range []string{"", "a,b", "Rx", "rx scope"} {
		if _, _, err := st.CreateDeviceSession(u.ID, "x", []string{bad}, ""); err == nil {
			t.Fatalf("scope %q accepted", bad)
		}
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("rejected device sessions were stored: %+v", list)
	}
}

func TestCleanLabel(t *testing.T) {
	cases := map[string]string{
		"  Car  ":      "Car",
		"a\tb\nc\x00d": "abcd",
		"bad\xffutf8":  "badutf8",
		"":             "",
	}
	for in, want := range cases {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
	long := CleanLabel(strings.Repeat("é", 70))
	if utf8.RuneCountInString(long) != 64 {
		t.Fatalf("CleanLabel cap = %d runes", utf8.RuneCountInString(long))
	}
}
