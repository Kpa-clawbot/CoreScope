package main

import "unicode/utf8"

// Observer-supplied strings come straight from the MQTT topic (id, IATA) or
// the status JSON (origin/name, model, firmware, client version, radio). Any
// publisher controls them and nothing upstream bounds their length, so cap
// them before they reach the observers table. Values are truncated, not
// dropped, so a legitimate observer with a long name still shows up.
const (
	maxObserverIDLen   = 128 // ids are 64-hex pubkeys in practice
	maxObserverTextLen = 128 // name, model, firmware, client version, radio
	maxObserverIATALen = 16  // region codes are 3–8 chars
)

// clampObserverField strips control characters and truncates to max runes.
func clampObserverField(s string, max int) string {
	s = sanitizeName(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
