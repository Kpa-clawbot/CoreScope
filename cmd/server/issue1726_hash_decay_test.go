package main

import (
	"testing"
	"time"
)

// Issue #1726: a MeshCore v1.16.0 repeater configured for 2-byte path hashes
// (path.hash.mode=1) was still shown as "varies" / mixed 1-byte+2-byte. The
// node had one genuine 1-byte flood advert mid-window, but every advert since
// has been 2-byte. The flip-flop heuristic weighed that stale 1-byte advert
// equally with recent ones, so the node stayed flagged for the full 7-day
// window even though its current config is settled.
//
// Expected: when the most recent non-zero-hop adverts all agree on a size, the
// node is "settled" and must NOT be flagged inconsistent. HashSize must reflect
// the chronologically-latest advert.

// hashAdvertTx builds an ADVERT StoreTx (route FLOOD, non-zero hop) for a given
// pubkey, hash size and FirstSeen timestamp.
//
//	hs=1 → path byte 0x01 (top 2 bits 00, hop bits non-zero)
//	hs=2 → path byte 0x41 (top 2 bits 01)
func hashAdvertTx(pk string, hs int, firstSeen string) *StoreTx {
	pt := 4 // ADVERT
	pathByte := "01"
	if hs == 2 {
		pathByte = "41"
	}
	return &StoreTx{
		RawHex:      "11" + pathByte + "aabb", // header 0x11 → routeType FLOOD
		FirstSeen:   firstSeen,
		PayloadType: &pt,
		DecodedJSON: `{"pubKey":"` + pk + `","name":"DK_3400_RAK_TEST","type":"ADVERT"}`,
	}
}

// ts formats a relative timestamp the way the ingestor writes first_seen
// (time.RFC3339, no fractional seconds).
func ts(d time.Duration) string {
	return time.Now().UTC().Add(d).Format(time.RFC3339)
}

func TestIssue1726_SettledNodeNotInconsistent(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pk := "36f6c7c73dfd265db996aeee25e1dc0cfe1ad4e5c1d6dd575325e3b241f4af78"

	// Chronological history within the 7-day window: a single 1-byte blip
	// 5 days ago, then settled to 2-byte. Old heuristic: AllSizes={1,2},
	// 2 transitions → Inconsistent=true. The node is actually settled at 2-byte.
	ps.byPayloadType[4] = []*StoreTx{
		hashAdvertTx(pk, 2, ts(-6*24*time.Hour)),
		hashAdvertTx(pk, 1, ts(-5*24*time.Hour)),
		hashAdvertTx(pk, 2, ts(-2*24*time.Hour)),
		hashAdvertTx(pk, 2, ts(-1*24*time.Hour)),
		hashAdvertTx(pk, 2, ts(-1*time.Hour)),
	}

	info := ps.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatalf("expected hash size info for %s", pk)
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize = %d, want 2 (latest advert is 2-byte)", ni.HashSize)
	}
	if ni.Inconsistent {
		t.Error("settled node (last 3 adverts all 2-byte) must NOT be flagged inconsistent")
	}
}

func TestIssue1726_HashSizeUsesChronologicallyLatest(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pk := "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff5555aaaa6666bbbb7777"

	// Inserted out of chronological order: the 2-byte advert is newest by
	// FirstSeen but appended first. HashSize must follow time, not insertion.
	ps.byPayloadType[4] = []*StoreTx{
		hashAdvertTx(pk, 2, ts(-1*time.Hour)),    // newest
		hashAdvertTx(pk, 1, ts(-5*24*time.Hour)), // oldest, appended last
	}

	info := ps.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatalf("expected hash size info for %s", pk)
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize = %d, want 2 (chronologically-latest advert)", ni.HashSize)
	}
}

// Chronological ordering must be robust to FirstSeen format differences:
// the ingestor writes RFC3339 with no fractional seconds, but a fractional
// (".000Z") form must still order correctly relative to it. A naive string
// compare would sort the no-fraction "...05Z" after a same-second "...05.000Z"
// (because 'Z' > '.'), picking the wrong "latest" advert.
func TestIssue1726_OrderingRobustToTimestampFormat(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pk := "1234000056780000abcd0000ef120000345600007890000012340000567800ab"

	// Same wall-clock second, sub-second apart: the older advert in the
	// no-fraction form, the newer 1ms later in the fractional form. A string
	// compare sorts "...05Z" AFTER "...05.001Z" ('Z' > '.'), so it would treat
	// the older 1-byte advert as latest; chronological parsing must not.
	base := time.Now().UTC().Truncate(time.Second).Add(-3 * 24 * time.Hour)
	older := base.Format(time.RFC3339)                                         // "...05Z"
	newer := base.Add(1 * time.Millisecond).Format("2006-01-02T15:04:05.000Z") // "...05.001Z"

	pt := 4
	mk := func(pathByte, firstSeen string) *StoreTx {
		return &StoreTx{
			RawHex:      "11" + pathByte + "aabb",
			FirstSeen:   firstSeen,
			PayloadType: &pt,
			DecodedJSON: `{"pubKey":"` + pk + `","type":"ADVERT"}`,
		}
	}
	ps.byPayloadType[4] = []*StoreTx{
		mk("41", newer), // 2-byte, newest, appended first
		mk("01", older), // 1-byte, oldest
	}

	info := ps.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatalf("expected hash size info for %s", pk)
	}
	if ni.HashSize != 2 {
		t.Errorf("HashSize = %d, want 2 (newest advert, despite mixed timestamp formats)", ni.HashSize)
	}
}

// A node still genuinely flip-flopping (recent adverts disagree) must remain
// flagged — the decay only clears settled nodes, not active flappers.
func TestIssue1726_ActiveFlapperStaysInconsistent(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	pk := "ffff111122223333444455556666777788889999aaaabbbbccccddddeeeeffff"

	ps.byPayloadType[4] = []*StoreTx{
		hashAdvertTx(pk, 2, ts(-4*24*time.Hour)),
		hashAdvertTx(pk, 1, ts(-3*24*time.Hour)),
		hashAdvertTx(pk, 2, ts(-2*24*time.Hour)),
		hashAdvertTx(pk, 1, ts(-1*24*time.Hour)),
		hashAdvertTx(pk, 2, ts(-1*time.Hour)),
	}

	info := ps.GetNodeHashSizeInfo()
	ni := info[pk]
	if ni == nil {
		t.Fatalf("expected hash size info for %s", pk)
	}
	if !ni.Inconsistent {
		t.Error("node whose recent adverts disagree must stay flagged inconsistent")
	}
}
