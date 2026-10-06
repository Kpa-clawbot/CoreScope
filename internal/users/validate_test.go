package users

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		"  Alice@Example.ORG ":   "alice@example.org",
		"a.b+tag@sub.example.be": "a.b+tag@sub.example.be",
	}
	for in, want := range good {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "alice", "alice@", "@example.org", "alice@localhost", "Alice <alice@example.org>",
		"alice@example.org.", strings.Repeat("a", 250) + "@example.org"}
	for _, in := range bad {
		if _, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted", in)
		} else {
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("NormalizeEmail(%q) error is %T, want *ValidationError", in, err)
			}
		}
	}
}

func TestValidateDisplayName(t *testing.T) {
	if got, err := ValidateDisplayName("  ON8AR Erwin  "); err != nil || got != "ON8AR Erwin" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ValidateDisplayName("\U0001F469\u200d\U0001F4BB dev"); err != nil { // ZWJ emoji sequence is allowed
		t.Fatalf("ZWJ sequence rejected: %v", err)
	}
	bad := []string{"a", strings.Repeat("x", 33), "evil\u202eeman", "tab\tname", "zero\u200bwidth", "line\u2028sep"}
	for _, in := range bad {
		if _, err := ValidateDisplayName(in); err == nil {
			t.Errorf("ValidateDisplayName(%q) accepted", in)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("correct horse"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	for _, in := range []string{"short", strings.Repeat("p", 129)} {
		if err := ValidatePassword(in); err == nil {
			t.Errorf("ValidatePassword(len %d) accepted", len(in))
		}
	}
}
