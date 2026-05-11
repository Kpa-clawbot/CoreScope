package main

import (
	"reflect"
	"testing"
)

// TestExtractHashtagsFromText covers the parsing helper used to discover new
// hashtag channels from decoded message text (issue #688).
func TestExtractHashtagsFromText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single mention from issue body",
			in:   "Hey, I created new channel called #mesh, please join",
			want: []string{"#mesh"},
		},
		{
			name: "multiple mentions preserve order",
			in:   "join #mesh and #wardriving today",
			want: []string{"#mesh", "#wardriving"},
		},
		{
			name: "dedup repeated mentions",
			in:   "#x then #x again",
			want: []string{"#x"},
		},
		{
			name: "ignores trailing punctuation",
			in:   "check #fun!",
			want: []string{"#fun"},
		},
		{
			name: "no hashtag returns nil",
			in:   "nothing to see here",
			want: nil,
		},
		{
			name: "bare # is not a channel",
			in:   "issue #",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractHashtagsFromText(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractHashtagsFromText(%q): got %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestGetChannels_DiscoversHashtagsFromMessages verifies that when a decoded
// CHAN message body mentions a previously-unknown hashtag channel, that
// channel is auto-registered in the GetChannels output (#688).
func TestGetChannels_DiscoversHashtagsFromMessages(t *testing.T) {
	// One known channel (#general) where someone announces a new channel #mesh.
	pkt := makeGrpTx(198, "general", "Alice: Hey, I created new channel called #mesh, please join", "Alice")
	ps := newChannelTestStore([]*StoreTx{pkt})

	channels := ps.GetChannels("")

	var sawGeneral, sawMesh bool
	for _, ch := range channels {
		switch ch["name"] {
		case "general":
			sawGeneral = true
		case "#mesh":
			sawMesh = true
			if d, _ := ch["discovered"].(bool); !d {
				t.Errorf("expected discovered=true on #mesh, got %v", ch["discovered"])
			}
		}
	}
	if !sawGeneral {
		t.Error("expected the source channel 'general' in GetChannels output")
	}
	if !sawMesh {
		t.Errorf("expected discovered hashtag channel '#mesh' in GetChannels output; got %d channels: %+v", len(channels), channels)
	}
}
