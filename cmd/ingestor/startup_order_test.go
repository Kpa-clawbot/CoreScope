package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The hop index is configured in main's startup block: after the ingest buffer
// and the MQTT connections are up (ingestBuffer.Start, client.Connect; the
// subscription follows the connect) and before the ingest gate opens
// (ingestBuffer.Ready). Removing the index holds the single writer for about a
// minute on a large database; run before the subscription, that minute's
// packets are lost instead of buffered (#2214 review, round 2).
//
// main has no seam for a behaviour test, so this pins the call order in its
// source. It is a guard, not the proof: the behaviour was checked against a
// broker with the built binaries.
func TestMainConfiguresHopIndexBetweenSubscribeAndGate(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var mainFn *ast.FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
			mainFn = fn
		}
	}
	if mainFn == nil {
		t.Fatal("func main not found in main.go")
	}
	order := []string{"ingestBuffer.Start", "client.Connect", "store.ConfigureObservationHops", "ingestBuffer.Ready"}
	pos := map[string]token.Pos{}
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		key := recv.Name + "." + sel.Sel.Name
		for _, want := range order {
			if key == want {
				if _, dup := pos[key]; dup {
					t.Fatalf("%s is called more than once in main; this test assumes one call site", key)
				}
				pos[key] = call.Pos()
			}
		}
		return true
	})
	for i, key := range order {
		if pos[key] == token.NoPos {
			t.Fatalf("%s not found in main", key)
		}
		if i > 0 && pos[key] <= pos[order[i-1]] {
			t.Fatalf("%s (line %d) must come after %s (line %d) in main",
				key, fset.Position(pos[key]).Line, order[i-1], fset.Position(pos[order[i-1]]).Line)
		}
	}
}
