package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestJanitorPrunesExpiredLinkChallenges(t *testing.T) {
	f := newAuthFixture(t)
	dave := f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	pk := strings.Repeat("cd", 32)
	f.st.SetClock(func() time.Time { return time.Now().Add(-10 * time.Minute) })
	ch, _, err := f.st.CreateLinkChallenge(dave.me.ID, pk)
	if err != nil {
		t.Fatal(err)
	}
	f.st.SetClock(time.Now)
	f.srv.auth.prune()
	// Pruned → missing. Without the janitor it would still be there, expired.
	if err := f.st.ConsumeLinkChallenge(dave.me.ID, pk, ch); !errors.Is(err, users.ErrChallengeMissing) {
		t.Fatalf("expired challenge survived the janitor: %v", err)
	}
}
