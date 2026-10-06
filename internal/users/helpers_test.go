package users

import (
	"path/filepath"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestStore opens a fresh users.db in a temp dir with a controllable clock.
func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	t.Cleanup(func() { st.Close() })
	return st, clk
}
