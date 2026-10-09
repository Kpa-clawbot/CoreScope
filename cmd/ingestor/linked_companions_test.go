package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The columns of companion_links in cmd/server's users.db (internal/users
// schema v6); the ingestor reads pubkey only.
const testCompanionLinksDDL = `CREATE TABLE IF NOT EXISTS companion_links (
	pubkey TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	linked_at INTEGER NOT NULL
)`

// testUnlinkedPK is never written to companion_links.
const testUnlinkedPK = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"

// linkClock is a settable clock for the 5 s miss gap.
type linkClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *linkClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *linkClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// setLinks replaces the companion_links rows of the users.db at path with pks
// (creating the file and table when needed), as the server would.
func setLinks(t *testing.T, path string, pks ...string) {
	t.Helper()
	stmts := []string{testCompanionLinksDDL, `DELETE FROM companion_links`}
	for i, pk := range pks {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES ('%s', 1, %d)`, pk, i))
	}
	execUsersDB(t, path, stmts...)
}

// newTestLinkedSet returns a set over a fresh users.db holding pks, on a
// frozen clock. It is not read yet.
func newTestLinkedSet(t *testing.T, pks ...string) (*linkedCompanionSet, string, *linkClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.db")
	setLinks(t, path, pks...)
	clk := &linkClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	s := newLinkedCompanionSet(path)
	s.now = clk.now
	t.Cleanup(s.Close)
	return s, path, clk
}

func TestLinkedCompanionsTableIsInUsersSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "users", "schema.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "CREATE TABLE companion_links (") {
		t.Fatalf("internal/users/schema.go no longer creates companion_links; %q reads it", linkedCompanionsQuery)
	}
}

func TestLinkedCompanionSetAllowsLinkedOnly(t *testing.T) {
	s, _, _ := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	if !s.Allow(testCompanionPK) || !s.Allow(strings.ToUpper(testCompanionPK)) {
		t.Fatal("linked pubkey refused")
	}
	if s.Allow(testUnlinkedPK) {
		t.Fatal("unlinked pubkey allowed")
	}
}

func TestLinkedCompanionSetMissRefreshAcceptsNewLink(t *testing.T) {
	s, path, clk := newTestLinkedSet(t)
	s.refresh()
	setLinks(t, path, testCompanionPK) // linked after the last refresh
	clk.add(linkedCompanionsMissGap - time.Second)
	if s.Allow(testCompanionPK) {
		t.Fatal("re-read inside the 5 s gap")
	}
	clk.add(time.Second)
	if !s.Allow(testCompanionPK) {
		t.Fatal("a miss after the gap did not pick up the new link")
	}
}

func TestLinkedCompanionSetMissRefreshCapHoldsUnderFlood(t *testing.T) {
	s, _, clk := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	base := s.readCount()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				s.Allow(fmt.Sprintf("%02x%062x", g, i))
			}
		}(g)
	}
	wg.Wait()
	if n := s.readCount() - base; n != 0 {
		t.Fatalf("%d re-reads inside the gap; want 0", n)
	}
	clk.add(linkedCompanionsMissGap)
	for i := 0; i < 1000; i++ {
		s.Allow(fmt.Sprintf("ff%062x", i))
	}
	if n := s.readCount() - base; n != 1 {
		t.Fatalf("%d re-reads for a flood after the gap; want 1", n)
	}
	if !s.Allow(testCompanionPK) {
		t.Fatal("the flood dropped a linked pubkey")
	}
}

func TestLinkedCompanionSetUnlinkHonouredAfterPeriodicRefresh(t *testing.T) {
	s, path, clk := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	setLinks(t, path) // unlinked on the server
	clk.add(30 * time.Second)
	if !s.Allow(testCompanionPK) {
		t.Fatal("a hit must not re-read; the unlink waits for the periodic refresh")
	}
	s.refresh()
	if s.Allow(testCompanionPK) {
		t.Fatal("unlink not honoured after the periodic refresh")
	}
}

func TestLinkedCompanionSetMissingTableIsEmptyAndWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path) // an older users.db: proposals, no companion_links
	s := newLinkedCompanionSet(path)
	defer s.Close()
	s.refresh()
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("dropped before the list was ever read; want pass-through")
	}
	if n := strings.Count(buf.String(), "no companion_links table"); n != 1 {
		t.Fatalf("warned %d times; want once:\n%s", n, buf.String())
	}
	setLinks(t, path, testCompanionPK) // the server was upgraded
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("the table appearing later is not picked up")
	}
	if s.Allow(testUnlinkedPK) {
		t.Fatal("an unlinked pubkey passed once the list was read")
	}
}

func TestLinkedCompanionSetMissingFileLogsOnceAndRecovers(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	path := filepath.Join(t.TempDir(), "users.db")
	s := newLinkedCompanionSet(path)
	defer s.Close()
	s.refresh()
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("dropped before users.db was ever read; want pass-through")
	}
	if n := strings.Count(buf.String(), "linked companions unavailable"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the ingestor created users.db")
	}
	setLinks(t, path, testCompanionPK)
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("users.db appearing later is not picked up")
	}
	if !strings.Contains(buf.String(), "linked companions readable again") {
		t.Fatalf("no recovery line:\n%s", buf.String())
	}
}

// Pass-through holds only until the first successful read: after that a
// failed read keeps the last good set, so it fails closed.
func TestLinkedCompanionSetFailsClosedOnlyAfterFirstRead(t *testing.T) {
	s, path, clock := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	if s.Allow(testUnlinkedPK) {
		t.Fatal("an unlinked pubkey passed after a successful read")
	}
	s.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clock.add(linkedCompanionsMissGap)
	s.refresh() // fails: users.db is gone
	if !s.Allow(testCompanionPK) {
		t.Fatal("the last good set was dropped on a failed read")
	}
	clock.add(linkedCompanionsMissGap)
	if s.Allow(testUnlinkedPK) {
		t.Fatal("a failed read after the first success let an unlinked pubkey through")
	}
}

func TestLinkedCompanionSetIsReadOnly(t *testing.T) {
	s, path, _ := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	_, err := s.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES ('x', 1, 1)`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("write through the ingestor's users.db handle: err = %v; want a read-only refusal", err)
	}
	if !strings.Contains(usersDBReadOnlyDSN(path), "mode=ro") {
		t.Fatal("DSN without mode=ro")
	}
}
