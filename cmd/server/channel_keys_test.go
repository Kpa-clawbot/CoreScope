package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

// --- deriveHashtagChannelKey ---

func TestDeriveHashtagChannelKey_KnownVector(t *testing.T) {
	// The derivation is SHA-256(channelName) → first 16 bytes hex (32 chars).
	// Compute the expected value the same way and assert the function matches.
	name := "#meshcore"
	full := sha256.Sum256([]byte(name))
	want := hex.EncodeToString(full[:16])

	got := deriveHashtagChannelKey(name)
	if got != want {
		t.Fatalf("deriveHashtagChannelKey(%q) = %q, want %q", name, got, want)
	}
	if len(got) != 32 {
		t.Fatalf("expected 32 hex chars (16 bytes), got %d", len(got))
	}
}

func TestDeriveHashtagChannelKey_NormalizationMatters(t *testing.T) {
	// Derivation is over the literal string including the leading '#'.
	// "#foo" and "foo" must produce different keys — callers normalize first.
	withHash := deriveHashtagChannelKey("#foo")
	withoutHash := deriveHashtagChannelKey("foo")
	if withHash == withoutHash {
		t.Fatal("expected '#foo' and 'foo' to derive distinct keys")
	}
	// Deterministic.
	if deriveHashtagChannelKey("#foo") != withHash {
		t.Fatal("derivation must be deterministic")
	}
}

// --- loadServerChannelKeys precedence ---

func TestLoadServerChannelKeys_DerivedFromHashChannels(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{HashChannels: []string{"meshcore", "#test"}}

	keys := loadServerChannelKeys(cfg, dir)

	// Bare names are normalized with a leading '#'.
	if keys["#meshcore"] != deriveHashtagChannelKey("#meshcore") {
		t.Errorf("#meshcore not derived correctly: %q", keys["#meshcore"])
	}
	if keys["#test"] != deriveHashtagChannelKey("#test") {
		t.Errorf("#test not derived correctly: %q", keys["#test"])
	}
}

func TestLoadServerChannelKeys_ExplicitOverridesDerived(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		HashChannels: []string{"#meshcore"},
		// Explicit config key for the same hashtag channel — highest priority.
		ChannelKeys: map[string]string{"#meshcore": "ffffffffffffffffffffffffffffffff"},
	}

	keys := loadServerChannelKeys(cfg, dir)

	if keys["#meshcore"] != "ffffffffffffffffffffffffffffffff" {
		t.Fatalf("explicit cfg.ChannelKeys must win over derived value, got %q", keys["#meshcore"])
	}
}

func TestLoadServerChannelKeys_RainbowFileLowestPriority(t *testing.T) {
	dir := t.TempDir()
	rainbow := filepath.Join(dir, "channel-rainbow.json")
	// Rainbow file supplies #public; cfg.ChannelKeys overrides #private's value;
	// derived hashtag wins over rainbow for #public.
	fileKeys := map[string]string{
		"#public":  "00000000000000000000000000000000",
		"#private": "11111111111111111111111111111111",
	}
	data, _ := json.Marshal(fileKeys)
	if err := os.WriteFile(rainbow, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		HashChannels: []string{"#public"},
		ChannelKeys:  map[string]string{"#private": "22222222222222222222222222222222"},
	}

	keys := loadServerChannelKeys(cfg, dir)

	// #public present in rainbow AND in hashChannels — derived value wins.
	if keys["#public"] != deriveHashtagChannelKey("#public") {
		t.Errorf("derived hashtag key should override rainbow value, got %q", keys["#public"])
	}
	// #private present in rainbow AND in cfg.ChannelKeys — explicit value wins.
	if keys["#private"] != "22222222222222222222222222222222" {
		t.Errorf("explicit cfg.ChannelKeys should override rainbow value, got %q", keys["#private"])
	}
}

func TestLoadServerChannelKeys_RainbowOnlyKeyKept(t *testing.T) {
	dir := t.TempDir()
	rainbow := filepath.Join(dir, "channel-rainbow.json")
	data, _ := json.Marshal(map[string]string{"#legacy": "33333333333333333333333333333333"})
	if err := os.WriteFile(rainbow, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}

	keys := loadServerChannelKeys(cfg, dir)

	// Key only in the rainbow file (no override) is retained verbatim.
	if keys["#legacy"] != "33333333333333333333333333333333" {
		t.Errorf("rainbow-only key should be kept, got %q", keys["#legacy"])
	}
}

