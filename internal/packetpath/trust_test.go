package packetpath

import "testing"

func TestMeetsPathTrust_DefaultThreshold(t *testing.T) {
	// nil cfg → DefaultMinHashBytesForMapping (1): trust-all, backward-compatible.
	cases := []struct {
		prefixBytes int
		want        bool
	}{
		{0, true},
		{1, true},
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
	cfg := &TrustConfig{MinHashBytesForMapping: 1}
	for _, prefixBytes := range []int{0, 1, 2, 3} {
		if !MeetsPathTrust(prefixBytes, cfg) {
			t.Errorf("MeetsPathTrust(%d, minBytes=1) = false, want true", prefixBytes)
		}
	}
}

func TestMeetsPathTrust_Exclude1Byte(t *testing.T) {
	// minHashBytesForMapping: 2 — recommended for denser meshes.
	cfg := &TrustConfig{MinHashBytesForMapping: 2}
	cases := []struct {
		prefixBytes int
		want        bool
	}{
		{0, false},
		{1, false},
		{2, true},
		{3, true},
	}
	for _, c := range cases {
		if got := MeetsPathTrust(c.prefixBytes, cfg); got != c.want {
			t.Errorf("MeetsPathTrust(%d, minBytes=2) = %v, want %v", c.prefixBytes, got, c.want)
		}
	}
}

func TestMeetsPathTrust_ZeroValueOptIn(t *testing.T) {
	// A zero-value MinHashBytesForMapping (unset) must fall back to default (1),
	// so bucket-0 and 1-byte prefixes pass.
	cfg := &TrustConfig{}
	if !MeetsPathTrust(1, cfg) {
		t.Errorf("MeetsPathTrust(1, %+v) = false, want true (unset uses default=1, trust-all)", cfg)
	}
	if !MeetsPathTrust(0, cfg) {
		t.Errorf("MeetsPathTrust(0, %+v) = false, want true (bucket-0 passes at default threshold)", cfg)
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
	explicit2 := &TrustConfig{MinHashBytesForMapping: 2}
	if got := explicit2.MinHashBytesOrDefault(); got != 2 {
		t.Errorf("explicit(2).MinHashBytesOrDefault() = %d, want 2", got)
	}
	negative := &TrustConfig{MinHashBytesForMapping: -5}
	if got := negative.MinHashBytesOrDefault(); got != DefaultMinHashBytesForMapping {
		t.Errorf("negative.MinHashBytesOrDefault() = %d, want %d (defensive fallback)", got, DefaultMinHashBytesForMapping)
	}
}

func TestMinHashBytesOrDefault_ClampsAboveMax(t *testing.T) {
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
