package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// Deterministic companion identities (Ed25519 signatures are deterministic too).
var (
	companionKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	otherKey     = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, 32))
)

const testHost = "scope.example.org" // host of testBase

func pubHex(k ed25519.PrivateKey) string { return hex.EncodeToString(k.Public().(ed25519.PublicKey)) }

func signLink(k ed25519.PrivateKey, host, challenge string) string {
	return hex.EncodeToString(ed25519.Sign(k, []byte("corescope-link:"+host+":"+challenge)))
}

func (f *authFixture) companionChallenge(t *testing.T, tok, pk string) string {
	t.Helper()
	w := f.do("POST", "/api/account/companions/challenge", companionChallengeRequest{Pubkey: pk}, bearer(tok), header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	resp := decode[companionChallengeResponse](t, w)
	if resp.Host != testHost {
		t.Fatalf("challenge host = %q; want %q (the host the server verifies against)", resp.Host, testHost)
	}
	return resp.Challenge
}

// linkCompanion runs the RX flow: challenge, sign with the companion key, link.
func (f *authFixture) linkCompanion(t *testing.T, tok string, k ed25519.PrivateKey, name string) *httptest.ResponseRecorder {
	t.Helper()
	ch := f.companionChallenge(t, tok, pubHex(k))
	return f.do("POST", "/api/account/companions",
		companionLinkRequest{Pubkey: pubHex(k), Challenge: ch, Signature: signLink(k, testHost, ch), Name: name},
		bearer(tok), header("Origin", rxOrigin))
}

func hasCompanionAudit(t *testing.T, f *authFixture, uid int64, action, pk string) bool {
	t.Helper()
	f.srv.auth.waitAudits()
	entries, err := f.st.AuditFor(uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action && e.Detail["pubkey"] == pk {
			return true
		}
	}
	return false
}

func TestCompanionLinkFlow(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	pk := pubHex(companionKey)

	w := f.linkCompanion(t, tok, companionKey, "Car")
	expectStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "myNodes") {
		t.Fatalf("link response still reports myNodes: %s", w.Body.String())
	}
	got := decode[companionLinkResponse](t, w)
	if got.Pubkey != pk || got.Name != "Car" || got.LinkedAt == "" {
		t.Fatalf("link response = %+v", got)
	}
	if l, err := f.st.GetCompanionLink(pk); err != nil || l.UserID != alice.me.ID {
		t.Fatalf("stored link = %+v, %v", l, err)
	}
	if !hasCompanionAudit(t, f, alice.me.ID, "companion.link", pk) {
		t.Fatal("no companion.link audit row")
	}

	// The browser lists it; no analyzer data, so lastSeenAt is null.
	w = f.do("GET", "/api/account/companions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"lastSeenAt":null`) {
		t.Fatalf("list = %s", w.Body.String())
	}
	if list := decode[[]companionJSON](t, w); len(list) != 1 || list[0].Pubkey != pk || list[0].Name != "Car" {
		t.Fatalf("list = %+v", list)
	}

	// A companion is not a node to monitor: linking leaves the synced
	// settings (meshcore-my-nodes) alone.
	if _, v, _ := f.st.GetSettings(alice.me.ID); v.Revision != 0 {
		t.Fatalf("linking wrote the synced settings (revision %d)", v.Revision)
	}

	// Unlink from the browser (cookie + CSRF).
	expectStatus(t, f.do("DELETE", "/api/account/companions/"+pk, nil, as(alice)), http.StatusNoContent)
	if _, err := f.st.GetCompanionLink(pk); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("link survived unlink: %v", err)
	}
	if !hasCompanionAudit(t, f, alice.me.ID, "companion.unlink", pk) {
		t.Fatal("no companion.unlink audit row")
	}
	expectStatus(t, f.do("DELETE", "/api/account/companions/"+pk, nil, as(alice)), http.StatusNotFound)
	expectStatus(t, f.do("DELETE", "/api/account/companions/nothex", nil, as(alice)), http.StatusBadRequest)
	w = f.do("GET", "/api/account/companions", nil, bearer(tok))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %s", w.Code, w.Body.String())
	}
}

func TestCompanionLinkRejections(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	pk := pubHex(companionKey)
	post := func(req companionLinkRequest) *httptest.ResponseRecorder {
		return f.do("POST", "/api/account/companions", req, bearer(tok))
	}

	// Wrong host in the signed message: 400, and the challenge is burned.
	ch := f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, "other.example.org", ch)}), http.StatusBadRequest)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)

	// Signed by another key; malformed signature.
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(otherKey, testHost, ch)}), http.StatusBadRequest)
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: "abcd"}), http.StatusBadRequest)

	// A challenge bound to another pubkey; an unknown challenge.
	ch = f.companionChallenge(t, tok, pubHex(otherKey))
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)
	unknown := strings.Repeat("00", 32)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: unknown, Signature: signLink(companionKey, testHost, unknown)}), http.StatusGone)

	// Malformed pubkey: 400, and the challenge is still consumed.
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: "nothex", Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusBadRequest)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)
	expectStatus(t, f.do("POST", "/api/account/companions/challenge", companionChallengeRequest{Pubkey: "zz"}, bearer(tok)), http.StatusBadRequest)

	// Expired challenge.
	f.st.SetClock(func() time.Time { return time.Now().Add(-10 * time.Minute) })
	ch = f.companionChallenge(t, tok, pk)
	f.st.SetClock(time.Now)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)

	// Reused challenge: the first use links, the second answers 410.
	ch = f.companionChallenge(t, tok, pk)
	good := companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch), Name: "Car"}
	expectStatus(t, post(good), http.StatusOK)
	expectStatus(t, post(good), http.StatusGone)

	if list, _ := f.st.ListCompanionLinks(alice.me.ID); len(list) != 1 {
		t.Fatalf("links = %+v, want only the good one", list)
	}
}

func TestCompanionListLastSeen(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedCoverageDB(t)
	pk := pubHex(companionKey)
	for _, at := range []string{"2026-10-01T10:00:00Z", "2026-10-02T09:00:00Z"} {
		mustExecDB(t, f.srv.db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
			VALUES ('`+pk+`','aabbcc',3,-6,51.05,3.72,'`+at+`','t','rxlog')`)
	}
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)

	w := f.do("GET", "/api/account/companions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	list := decode[[]companionJSON](t, w)
	if len(list) != 1 || list[0].LastSeenAt == nil || *list[0].LastSeenAt != "2026-10-02T09:00:00Z" {
		t.Fatalf("list = %s", w.Body.String())
	}
}
