// Package main — discovered channels (#688).
//
// When a decoded channel message text mentions a previously-unknown hashtag
// channel (e.g. "Hey, I created new channel called #mesh, please join"), we
// auto-register that hashtag so future traffic can be displayed. This file
// owns the parsing helper plus the integration glue exposed via GetChannels.
package main

import "regexp"

// hashtagRE matches MeshCore-style hashtag channel mentions inside free text.
// A valid channel name starts with '#', followed by one or more letters,
// digits, underscore, or dash. Trailing punctuation (.,!?:;) is excluded by
// the character class.
var hashtagRE = regexp.MustCompile(`#[A-Za-z0-9_\-]+`)

// extractHashtagsFromText scans a decoded message text and returns the unique
// hashtag channel mentions found, in first-seen order. The leading '#' is
// preserved so callers can match against canonical channel names directly.
//
// Examples:
//   extractHashtagsFromText("hi #mesh and #fun")       => []string{"#mesh", "#fun"}
//   extractHashtagsFromText("nothing here")             => nil
//   extractHashtagsFromText("dup #x and #x again")      => []string{"#x"}
//
// NOTE: this is a STUB for the red TDD commit. It returns nil so the
// associated test fails on its assertion. The real implementation lands in
// the green commit.
func extractHashtagsFromText(text string) []string {
	_ = hashtagRE
	_ = text
	return nil
}
