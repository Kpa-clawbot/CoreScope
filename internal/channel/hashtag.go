package channel

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxHashtagNameBytes is the longest hashtag channel name in bytes, "#"
// included: firmware keeps the name in ChannelDetails.name[32] (MeshCore
// src/helpers/ChannelDetails.h:8), one byte of which is the terminating NUL.
const MaxHashtagNameBytes = 31

// NameError is a problem with a hashtag channel name. Msg is safe to show.
type NameError struct{ Msg string }

func (e *NameError) Error() string { return e.Msg }

const zwj = '\u200d'

const (
	nameMsgInvalid   = "the name is not valid text"
	nameMsgEmpty     = "enter a channel name after #"
	nameMsgLong      = "a channel name is at most 31 bytes including the # (MeshCore stores 32 with the terminator)"
	nameMsgPublic    = "Public is the built-in channel and cannot be proposed"
	nameMsgInvisible = "the name contains invisible or control characters"
)

// ValidateHashtagName trims raw, prefixes a missing "#" and checks the name
// rules of docs/specs/2026-10-07-channel-proposals-design.md. It returns the
// exact name the channel key is derived from (case preserved).
// public/channel-proposals.js mirrors these rules; this function decides.
func ValidateHashtagName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "#") {
		s = "#" + s
	}
	if !utf8.ValidString(s) {
		return "", &NameError{Msg: nameMsgInvalid}
	}
	if strings.TrimSpace(strings.ReplaceAll(s[1:], string(zwj), "")) == "" {
		return "", &NameError{Msg: nameMsgEmpty}
	}
	if len(s) > MaxHashtagNameBytes {
		return "", &NameError{Msg: nameMsgLong}
	}
	if strings.EqualFold(s, "#public") {
		return "", &NameError{Msg: nameMsgPublic}
	}
	for _, r := range s {
		if r == zwj {
			continue
		}
		if invisibleNameRune(r) {
			return "", &NameError{Msg: nameMsgInvisible}
		}
	}
	return s, nil
}

// invisibleNameRune is a stricter variant of the display-name rule in
// internal/users/validate.go: on top of control, format (Cf) and line or
// paragraph separators it refuses the invisible fillers that are not Cf
// (Other_Default_Ignorable_Code_Point such as U+3164, and U+2800 BRAILLE
// PATTERN BLANK) and every space separator other than ASCII space, so a name
// cannot pass for another one. Variation selectors such as U+FE0F stay
// allowed for emoji. The caller skips ZWJ before asking.
func invisibleNameRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || r == '\u2800' ||
		(r != ' ' && unicode.Is(unicode.Zs, r))
}
