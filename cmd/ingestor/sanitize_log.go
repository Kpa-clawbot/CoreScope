package main

import "strings"

// sanitizeLogString strips ASCII control bytes that would otherwise let a
// node-controlled string (advert name, observer origin, channel name) inject
// fake lines into the log stream. CR (\r), LF (\n), TAB (\t), NUL (\x00),
// any other byte < 0x20, and 0x7F (DEL) are replaced with '?'.
//
// This is intentionally narrower than sanitizeName: sanitizeName preserves
// \t and \n because they may appear in legitimately-stored display names.
// Log sinks want neither.
//
// See audit-input-vulns-20260603 (LOW — log injection via newline in advert
// name) and references at cmd/ingestor/main.go:659,689.
func sanitizeLogString(s string) string {
	if s == "" {
		return s
	}
	// Iterate over runes so multibyte UTF-8 (Cyrillic, emoji) is preserved.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
