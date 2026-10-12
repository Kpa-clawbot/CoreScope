package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampObserverField(t *testing.T) {
	if got := clampObserverField("Observer One", 128); got != "Observer One" {
		t.Fatalf("short value changed: %q", got)
	}
	long := strings.Repeat("x", 5000)
	if got := clampObserverField(long, 128); utf8.RuneCountInString(got) != 128 {
		t.Fatalf("long value not truncated to 128 runes: %d", utf8.RuneCountInString(got))
	}
	// Truncation counts runes, not bytes, so a multi-byte name is not cut mid-character.
	multi := strings.Repeat("é", 200)
	got := clampObserverField(multi, 128)
	if utf8.RuneCountInString(got) != 128 || !utf8.ValidString(got) {
		t.Fatalf("multi-byte truncation broke the string: %d runes valid=%v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if got := clampObserverField("a\x00b\x1fc", 128); got != "abc" {
		t.Fatalf("control characters not stripped: %q", got)
	}
}

func TestExtractObserverMetaCapsStrings(t *testing.T) {
	long := strings.Repeat("m", 1000)
	meta := extractObserverMeta(map[string]interface{}{
		"model":          long,
		"firmware":       long,
		"client_version": long,
		"radio":          long,
	})
	if meta == nil {
		t.Fatal("meta nil")
	}
	for name, v := range map[string]*string{"model": meta.Model, "firmware": meta.Firmware, "client_version": meta.ClientVersion, "radio": meta.Radio} {
		if v == nil {
			t.Fatalf("%s not set", name)
		}
		if n := utf8.RuneCountInString(*v); n != maxObserverTextLen {
			t.Fatalf("%s not capped: %d runes", name, n)
		}
	}
}
