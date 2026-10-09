package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func myNodesOf(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var d settingsDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(d.Keys[myNodesKey]), &items); err != nil {
		t.Fatalf("my nodes %q: %v", d.Keys[myNodesKey], err)
	}
	return items
}

func TestAddToMyNodesMerges(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	pk1, pk2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// No document yet: one is started.
	if got, err := a.addToMyNodes(uid, pk1, "Car", at); err != nil || got != myNodesAdded {
		t.Fatalf("first add = %q, %v", got, err)
	}
	raw, v, _ := f.st.GetSettings(uid)
	items := myNodesOf(t, raw)
	if v.Revision != 1 || len(items) != 1 || items[0]["pubkey"] != pk1 || items[0]["name"] != "Car" ||
		items[0]["addedAt"] != "2026-10-08T12:00:00.000Z" {
		t.Fatalf("after first add: rev %d, items %v", v.Revision, items)
	}

	// Existing items and other keys are kept; a pubkey already listed (any case) is not added twice.
	existing := `[{"pubkey":"` + strings.ToUpper(pk2) + `","name":"Home <rpt>","addedAt":"2026-01-01T00:00:00.000Z","extra":1}]`
	doc, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{"meshcore-theme": "dark", myNodesKey: existing}})
	v, err := f.st.PutSettings(uid, v, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.addToMyNodes(uid, pk2, "x", at); err != nil || got != myNodesPresent {
		t.Fatalf("present = %q, %v", got, err)
	}
	if _, v2, _ := f.st.GetSettings(uid); v2 != v {
		t.Fatalf("present changed the version: %+v -> %+v", v, v2)
	}
	if got, err := a.addToMyNodes(uid, pk1, "", at); err != nil || got != myNodesAdded {
		t.Fatalf("second add = %q, %v", got, err)
	}
	raw, v3, _ := f.st.GetSettings(uid)
	if v3.Revision != v.Revision+1 || v3.Generation != v.Generation {
		t.Fatalf("revision not bumped in place: %+v -> %+v", v, v3)
	}
	var d settingsDoc
	_ = json.Unmarshal([]byte(raw), &d)
	items = myNodesOf(t, raw)
	if d.Keys["meshcore-theme"] != "dark" || len(items) != 2 || items[0]["name"] != "Home <rpt>" ||
		items[0]["extra"] != float64(1) || items[1]["pubkey"] != pk1 || items[1]["name"] != pk1[:12] {
		t.Fatalf("merged doc keys=%v items=%v", d.Keys, items)
	}
	if strings.Contains(raw, `\u003c`) {
		t.Fatal("merge HTML-escaped an existing item")
	}
}

func TestAddToMyNodesFullAtCap(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	pk1, pk2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	build := func(n int) string {
		items := `[{"pubkey":"` + pk2 + `","name":"` + strings.Repeat("x", n) + `","addedAt":"2026-01-01T00:00:00.000Z"}]`
		d, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{myNodesKey: items}})
		return d
	}
	doc := build(settingsDocMaxBytes - 40 - len(build(0))) // 40 bytes below the cap: no room for an item
	if len(doc) != settingsDocMaxBytes-40 {
		t.Fatalf("fixture doc is %d bytes", len(doc))
	}
	v, err := f.st.PutSettings(uid, users.SettingsVersion{}, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.addToMyNodes(uid, pk1, "Car", time.Now()); err != nil || got != myNodesFull {
		t.Fatalf("at the cap = %q, %v", got, err)
	}
	if raw, v2, _ := f.st.GetSettings(uid); v2 != v || raw != doc {
		t.Fatal("document changed at the cap")
	}
}

func TestAddToMyNodesErrorIsNotFull(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	// A stored my-nodes value that is not a JSON array cannot be merged.
	doc, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{myNodesKey: `{"not":"a list"}`}})
	v, err := f.st.PutSettings(uid, users.SettingsVersion{}, doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.addToMyNodes(uid, strings.Repeat("a1", 32), "Car", time.Now())
	if err == nil || got == myNodesFull {
		t.Fatalf("bad list = %q, %v; want an error, not full", got, err)
	}
	if raw, v2, _ := f.st.GetSettings(uid); v2 != v || raw != doc {
		t.Fatal("document changed on a merge error")
	}
}
