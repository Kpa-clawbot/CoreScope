package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/meshcore-analyzer/packetpath"
)

// The server reads a packet's path hash size two ways: packetpath.HashSize
// (channel messages' path_hash_size) and the decoder's path.hashSize (shipped
// in every decoded_json). They must agree, or the Channels view and the
// packet detail can show different sizes for the same packet. The cases are
// the ones shared with public/app.js pathHashSize, so this test also runs that
// table in CI (internal/packetpath has no CI job of its own).
func TestPathHashSizeAgreesWithDecoder(t *testing.T) {
	data, err := os.ReadFile("../../test-fixtures/path-hash-size-cases.json")
	if err != nil {
		t.Fatalf("read shared cases: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Name string `json:"name"`
			Raw  string `json:"raw"`
			Want int    `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse shared cases: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("shared cases file has no cases")
	}

	compared := 0
	for _, c := range fixture.Cases {
		if got := packetpath.HashSize(c.Raw); got != c.Want {
			t.Errorf("%s: packetpath.HashSize(%q) = %d, want %d", c.Name, c.Raw, got, c.Want)
		}
		// The decoder needs a whole payload; the shared cases only carry the
		// header and path, so pad them. Inputs it rejects (reserved size bits,
		// truncated or invalid hex) have no decoder value to compare.
		raw := c.Raw + repeatHex("AA", 40)
		pkt, err := DecodePacket(raw, false)
		if err != nil {
			continue
		}
		// TRACE path bytes are SNR readings; the decoder's hashSize for TRACE
		// comes from the trace flags and is not a path hash size.
		if pkt.Header.PayloadType == PayloadTRACE {
			continue
		}
		compared++
		if helper := packetpath.HashSize(raw); pkt.Path.HashSize != helper {
			t.Errorf("%s: decoder path.hashSize = %d, packetpath.HashSize = %d", c.Name, pkt.Path.HashSize, helper)
		}
	}
	if compared == 0 {
		t.Fatal("no shared case was decodable; the agreement check compared nothing")
	}
}