func TestLoadServerChannelKeys_EnvOverridesPath(t *testing.T) {
	dir := t.TempDir()
	envDir := t.TempDir()
	envFile := filepath.Join(envDir, "keys.json")
	data, _ := json.Marshal(map[string]string{"#fromenv": "44444444444444444444444444444444"})
	if err := os.WriteFile(envFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHANNEL_KEYS_PATH", envFile)

	cfg := &Config{}
	keys := loadServerChannelKeys(cfg, dir)
	if keys["#fromenv"] != "44444444444444444444444444444444" {
		t.Errorf("CHANNEL_KEYS_PATH env var should drive the rainbow file path, got %q", keys["#fromenv"])
	}
}

func TestLoadServerChannelKeys_EmptyWhenNothingConfigured(t *testing.T) {
	dir := t.TempDir()
	keys := loadServerChannelKeys(&Config{}, dir)
	if len(keys) != 0 {
		t.Errorf("expected no keys with empty config and no rainbow file, got %d", len(keys))
	}
}

// --- publicChannelKeys filtering ---

func TestPublicChannelKeys_OnlyHashtagChannels(t *testing.T) {
	all := map[string]string{
		"#public":         deriveHashtagChannelKey("#public"),
		"#meshcore":       deriveHashtagChannelKey("#meshcore"),
		"private-channel": "deadbeefdeadbeefdeadbeefdeadbeef", // operator secret
		"OtherPrivate":    "cafecafecafecafecafecafecafecafe",
	}

	pub := publicChannelKeys(all)

	if _, ok := pub["private-channel"]; ok {
		t.Error("publicChannelKeys must NOT expose a private (non-#) channel key")
	}
	if _, ok := pub["OtherPrivate"]; ok {
		t.Error("publicChannelKeys must NOT expose a private (non-#) channel key")
	}
	if len(pub) != 2 {
		t.Fatalf("expected exactly 2 hashtag keys, got %d: %v", len(pub), pub)
	}
	for name := range pub {
		if name[0] != '#' {
			t.Errorf("publicChannelKeys returned non-hashtag channel %q", name)
		}
	}
}

func TestPublicChannelKeys_OverrideForHashtagNotLeaked(t *testing.T) {
	// An operator configured a *non-derivable* value for a #-channel. The
	// public subset must echo the SHA-256 derivation, never the override.
	override := "abcdefabcdefabcdefabcdefabcdefab"
	all := map[string]string{"#secretized": override}

	pub := publicChannelKeys(all)

	if pub["#secretized"] == override {
		t.Fatal("publicChannelKeys leaked an operator-configured override for a #-channel")
	}
	if pub["#secretized"] != deriveHashtagChannelKey("#secretized") {
		t.Fatalf("publicChannelKeys must serve the derivable value, got %q", pub["#secretized"])
	}
}

func TestPublicChannelKeys_EmptyInput(t *testing.T) {
	pub := publicChannelKeys(map[string]string{})
	if len(pub) != 0 {
		t.Errorf("expected empty result for empty input, got %d", len(pub))
	}
}

// --- handleConfigChannelKeys handler (auth gating) ---

// setupChannelKeysServer builds a server whose channelKeys map mixes a
// derivable hashtag channel and an operator-configured private channel.
func setupChannelKeysServer(t *testing.T, apiKey string) (*mux.Router, map[string]string) {
	t.Helper()
	db := setupTestDB(t)
	cfg := &Config{Port: 3000, APIKey: apiKey}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	full := map[string]string{
		"#public":         deriveHashtagChannelKey("#public"),
		"private-channel": "deadbeefdeadbeefdeadbeefdeadbeef",
	}
	srv.channelKeys = full
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router, full
}

func TestHandleConfigChannelKeys_UnauthenticatedGetsHashtagOnly(t *testing.T) {
	router, _ := setupChannelKeysServer(t, "a-strong-api-key-1234567")

	req := httptest.NewRequest("GET", "/api/config/channel-keys", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if _, ok := body["private-channel"]; ok {
		t.Error("unauthenticated response leaked a private channel key")
	}
	if body["#public"] != deriveHashtagChannelKey("#public") {
		t.Errorf("expected derivable #public key, got %q", body["#public"])
	}
	if len(body) != 1 {
		t.Fatalf("expected only hashtag keys for unauthenticated caller, got %d: %v", len(body), body)
	}
}

func TestHandleConfigChannelKeys_AuthenticatedGetsFullMap(t *testing.T) {
	apiKey := "a-strong-api-key-1234567"
	router, full := setupChannelKeysServer(t, apiKey)

	req := httptest.NewRequest("GET", "/api/config/channel-keys", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if body["private-channel"] != full["private-channel"] {
		t.Errorf("authenticated caller should receive the private key, got %q", body["private-channel"])
	}
	if body["#public"] != full["#public"] {
		t.Errorf("authenticated caller should receive #public, got %q", body["#public"])
	}
	if len(body) != len(full) {
		t.Fatalf("authenticated caller should get the full map (%d), got %d", len(full), len(body))
	}
}

func TestHandleConfigChannelKeys_WrongKeyTreatedAsUnauthenticated(t *testing.T) {
	router, _ := setupChannelKeysServer(t, "a-strong-api-key-1234567")

	req := httptest.NewRequest("GET", "/api/config/channel-keys", nil)
	req.Header.Set("X-API-Key", "wrong-key-but-long-enough")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (endpoint is unauthenticated), got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if _, ok := body["private-channel"]; ok {
		t.Error("wrong API key must NOT unlock the private channel key")
	}
}

func TestHandleConfigChannelKeys_WeakKeyTreatedAsUnauthenticated(t *testing.T) {
	// A weak API key never authorizes, even when it matches cfg.APIKey.
	weakKey := "test"
	router, _ := setupChannelKeysServer(t, weakKey)

	req := httptest.NewRequest("GET", "/api/config/channel-keys", nil)
	req.Header.Set("X-API-Key", weakKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if _, ok := body["private-channel"]; ok {
		t.Error("weak API key must NOT unlock the private channel key")
	}
}
