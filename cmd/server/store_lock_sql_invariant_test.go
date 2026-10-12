package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// #2146: no SQL while PacketStore.mu is held. The server's read pool has 4
// connections; a store-lock holder that waits for one stalls the ingest
// writer, and behind a pending writer every other reader of the store.
//
// This test parses the package and fails when code inside a store.mu
// section calls, directly or through other package functions, a function
// that queries SQLite. Calls are resolved by name only (no type checking),
// which is enough for this package's receiver conventions.

// sqlUnderLockAllowedCallees query SQLite only on a cold or invalidated
// cache; past their TTL they serve the cached value and refresh in the
// background (cache_refresh.go).
var sqlUnderLockAllowedCallees = map[string]bool{
	"PacketStore.getCachedNodesAndPM":    true,
	"PacketStore.resolveRegionObservers": true,
	"PacketStore.resolveAreaNodes":       true,
}

// sqlUnderLockAllowedHolders run at startup, before or while the store is
// first filled. Load and scanAndMergeChunk already release the lock around
// their evidence query; the scan cannot see an Unlock in a nested block.
var sqlUnderLockAllowedHolders = map[string]bool{
	"PacketStore.Load":              true,
	"PacketStore.LoadChunked":       true,
	"PacketStore.loadChunk":         true,
	"PacketStore.scanAndMergeChunk": true,
}

func TestNoSQLUnderStoreLock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	srcs := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs[f] = string(b)
	}
	hits, regions := findSQLUnderStoreLock(t, srcs)
	// Guard against a scan that silently stops matching anything.
	if regions < 40 {
		t.Fatalf("found only %d store.mu sections; the scan no longer recognises the locking code", regions)
	}
	for _, h := range hits {
		t.Errorf("SQL under store.mu: %s", h)
	}
}

