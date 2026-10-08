package users

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestNormalizePubkey(t *testing.T) {
	pk := strings.Repeat("ab", 32)
	if got, err := NormalizePubkey("  " + strings.ToUpper(pk) + " "); err != nil || got != pk {
		t.Fatalf("NormalizePubkey = %q, %v", got, err)
	}
	for _, bad := range []string{"", pk[:62], pk + "00", strings.Repeat("zz", 32)} {
		if _, err := NormalizePubkey(bad); !errors.Is(err, ErrBadPubkey) {
			t.Fatalf("NormalizePubkey(%q) err = %v", bad, err)
		}
	}
}

func TestLinkChallengeLifecycle(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	pk := strings.Repeat("ab", 32)
	ch, exp, err := st.CreateLinkChallenge(a.ID, strings.ToUpper(pk))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(ch)
	if err != nil || len(raw) != 32 || ch != strings.ToLower(ch) || !exp.Equal(clk.Now().Add(LinkChallengeTTL)) {
		t.Fatalf("CreateLinkChallenge = %q, %v, %v", ch, exp, err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM link_challenges WHERE challenge_hash = ? AND pubkey = ? AND user_id = ?`,
		challengeHash(raw), pk, a.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored challenge rows = %d, %v (want SHA-256 of the raw bytes)", n, err)
	}
	if _, _, err := st.CreateLinkChallenge(a.ID, "nothex"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad pubkey err = %v", err)
	}

	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("second consume err = %v, want single use", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, "zz"); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("malformed challenge err = %v", err)
	}
}

func TestLinkChallengeBindingAndExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ab", 32)
	otherPk := strings.Repeat("cd", 32)

	ch, _, _ := st.CreateLinkChallenge(a.ID, pk)
	if err := st.ConsumeLinkChallenge(b.ID, pk, ch); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("other user err = %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("mismatch did not consume: %v", err)
	}

	ch, _, _ = st.CreateLinkChallenge(a.ID, pk)
	if err := st.ConsumeLinkChallenge(a.ID, otherPk, ch); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("other pubkey err = %v", err)
	}

	ch, _, _ = st.CreateLinkChallenge(a.ID, pk)
	clk.Advance(LinkChallengeTTL)
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("expired err = %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("expired challenge not consumed: %v", err)
	}
}
