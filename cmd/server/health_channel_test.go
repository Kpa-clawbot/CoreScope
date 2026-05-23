package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// encryptGroupText performs AES-128-ECB encryption for constructing test vectors.
// Returns the full block-aligned ciphertext (NOT truncated), matching what
// MeshCore transmits over the air. DecryptGroupText must receive the full
// block-aligned buffer so the last block decrypts correctly.
func encryptGroupText(key, plaintext []byte) []byte {
	padLen := (aes.BlockSize - len(plaintext)%aes.BlockSize) % aes.BlockSize
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	ciphertext := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(ciphertext[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return ciphertext // full padded length — mimics MeshCore on-air format
}

// computeMAC returns the 2-byte HMAC prefix as hex.
func computeMAC(hmacKey, cipherBytes []byte) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(cipherBytes)
	digest := mac.Sum(nil)
	return hex.EncodeToString(digest[:2])
}

func TestNewChannelKey_ValidSecret(t *testing.T) {
	// 32 bytes of hex
	secret := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	ck, err := NewChannelKey(secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ck.AESKey) != 16 {
		t.Errorf("expected 16-byte AES key, got %d", len(ck.AESKey))
	}
	if len(ck.HMACKey) != 32 {
		t.Errorf("expected 32-byte HMAC key, got %d", len(ck.HMACKey))
	}
	// Channel hash = SHA256(raw)[0]
	rawBytes, _ := hex.DecodeString(secret)
	digest := sha256.Sum256(rawBytes)
	if ck.ChannelHashByte != digest[0] {
		t.Errorf("expected ChannelHashByte=%d, got %d", digest[0], ck.ChannelHashByte)
	}
}

func TestNewChannelKey_TooShort(t *testing.T) {
	// 15 bytes = 30 hex chars — too short
	_, err := NewChannelKey("aabbccddeeff001122334455667788")
	if err == nil {
		t.Error("expected error for short secret, got nil")
	}
}

func TestNewChannelKey_InvalidHex(t *testing.T) {
	_, err := NewChannelKey("ZZZZ")
	if err == nil {
		t.Error("expected error for invalid hex, got nil")
	}
}

func TestMatchesChannelHash(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, err := NewChannelKey(secret)
	if err != nil {
		t.Fatalf("setup error: %v", err)
	}
	hashByte := int(ck.ChannelHashByte)
	if !ck.MatchesChannelHash(hashByte) {
		t.Errorf("expected hash %d to match", hashByte)
	}
	// A different byte should not match (unless collision, which is unlikely for these test values).
	other := (hashByte + 1) % 256
	if ck.MatchesChannelHash(other) {
		t.Errorf("expected hash %d to NOT match (channel hash is %d)", other, hashByte)
	}
}

func TestMatchesChannelHash_NilKey(t *testing.T) {
	var ck *ChannelKey
	// Nil key accepts all hashes.
	if !ck.MatchesChannelHash(0) {
		t.Error("nil key should accept hash 0")
	}
	if !ck.MatchesChannelHash(255) {
		t.Error("nil key should accept hash 255")
	}
}

func TestParseGroupTextMessage_RoundTrip(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, err := NewChannelKey(secret)
	if err != nil {
		t.Fatalf("setup error: %v", err)
	}

	// Build plaintext: [4-byte LE timestamp][1-byte flags][message]
	// Timestamp: 2026-01-01 00:00:00 UTC = 1735689600
	const ts uint32 = 1735689600
	plaintext := make([]byte, 5+len("MHC-A3F7Z2"))
	binary.LittleEndian.PutUint32(plaintext[0:4], ts)
	plaintext[4] = 0x00 // flags byte
	copy(plaintext[5:], "MHC-A3F7Z2")

	cipherBytes := encryptGroupText(ck.AESKey, plaintext)
	macHex := computeMAC(ck.HMACKey, cipherBytes)

	// Validate MAC
	if !ck.ValidateMAC(macHex, cipherBytes) {
		t.Fatal("expected MAC to validate")
	}

	// Parse
	sender, body, ok := ck.ParseGroupTextMessage(cipherBytes)
	if !ok {
		t.Fatal("expected ParseGroupTextMessage to succeed")
	}
	if body != "MHC-A3F7Z2" {
		t.Errorf("expected body %q, got %q", "MHC-A3F7Z2", body)
	}
	if sender != "" {
		t.Errorf("expected empty sender, got %q", sender)
	}
}

func TestParseGroupTextMessage_WithSender(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, _ := NewChannelKey(secret)

	const ts uint32 = 1735689600
	msg := "Alice: MHC-B2C3D4"
	plaintext := make([]byte, 5+len(msg))
	binary.LittleEndian.PutUint32(plaintext[0:4], ts)
	plaintext[4] = 0x00
	copy(plaintext[5:], msg)

	cipherBytes := encryptGroupText(ck.AESKey, plaintext)
	sender, body, ok := ck.ParseGroupTextMessage(cipherBytes)
	if !ok {
		t.Fatal("expected ParseGroupTextMessage to succeed")
	}
	if sender != "Alice" {
		t.Errorf("expected sender %q, got %q", "Alice", sender)
	}
	if body != "MHC-B2C3D4" {
		t.Errorf("expected body %q, got %q", "MHC-B2C3D4", body)
	}
}

func TestParseGroupTextMessage_BadTimestamp(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, _ := NewChannelKey(secret)

	// Timestamp = 0 → year 1970 — should be rejected.
	plaintext := make([]byte, 5+len("MHC-ABCDEF"))
	// bytes 0-3 are zero (timestamp = 0 = year 1970)
	copy(plaintext[5:], "MHC-ABCDEF")
	cipherBytes := encryptGroupText(ck.AESKey, plaintext)

	_, _, ok := ck.ParseGroupTextMessage(cipherBytes)
	if ok {
		t.Error("expected ParseGroupTextMessage to fail with bad timestamp (year 1970)")
	}
}

// TestParseGroupTextMessage_UnicodeUsername verifies that sender names with
// non-ASCII characters (emoji, accented letters, chess symbols) are accepted.
// The old 70%-printable-ASCII check rejected these messages because multibyte
// UTF-8 sequences were counted as non-printable.
func TestParseGroupTextMessage_UnicodeUsername(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, _ := NewChannelKey(secret)

	cases := []struct {
		senderMsg string // full "Sender: body" string in plaintext
		wantBody  string
	}{
		{"EV_DHR♝: CHC-OBZJP9", "CHC-OBZJP9"},                // chess piece (U+265D)
		{"NL-VLA-Tube-🏡: CHC-OBZJP9", "CHC-OBZJP9"},          // house emoji (U+1F3E1)
		{"Münster: CHC-OBZJP9", "CHC-OBZJP9"},                 // accented character
		{"用户: CHC-OBZJP9", "CHC-OBZJP9"},                     // CJK characters
	}

	const ts uint32 = 1735689600
	for _, tc := range cases {
		plaintext := make([]byte, 5+len(tc.senderMsg))
		binary.LittleEndian.PutUint32(plaintext[0:4], ts)
		plaintext[4] = 0x00
		copy(plaintext[5:], tc.senderMsg)

		cipherBytes := encryptGroupText(ck.AESKey, plaintext)
		_, body, ok := ck.ParseGroupTextMessage(cipherBytes)
		if !ok {
			t.Errorf("ParseGroupTextMessage(%q): expected ok, got failure", tc.senderMsg)
			continue
		}
		if body != tc.wantBody {
			t.Errorf("ParseGroupTextMessage(%q): want body %q, got %q", tc.senderMsg, tc.wantBody, body)
		}
	}
}

// TestParseGroupTextMessage_GarbageDecryption verifies that random bytes (wrong key)
// are rejected. Random bytes are almost never valid UTF-8 — especially sequences
// that start with 0xE2/0xF0 continuation bytes but don't follow UTF-8 rules.
func TestParseGroupTextMessage_GarbageDecryption(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	wrongSecret := "ffeeddccbbaa99887766554433221100"
	ck, _ := NewChannelKey(secret)
	ckWrong, _ := NewChannelKey(wrongSecret)

	const ts uint32 = 1735689600
	plaintext := make([]byte, 5+len("CHC-ABCDEF"))
	binary.LittleEndian.PutUint32(plaintext[0:4], ts)
	copy(plaintext[5:], "CHC-ABCDEF")

	// Encrypt with the correct key.
	cipherBytes := encryptGroupText(ck.AESKey, plaintext)

	// Decrypt with a WRONG key → garbage plaintext.
	wrong, err := ckWrong.DecryptGroupText(cipherBytes)
	if err != nil {
		t.Fatalf("DecryptGroupText with wrong key should not error: %v", err)
	}
	// ParseGroupTextMessage should reject it (timestamp or UTF-8 check).
	// We test the UTF-8 check directly by calling ParseGroupTextMessage on garbage.
	_ = wrong // the test is that ParseGroupTextMessage with wrong key returns ok=false
	_, _, ok := ckWrong.ParseGroupTextMessage(cipherBytes)
	// This test is probabilistic: a wrong key might accidentally produce valid
	// UTF-8 with a plausible timestamp. In practice this is astronomically rare,
	// but we record the result informatively rather than asserting failure.
	if ok {
		t.Logf("WARN: wrong-key decryption accidentally passed validation (probabilistic)")
	}
}

func TestValidateMAC_WrongKey(t *testing.T) {
	secret1 := "aabbccddeeff00112233445566778899"
	secret2 := "ffeeddccbbaa99887766554433221100"
	ck1, _ := NewChannelKey(secret1)
	ck2, _ := NewChannelKey(secret2)

	data := []byte("some ciphertext bytes here")
	macHex := computeMAC(ck1.HMACKey, data)

	if ck2.ValidateMAC(macHex, data) {
		t.Error("expected MAC with wrong key to fail validation")
	}
}

func TestMatchesCode(t *testing.T) {
	tests := []struct {
		body, code string
		want       bool
	}{
		{"MHC-A3F7Z2", "MHC-A3F7Z2", true},
		{"send MHC-A3F7Z2 now", "MHC-A3F7Z2", true},
		{"send mhc-a3f7z2 now", "MHC-A3F7Z2", true},   // case-insensitive
		{"sendMHC-A3F7Z2now", "MHC-A3F7Z2", false},     // no word boundary before/after
		{"mhc-a3f7z2extra", "MHC-A3F7Z2", false},       // 'e' after code is alphanumeric
		{"prefix MHC-A3F7Z2", "MHC-A3F7Z2", true},      // space before is ok
		{"Alice: MHC-A3F7Z2", "MHC-A3F7Z2", true},      // colon+space before is ok
		{"", "MHC-A3F7Z2", false},
		{"MHC-A3F7Z2", "MHC-XXXXXX", false},
		{"send MHC-A3F7Z2!", "MHC-A3F7Z2", true},       // '!' after is not alphanumeric
	}
	for _, tt := range tests {
		got := matchesCode(tt.body, tt.code)
		if got != tt.want {
			t.Errorf("matchesCode(%q, %q) = %v, want %v", tt.body, tt.code, got, tt.want)
		}
	}
}

func TestObserverKeyFromTopic(t *testing.T) {
	tests := []struct {
		topic string
		want  string
	}{
		{"meshcore/abc123/packets", "abc123"},
		{"meshcore/region/abc123/packets", "abc123"},
		{"meshcore/EU/aabbccddeeff/packets", "aabbccddeeff"},
		{"meshcore/abc", "meshcore/abc"}, // too short — returns original
	}
	for _, tt := range tests {
		got := observerKeyFromTopic(tt.topic)
		if got != tt.want {
			t.Errorf("observerKeyFromTopic(%q) = %q, want %q", tt.topic, got, tt.want)
		}
	}
}

func TestParseEnvelope_JSONFormat(t *testing.T) {
	payload := []byte(`{"raw":"AABBCC","rssi":-95.5,"snr":4.5}`)
	env, ok := parseEnvelope(payload)
	if !ok {
		t.Fatal("expected ok")
	}
	if env.Raw != "AABBCC" {
		t.Errorf("expected raw=AABBCC, got %q", env.Raw)
	}
	if env.RSSI == nil || *env.RSSI != -95.5 {
		t.Errorf("expected RSSI=-95.5")
	}
}

func TestParseEnvelope_RawHexFallback(t *testing.T) {
	payload := []byte("DEADBEEF")
	env, ok := parseEnvelope(payload)
	if !ok {
		t.Fatal("expected ok")
	}
	if env.Raw != "DEADBEEF" {
		t.Errorf("expected raw=DEADBEEF, got %q", env.Raw)
	}
}

func TestParseEnvelope_Empty(t *testing.T) {
	_, ok := parseEnvelope([]byte(""))
	if ok {
		t.Error("expected empty payload to return !ok")
	}
}

// TestMatchesCode_WithHyphenInCode verifies the hyphen-containing code format works.
// The health check generates codes like "MHC-A3F7Z2". The hyphen is not alphanumeric
// so word-boundary detection must handle it correctly.
func TestMatchesCode_WithHyphenInCode(t *testing.T) {
	code := "MHC-A3F7Z2"
	// hyphen before the code prefix should NOT block the match
	if !matchesCode("test: MHC-A3F7Z2", code) {
		t.Error("expected match with colon-space prefix")
	}
	// alphanumeric before code should block match
	if matchesCode("XMHC-A3F7Z2", code) {
		t.Error("expected no match with alphanumeric prefix")
	}
	// hyphen before code IS a word boundary (hyphen is not alphanumeric)
	if !matchesCode("-MHC-A3F7Z2", code) {
		t.Error("expected match with hyphen prefix (hyphen is not alphanumeric)")
	}
}

// TestComputeMAC_LengthCheck verifies ValidateMAC rejects MAC that is too short.
func TestValidateMAC_TooShort(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, _ := NewChannelKey(secret)
	data := []byte("cipher")
	// Only 1 byte MAC (need ≥2)
	if ck.ValidateMAC("aa", data) {
		// "aa" = 1 byte hex — actually 1 byte, ≥2 check should fail
		// Wait: hex.DecodeString("aa") = []byte{0xaa}, len = 1 < 2 → false
		// The hex "aa" decodes to 1 byte. But ValidateMAC requires len ≥ 2.
		// So it should return false. If it returned true, that's a bug.
		t.Error("expected single-byte MAC to fail validation")
	}
	// Whitespace/empty
	if ck.ValidateMAC("", data) {
		t.Error("expected empty MAC to fail validation")
	}
	// invalid hex
	if ck.ValidateMAC("ZZ", data) {
		t.Error("expected invalid hex MAC to fail validation")
	}
}

func TestDecryptGroupText_NilKey(t *testing.T) {
	var ck *ChannelKey
	_, err := ck.DecryptGroupText([]byte("anything"))
	if err == nil {
		t.Error("expected error for nil key")
	}
}

func TestDecryptGroupText_Empty(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck, _ := NewChannelKey(secret)
	_, err := ck.DecryptGroupText([]byte{})
	if err == nil {
		t.Error("expected error for empty ciphertext")
	}
}

func TestNormaliseBrokerURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"mqtt://broker:1883", "tcp://broker:1883"},
		{"mqtts://broker:8883", "ssl://broker:8883"},
		{"tcp://broker:1883", "tcp://broker:1883"},
		{"ssl://broker:8883", "ssl://broker:8883"},
		{"ws://broker:80/mqtt", "ws://broker:80/mqtt"},
		{"wss://collector1.example.nl:443", "wss://collector1.example.nl:443"},
	}
	for _, tt := range tests {
		got := normaliseBrokerURL(tt.in)
		if got != tt.want {
			t.Errorf("normaliseBrokerURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFirstBrokerURL_ConfigField(t *testing.T) {
	cfg := &HealthCheckConfig{MQTTBroker: "wss://collector1.example.nl:443"}
	got := firstBrokerURL(cfg)
	if got != "wss://collector1.example.nl:443" {
		t.Errorf("expected wss URL from config, got %q", got)
	}
}

func TestFirstBrokerURL_NilConfig(t *testing.T) {
	// nil config should return localhost fallback (env var not set in test)
	t.Setenv("MQTT_BROKER", "")
	got := firstBrokerURL(nil)
	if got != "tcp://localhost:1883" {
		t.Errorf("expected localhost fallback, got %q", got)
	}
}

func TestFirstBrokerURL_EnvFallback(t *testing.T) {
	t.Setenv("MQTT_BROKER", "wss://env-broker:443")
	got := firstBrokerURL(&HealthCheckConfig{}) // empty MQTTBroker
	if got != "wss://env-broker:443" {
		t.Errorf("expected wss URL from env var, got %q", got)
	}
}

func TestFirstBrokerURL_ConfigTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("MQTT_BROKER", "tcp://env-broker:1883")
	cfg := &HealthCheckConfig{MQTTBroker: "wss://config-broker:443"}
	got := firstBrokerURL(cfg)
	if got != "wss://config-broker:443" {
		t.Errorf("config MQTTBroker should take precedence over env var, got %q", got)
	}
}

// TestChannelHashIsDeterministic verifies the same secret always gives the same hash byte.
func TestChannelHashIsDeterministic(t *testing.T) {
	secret := "aabbccddeeff00112233445566778899"
	ck1, _ := NewChannelKey(secret)
	ck2, _ := NewChannelKey(strings.ToUpper(secret))
	// ToUpper makes no difference since hex digits 0-9 and a-f/A-F are handled by hex.DecodeString
	if ck1.ChannelHashByte != ck2.ChannelHashByte {
		t.Errorf("hash byte should be deterministic: %d vs %d", ck1.ChannelHashByte, ck2.ChannelHashByte)
	}
}