// The scan itself must catch a direct and an indirect query under the lock,
// and ignore one after the unlock.
func TestNoSQLUnderStoreLockDetects(t *testing.T) {
	src := `package main
type PacketStore struct{}
type DB struct{}
func (db *DB) GetThing() {}
func (s *PacketStore) helper() { s.db.GetThing() }
func (s *PacketStore) Direct() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.db.conn.Query("SELECT 1")
}
func (s *PacketStore) Indirect() {
	s.mu.RLock()
	s.helper()
	s.mu.RUnlock()
}
func (s *PacketStore) After() {
	s.mu.RLock()
	x := 1
	s.mu.RUnlock()
	s.helper()
	_ = x
}
`
	hits, _ := findSQLUnderStoreLock(t, map[string]string{"x.go": src})
	got := strings.Join(hits, "\n")
	for _, want := range []string{"PacketStore.Direct", "PacketStore.Indirect calls PacketStore.helper"} {
		if !strings.Contains(got, want) {
			t.Errorf("scan missed %q; hits:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PacketStore.After") {
		t.Errorf("scan flagged a query after the unlock; hits:\n%s", got)
	}
}

var sqlMethodNames = map[string]bool{
	"Query": true, "QueryRow": true, "QueryContext": true, "QueryRowContext": true,
	"Exec": true, "ExecContext": true, "Prepare": true, "Begin": true, "BeginTx": true,
}

func lockScanExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return lockScanExpr(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return lockScanExpr(x.Fun) + "()"
	}
	return "?"
}

func lockScanRecv(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// findSQLUnderStoreLock returns one line per call made inside a store.mu
// section that reaches SQL, and the number of sections it found.
func findSQLUnderStoreLock(t *testing.T, srcs map[string]string) (hits []string, regions int) {
	t.Helper()
	fset := token.NewFileSet()
	type fn struct {
		decl *ast.FuncDecl
		recv string
	}
	funcs := map[string]fn{}
	for name, src := range srcs {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			recv := lockScanRecv(fd)
			funcs[recv+"."+fd.Name.Name] = fn{fd, recv}
		}
	}
	resolve := func(c *ast.CallExpr, recv string) string {
		switch f := c.Fun.(type) {
		case *ast.Ident:
			if _, ok := funcs["."+f.Name]; ok {
				return "." + f.Name
			}
		case *ast.SelectorExpr:
			x := lockScanExpr(f.X)
			var typ string
			switch {
			case x == "s" && recv != "":
				typ = recv
			case strings.HasSuffix(x, "store"):
				typ = "PacketStore"
			case strings.HasSuffix(x, "db"):
				typ = "DB"
			}
			if _, ok := funcs[typ+"."+f.Sel.Name]; ok {
				return typ + "." + f.Sel.Name
			}
		}
		return ""
	}

	// via[k] is the next step from k towards a function that runs SQL.
	via := map[string]string{}
	edges := map[string][]string{}
	for k, f := range funcs {
		if f.recv == "DB" {
			via[k] = k
		}
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if se, ok := c.Fun.(*ast.SelectorExpr); ok && sqlMethodNames[se.Sel.Name] && strings.HasSuffix(lockScanExpr(se.X), "conn") {
				via[k] = k
			}
			if callee := resolve(c, f.recv); callee != "" {
				edges[k] = append(edges[k], callee)
			}
			return true
		})
	}
	for k := range sqlUnderLockAllowedCallees {
		delete(via, k)
	}
	for changed := true; changed; {
		changed = false
		for k, es := range edges {
			if _, ok := via[k]; ok || sqlUnderLockAllowedCallees[k] {
				continue
			}
			for _, e := range es {
				if _, ok := via[e]; ok {
					via[k] = e
					changed = true
					break
				}
			}
		}
	}
	chain := func(k string) string {
		out := []string{k}
		for i := 0; i < 10 && via[k] != k; i++ {
			k = via[k]
			out = append(out, k)
		}
		return strings.Join(out, " -> ")
	}

	isStoreMu := func(x ast.Expr, recv string) bool {
		s := lockScanExpr(x)
		return s == "s.store.mu" || s == "store.mu" || (s == "s.mu" && recv == "PacketStore")
	}
	isUnlock := func(st ast.Stmt) bool {
		es, ok := st.(*ast.ExprStmt)
		if !ok {
			return false
		}
		c, ok := es.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		s := lockScanExpr(c.Fun)
		return strings.HasSuffix(s, "mu.RUnlock") || strings.HasSuffix(s, "mu.Unlock")
	}

	for k, f := range funcs {
		if sqlUnderLockAllowedHolders[k] {
			continue
		}
		var spans [][2]token.Pos
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			b, ok := n.(*ast.BlockStmt)
			if !ok {
				return true
			}
			for i, st := range b.List {
				es, ok := st.(*ast.ExprStmt)
				if !ok {
					continue
				}
				c, ok := es.X.(*ast.CallExpr)
				if !ok {
					continue
				}
				se, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || (se.Sel.Name != "RLock" && se.Sel.Name != "Lock") || !isStoreMu(se.X, f.recv) {
					continue
				}
				end := b.End() // a deferred unlock holds it to the end of the block
				for _, st2 := range b.List[i+1:] {
					if isUnlock(st2) {
						end = st2.Pos()
						break
					}
				}
				spans = append(spans, [2]token.Pos{c.Pos(), end})
			}
			return true
		})
		regions += len(spans)
		ast.Inspect(f.decl.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			inside := false
			for _, sp := range spans {
				if c.Pos() > sp[0] && c.Pos() < sp[1] {
					inside = true
				}
			}
			if !inside {
				return true
			}
			if se, ok := c.Fun.(*ast.SelectorExpr); ok && sqlMethodNames[se.Sel.Name] && strings.HasSuffix(lockScanExpr(se.X), "conn") {
				hits = append(hits, fset.Position(c.Pos()).String()+"  "+k+" queries directly")
				return true
			}
			if callee := resolve(c, f.recv); callee != "" {
				if _, ok := via[callee]; ok {
					hits = append(hits, fset.Position(c.Pos()).String()+"  "+k+" calls "+chain(callee))
				}
			}
			return true
		})
	}
	sort.Strings(hits)
	return hits, regions
}
