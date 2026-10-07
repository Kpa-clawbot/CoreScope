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
	// Mirrors the display-name rule in internal/users/validate.go; keep both in sync.
	for _, r := range s {
		if r == zwj {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return "", &NameError{Msg: nameMsgInvisible}
		}
	}
	return s, nil
}
