package channel

import (
	"errors"
	"strings"
	"testing"
)

const (
	wantEmpty     = "enter a channel name after #"
	wantLong      = "a channel name is at most 31 bytes including the # (MeshCore stores 32 with the terminator)"
	wantPublic    = "Public is the built-in channel and cannot be proposed"
	wantInvisible = "the name contains invisible or control characters"
	wantInvalid   = "the name is not valid text"
)

// The same accept and refuse lists are mirrored in
// tests/unit/test-channel-proposals-ui.js for public/channel-proposals.js.
func TestValidateHashtagNameAccepts(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mycity", "#mycity"},
		{"  #MyCity  ", "#MyCity"},
		{"\u3000#tokyo\u00a0", "#tokyo"},
		{"# a", "# a"},
		{"#" + strings.Repeat("a", 30), "#" + strings.Repeat("a", 30)},
		{"#" + strings.Repeat("é", 15), "#" + strings.Repeat("é", 15)},
		{"#" + strings.Repeat("€", 10), "#" + strings.Repeat("€", 10)},
		{"#👩\u200d💻", "#👩\u200d💻"},
		{"#publicity", "#publicity"},
		{`#a"<b>`, `#a"<b>`},
	}
	for _, c := range cases {
		got, err := ValidateHashtagName(c.in)
		if err != nil || got != c.want {
			t.Errorf("ValidateHashtagName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if n := len("#👩\u200d💻"); n != 12 {
		t.Fatalf("emoji fixture is %d bytes, want 12", n)
	}
}

func TestValidateHashtagNameRefuses(t *testing.T) {
	cases := []struct{ in, msg string }{
		{"", wantEmpty},
		{"   ", wantEmpty},
		{"#", wantEmpty},
		{" # ", wantEmpty},
		{"#\u200d", wantEmpty},
		{"#" + strings.Repeat("a", 31), wantLong},
		{"#" + strings.Repeat("é", 15) + "a", wantLong},
		{"#public", wantPublic},
		{"public", wantPublic},
		{"Public", wantPublic},
		{"#PUBLIC", wantPublic},
		{"#a\u202eb", wantInvisible},
		{"#a\u200bb", wantInvisible},
		{"#a\ufeffb", wantInvisible},
		{"\ufeffabc", wantInvisible},
		{"#a\tb", wantInvisible},
		{"#a\u2028b", wantInvisible},
		{"#a\u0007b", wantInvisible},
		{"#\u200e", wantInvisible},
		{"#a\xffb", wantInvalid},
	}
	for _, c := range cases {
		got, err := ValidateHashtagName(c.in)
		var ne *NameError
		if !errors.As(err, &ne) || ne.Msg != c.msg {
			t.Errorf("ValidateHashtagName(%q) = %q, %v; want NameError %q", c.in, got, err, c.msg)
		}
	}
}

func TestMaxHashtagNameBytesMatchesFirmware(t *testing.T) {
	// ChannelDetails.name[32] minus the terminating NUL.
	if MaxHashtagNameBytes != 31 {
		t.Fatalf("MaxHashtagNameBytes = %d, want 31", MaxHashtagNameBytes)
	}
}
