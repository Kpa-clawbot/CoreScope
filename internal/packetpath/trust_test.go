package packetpath

import "testing"

func TestMeetsPathTrust_DefaultThreshold(t *testing.T) {
	// nil cfg → DefaultMinHashBytesForMapping (2): 1-byte excluded, 2/3-byte trusted.
	cases := []struct {
		prefixBytes int
		want        bool
	}{
		{0, false}, // legacy/unknown bucket
		{1, false}, // 1-byte prefix — excluded by default
		{2, true},
		{3, true},
	}
	for _, c := range cases {
		if got := MeetsPathTrust(c.prefixBytes, nil); got != c.want {
			t.Errorf("MeetsPathTrust(%d, nil) = %v, want %v", c.prefixBytes, got, c.want)
		}
	}
}

func TestMeetsPathTrust_TrustAllOptIn(t *testing.T) {
	// Explicit minHashBytesForMapping: 1 restores pre-#1784 trust-all behavior,
	// including the legacy bucket-0 (unknown-length) observations.
	cfg := &TrustConfig{MinHashBytesForMapping: 1}
	for _, prefixBytes := range []int{0, 1, 2, 3} {
		if !MeetsPathTrust(prefixBytes, cfg) {
			t.Errorf("MeetsPathTrust(%d, minBytes=1) = false, want true", prefixBytes)
		}
	}
}

func TestMeetsPathTrust_ZeroValueOptIn(t *testing.T) {
	// A zero-value MinHashBytesForMapping (unset in JSON) must NOT behave
	// like an explicit "1" opt-in — it should fall back to the default (2).
	cfg := &TrustConfig{}
	if MeetsPathTrust(1, cfg) {
		t.Errorf("MeetsPathTrust(1, %+v) = true, want false (unset should use default, not opt-in to trust-all)", cfg)
	}
	if !MeetsPathTrust(2, cfg) {
		t.Errorf("MeetsPathTrust(2, %+v) = false, want true", cfg)
	}
}

func TestMeetsPathTrust_ConservativeThreshold3(t *testing.T) {
	cfg := &TrustConfig{MinHashBytesForMapping: 3}
	cases := []struct {
		prefixBytes int
		want        bool
	}{
		{0, false},
		{1, false},
		{2, false},
		{3, true},
	}
	for _, c := range cases {
		if got := MeetsPathTrust(c.prefixBytes, cfg); got != c.want {
			t.Errorf("MeetsPathTrust(%d, minBytes=3) = %v, want %v", c.prefixBytes, got, c.want)
		}
	}
}

func TestMinHashBytesOrDefault(t *testing.T) {
	var nilCfg *TrustConfig
	if got := nilCfg.MinHashBytesOrDefault(); got != DefaultMinHashBytesForMapping {
		t.Errorf("nil.MinHashBytesOrDefault() = %d, want %d", got, DefaultMinHashBytesForMapping)
	}
	unset := &TrustConfig{}
	if got := unset.MinHashBytesOrDefault(); got != DefaultMinHashBytesForMapping {
		t.Errorf("unset.MinHashBytesOrDefault() = %d, want %d", got, DefaultMinHashBytesForMapping)
	}
	explicit := &TrustConfig{MinHashBytesForMapping: 1}
	if got := explicit.MinHashBytesOrDefault(); got != 1 {
		t.Errorf("explicit(1).MinHashBytesOrDefault() = %d, want 1", got)
	}
	negative := &TrustConfig{MinHashBytesForMapping: -5}
	if got := negative.MinHashBytesOrDefault(); got != DefaultMinHashBytesForMapping {
		t.Errorf("negative.MinHashBytesOrDefault() = %d, want %d (defensive fallback)", got, DefaultMinHashBytesForMapping)
	}
}

func TestMinHashBytesOrDefault_ClampsAboveMax(t *testing.T) {
	// A misconfigured threshold above MaxHashBytes (e.g. a typo like 99)
	// must clamp to MaxHashBytes rather than silently excluding every
	// observation (no real prefix is ever longer than 3 bytes).
	cases := []int{4, 5, 99}
	for _, v := range cases {
		cfg := &TrustConfig{MinHashBytesForMapping: v}
		if got := cfg.MinHashBytesOrDefault(); got != MaxHashBytes {
			t.Errorf("MinHashBytesForMapping: %d -> MinHashBytesOrDefault() = %d, want %d (clamped)", v, got, MaxHashBytes)
		}
	}
}

func TestMinHashBytesOrDefault_ExactlyMax(t *testing.T) {
	cfg := &TrustConfig{MinHashBytesForMapping: MaxHashBytes}
	if got := cfg.MinHashBytesOrDefault(); got != MaxHashBytes {
		t.Errorf("MinHashBytesOrDefault() = %d, want %d (exactly max, no clamp needed)", got, MaxHashBytes)
	}
}

func TestMeetsPathTrust_ClampedThresholdOnlyTrustsMaxBytes(t *testing.T) {
	// minHashBytesForMapping: 99 clamps to 3 — only 3-byte prefixes trusted.
	cfg := &TrustConfig{MinHashBytesForMapping: 99}
	cases := []struct {
		prefixBytes int
		want        bool
	}{
		{0, false},
		{1, false},
		{2, false},
		{3, true},
	}
	for _, c := range cases {
		if got := MeetsPathTrust(c.prefixBytes, cfg); got != c.want {
			t.Errorf("MeetsPathTrust(%d, minBytes=99->clamped) = %v, want %v", c.prefixBytes, got, c.want)
		}
	}
}
