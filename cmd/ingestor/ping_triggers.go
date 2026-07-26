package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

// channelMentionPrefixRe strips a leading "@target " reply-address the same
// way the frontend does (public/channels.js replyMatch) and cmd/server/db.go
// does (pingBotReply's isPingTrigger) before matching the ping trigger, so
// "@CoreScopeBot ping" triggers the same as a bare "ping". Kept in sync by
// hand across all three copies -- see cmd/server/db.go's isPingTrigger.
var channelMentionPrefixRe = regexp.MustCompile(`^@[A-Za-z0-9_-]{1,32}\s+`)

// pingTriggerWords mirrors cmd/server/db.go's pingTriggerWords and
// public/channels.js's copy of the same list -- keep all three in sync by
// hand.
var pingTriggerWords = map[string]bool{
	"ping":  true,
	"/ping": true,
}

// isPingTrigger reports whether displayText, after stripping a leading
// "@target " mention, exactly matches one of pingTriggerWords. Mirrors
// cmd/server/db.go's isPingTrigger exactly so a message that gets a pong
// reply in the Channels view is the same one recorded here.
func isPingTrigger(displayText string) bool {
	trigger := strings.TrimSpace(displayText)
	trigger = channelMentionPrefixRe.ReplaceAllString(trigger, "")
	return pingTriggerWords[strings.ToLower(strings.TrimSpace(trigger))]
}

// pingTriggerSenderAndText extracts the display sender/text from a CHAN
// transmission's decoded_json exactly the way cmd/server/db.go's
// GetChannelMessages computes displaySender/displayText: decoded["text"]
// is "Sender: message body" for most firmware, so a leading "Name: "
// prefix (first 50 chars) is peeled off and preferred over decoded["sender"]
// when present.
func pingTriggerSenderAndText(decodedJSON string) (sender, displayText string, ok bool) {
	var decoded map[string]interface{}
	if json.Unmarshal([]byte(decodedJSON), &decoded) != nil {
		return "", "", false
	}
	text, _ := decoded["text"].(string)
	sender, _ = decoded["sender"].(string)
	displayText = text
	if text != "" {
		if idx := strings.Index(text, ": "); idx > 0 && idx < 50 {
			sender = text[:idx]
			displayText = text[idx+2:]
		}
	}
	return sender, displayText, true
}
