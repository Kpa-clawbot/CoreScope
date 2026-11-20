package dbx

import (
	"fmt"
	"strings"
	"testing"
)

func TestRebind(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", ``, ``},
		{"no placeholders", `SELECT 1`, `SELECT 1`},
		{"single", `SELECT * FROM t WHERE a = ?`, `SELECT * FROM t WHERE a = $1`},
		{"several", `INSERT INTO t (a,b,c) VALUES (?,?,?)`, `INSERT INTO t (a,b,c) VALUES ($1,$2,$3)`},
		{"at start and end", `?+?`, `$1+$2`},
		{"only placeholder", `?`, `$1`},
		{"double question mark", `SELECT ??`, `SELECT $1$2`},
		{"ten or more", strings.Repeat("?,", 10) + "?", "$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11"},
		{"already dollar", `SELECT $1, $2 FROM t`, `SELECT $1, $2 FROM t`},

		{"single-quoted", `SELECT 'what?' , ?`, `SELECT 'what?' , $1`},
		{"single-quoted doubled quote", `SELECT 'it''s ?', ?`, `SELECT 'it''s ?', $1`},
		{"single-quoted only", `SELECT '?'`, `SELECT '?'`},
		{"adjacent literals", `SELECT '?''?' || ?`, `SELECT '?''?' || $1`},
		{"escape string backslash quote", `SELECT E'a\'?', ?`, `SELECT E'a\'?', $1`},
		{"lowercase escape string", `SELECT e'\\', ?`, `SELECT e'\\', $1`},
		{"backslash not escape in plain string", `SELECT 'a\', ?`, `SELECT 'a\', $1`},
		{"identifier ending in e is not escape string", `SELECT name'x' , ?`, `SELECT name'x' , $1`},

		{"double-quoted identifier", `SELECT "col?" FROM t WHERE x = ?`, `SELECT "col?" FROM t WHERE x = $1`},
		{"double-quoted doubled quote", `SELECT "a""?" , ?`, `SELECT "a""?" , $1`},

		{"dollar-quoted", `SELECT $$a ? b$$, ?`, `SELECT $$a ? b$$, $1`},
		{"tagged dollar-quoted", `DO $fn$ SELECT ?; $$ ? $$ $fn$; SELECT ?`, `DO $fn$ SELECT ?; $$ ? $$ $fn$; SELECT $1`},
		{"tag with digits and underscore", `SELECT $t_1$?$t_1$, ?`, `SELECT $t_1$?$t_1$, $1`},
		{"dollar inside identifier", `SELECT a$b$ FROM t WHERE x = ? AND y = $c$?$c$`, `SELECT a$b$ FROM t WHERE x = $1 AND y = $c$?$c$`},
		// Only ? is counted; mixing ? with $n is unsupported.
		{"positional param is not a quote", `SELECT $1 || ?`, `SELECT $1 || $1`},

		{"line comment", "SELECT ? -- why?\n, ?", "SELECT $1 -- why?\n, $2"},
		{"line comment at end", "SELECT ? -- trailing ?", "SELECT $1 -- trailing ?"},
		{"block comment", `SELECT /* a ? b */ ?`, `SELECT /* a ? b */ $1`},
		{"nested block comment", `SELECT /* a /* ? */ ? */ ?`, `SELECT /* a /* ? */ ? */ $1`},
		{"single dash is minus", `SELECT ?-?`, `SELECT $1-$2`},
		{"slash is divide", `SELECT ?/?`, `SELECT $1/$2`},

		{"unicode", `SELECT 'héllo ?', 名前 FROM t WHERE ñ = ? AND "é?" = ?`, `SELECT 'héllo ?', 名前 FROM t WHERE ñ = $1 AND "é?" = $2`},
		{"unicode dollar tag", `SELECT $é$?$é$, ?`, `SELECT $é$?$é$, $1`},

		{"unterminated string", `SELECT ?, 'oops ?`, `SELECT $1, 'oops ?`},
		{"unterminated dollar", `SELECT ?, $$ ?`, `SELECT $1, $$ ?`},
		{"unterminated block comment", `SELECT ? /* ?`, `SELECT $1 /* ?`},
		{"lone dollar at end", `SELECT ? $`, `SELECT $1 $`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rebind(tc.in); got != tc.want {
				t.Errorf("rebind(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			// Twice: the first call fills the cache, the second reads it.
			for i := 0; i < 2; i++ {
				if got := Rebind(tc.in); got != tc.want {
					t.Errorf("Rebind(%q) call %d\n got %q\nwant %q", tc.in, i, got, tc.want)
				}
			}
		})
	}
}

