package users

import (
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidationError is a user-facing input problem; Msg is safe to show.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// NormalizeEmail trims and lowercases an address and checks it is a bare
// addr-spec with a dotted domain. The result is the stored identity.
func NormalizeEmail(raw string) (string, error) {
	invalid := &ValidationError{Msg: "enter a valid email address"}
	e := strings.ToLower(strings.TrimSpace(raw))
	if e == "" || len(e) > 254 {
		return "", invalid
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Name != "" || addr.Address != e {
		return "", invalid
	}
	at := strings.LastIndexByte(e, '@')
	domain := e[at+1:]
	if at < 1 || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", invalid
	}
	return e, nil
}

// ValidateDisplayName trims a display name and enforces 2–32 characters
// without control, format (bidi, zero-width) or line/paragraph separator
// characters. ZWJ (U+200D) is allowed so emoji sequences survive.
func ValidateDisplayName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if !utf8.ValidString(n) {
		return "", &ValidationError{Msg: "display name is not valid text"}
	}
	if c := utf8.RuneCountInString(n); c < 2 || c > 32 {
		return "", &ValidationError{Msg: "display name must be 2 to 32 characters"}
	}
	for _, r := range n {
		if r == '\u200d' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return "", &ValidationError{Msg: "display name contains invisible or control characters"}
		}
	}
	return n, nil
}

// ValidatePassword enforces length only (10–128 characters), per NIST 800-63B.
func ValidatePassword(p string) error {
	if c := utf8.RuneCountInString(p); c < 10 || c > 128 {
		return &ValidationError{Msg: "password must be 10 to 128 characters"}
	}
	return nil
}