func TestRebindNoPlaceholderSkipsCache(t *testing.T) {
	resetRebindCache()
	Rebind(`SELECT 1`)
	if n := cacheLen(); n != 0 {
		t.Fatalf("cache has %d entries after a query without ?, want 0", n)
	}
}

func TestRebindCacheBoundedByEntries(t *testing.T) {
	resetRebindCache()
	for i := 0; i < rebindCacheMaxEntries*3; i++ {
		q := fmt.Sprintf("SELECT %d WHERE x = ?", i)
		if got, want := Rebind(q), fmt.Sprintf("SELECT %d WHERE x = $1", i); got != want {
			t.Fatalf("Rebind = %q, want %q", got, want)
		}
		if n := cacheLen(); n > rebindCacheMaxEntries {
			t.Fatalf("cache grew to %d entries, max %d", n, rebindCacheMaxEntries)
		}
	}
}

func TestRebindCacheBoundedByBytes(t *testing.T) {
	resetRebindCache()
	big := "SELECT ? " + strings.Repeat("x", 256<<10)
	for i := 0; i < 64; i++ {
		Rebind(fmt.Sprintf("%s%d", big, i))
		rebindCache.RLock()
		b := rebindCache.bytes
		rebindCache.RUnlock()
		if b > rebindCacheMaxBytes {
			t.Fatalf("cache holds %d bytes, max %d", b, rebindCacheMaxBytes)
		}
	}
	// A single query bigger than the whole budget is rewritten but not cached.
	resetRebindCache()
	huge := "SELECT ? " + strings.Repeat("y", rebindCacheMaxBytes)
	if got := Rebind(huge); !strings.HasPrefix(got, "SELECT $1 ") {
		t.Fatalf("huge query not rewritten: %q...", got[:20])
	}
	if n := cacheLen(); n != 0 {
		t.Fatalf("oversized query was cached (%d entries)", n)
	}
}

func TestRebindConcurrent(t *testing.T) {
	resetRebindCache()
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 2000; i++ {
				q := fmt.Sprintf("SELECT ? -- %d", (g*2000+i)%5000)
				if got := Rebind(q); !strings.HasPrefix(got, "SELECT $1 -- ") {
					t.Errorf("Rebind(%q) = %q", q, got)
					return
				}
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
}

func resetRebindCache() {
	rebindCache.Lock()
	clear(rebindCache.m)
	rebindCache.bytes = 0
	rebindCache.Unlock()
}

func cacheLen() int {
	rebindCache.RLock()
	defer rebindCache.RUnlock()
	return len(rebindCache.m)
}

// benchQuery is a realistic ~300-char CoreScope-style query with 6 placeholders.
const benchQuery = `SELECT o.id, o.observer_id, o.snr, o.rssi, o.path_json, o.timestamp, t.hash, t.payload_type
FROM observations o JOIN transmissions t ON t.id = o.transmission_id
WHERE o.observer_id = ? AND o.timestamp >= ? AND o.timestamp < ? AND t.payload_type IN (?, ?)
  AND o.snr > ? ORDER BY o.timestamp DESC LIMIT 500`

func BenchmarkRebind(b *testing.B) {
	if got := strings.Count(rebind(benchQuery), "$"); got != 6 {
		b.Fatalf("bench query has %d placeholders, want 6", got)
	}
	b.Run("cached", func(b *testing.B) {
		Rebind(benchQuery)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Rebind(benchQuery)
		}
	})
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rebind(benchQuery)
		}
	})
}
