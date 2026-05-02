package main

import (
	"encoding/json"
	"testing"
	"time"
)

var _ = time.Second // suppress unused import

// Helper to create a minimal PacketStore with GRP_TXT packets for channel analytics testing.
func newChannelTestStore(packets []*StoreTx) *PacketStore {
	ps := &PacketStore{
		packets:       packets,
		byHash:        make(map[string]*StoreTx),
		byTxID:        make(map[int]*StoreTx),
		byObsID:       make(map[int]*StoreObs),
		byObserver:    make(map[string][]*StoreObs),
		byNode:        make(map[string][]*StoreTx),
		byPathHop:     make(map[string][]*StoreTx),
		nodeHashes:    make(map[string]map[string]bool),
		byPayloadType: make(map[int][]*StoreTx),
		rfCache:       make(map[string]*cachedResult),
		topoCache:     make(map[string]*cachedResult),
		hashCache:     make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:     make(map[string]*cachedResult),
		distCache:     make(map[string]*cachedResult),
		subpathCache:  make(map[string]*cachedResult),
		spIndex:       make(map[string]int),
		spTxIndex:     make(map[string][]*StoreTx),
		advertPubkeys: make(map[string]int),
		lastSeenTouched: make(map[string]time.Time),
		clockSkew:     NewClockSkewEngine(),
	}
	ps.byPayloadType[5] = packets
	return ps
}

func makeGrpTx(channelHash int, channel, text, sender string) *StoreTx {
	decoded := map[string]interface{}{
		"type":        "CHAN",
		"channelHash": float64(channelHash),
		"channel":     channel,
		"text":        text,
		"sender":      sender,
	}
	b, _ := json.Marshal(decoded)
	pt := 5
	return &StoreTx{
		ID:          1,
		DecodedJSON: string(b),
		FirstSeen:   "2026-05-01T12:00:00Z",
		PayloadType: &pt,
	}
}

// TestComputeAnalyticsChannels_MergesEncryptedAndDecrypted verifies that packets
// with the same hash byte but different decryption status merge into ONE bucket.
func TestComputeAnalyticsChannels_MergesEncryptedAndDecrypted(t *testing.T) {
	// Hash 129 is the real hash for #wardriving: SHA256(SHA256("#wardriving")[:16])[0] = 129
	// Some packets are decrypted (have channel name), some are not (encrypted)
	packets := []*StoreTx{
		makeGrpTx(129, "#wardriving", "hello", "alice"),
		makeGrpTx(129, "#wardriving", "world", "bob"),
		makeGrpTx(129, "", "", ""),       // encrypted — no channel name
		makeGrpTx(129, "", "", ""),       // encrypted
	}

	store := newChannelTestStore(packets)
	result := store.computeAnalyticsChannels("")

	channels := result["channels"].([]map[string]interface{})
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel bucket, got %d: %+v", len(channels), channels)
	}
	ch := channels[0]
	if ch["name"] != "#wardriving" {
		t.Errorf("expected name '#wardriving', got %q", ch["name"])
	}
	if ch["messages"] != 4 {
		t.Errorf("expected 4 messages, got %v", ch["messages"])
	}
	if ch["encrypted"] != false {
		t.Errorf("expected encrypted=false (some packets decrypted), got %v", ch["encrypted"])
	}
}

// TestComputeAnalyticsChannels_RejectsRainbowTableMismatch verifies that a packet
// with channelHash=72 but channel="#wardriving" (mismatch) does NOT create a
// "#wardriving" bucket — it falls into "ch72" instead.
func TestComputeAnalyticsChannels_RejectsRainbowTableMismatch(t *testing.T) {
	// Hash 72 is NOT the correct hash for #wardriving (which is 129).
	// This simulates a rainbow-table collision/mismatch.
	packets := []*StoreTx{
		makeGrpTx(72, "#wardriving", "ghost", "eve"), // mismatch: hash 72 != wardriving's real hash
		makeGrpTx(129, "#wardriving", "real", "alice"), // correct match
	}

	store := newChannelTestStore(packets)
	result := store.computeAnalyticsChannels("")

	channels := result["channels"].([]map[string]interface{})
	if len(channels) != 2 {
		t.Fatalf("expected 2 channel buckets, got %d: %+v", len(channels), channels)
	}

	// Find the buckets
	var ch72, ch129 map[string]interface{}
	for _, ch := range channels {
		if ch["hash"] == "72" {
			ch72 = ch
		} else if ch["hash"] == "129" {
			ch129 = ch
		}
	}

	if ch72 == nil {
		t.Fatal("expected a bucket for hash 72")
	}
	if ch129 == nil {
		t.Fatal("expected a bucket for hash 129")
	}

	// ch72 should NOT be named "#wardriving" — it should be the placeholder
	if ch72["name"] == "#wardriving" {
		t.Errorf("hash 72 bucket should NOT be named '#wardriving' (rainbow-table mismatch rejected)")
	}
	if ch72["name"] != "ch72" {
		t.Errorf("expected hash 72 bucket named 'ch72', got %q", ch72["name"])
	}

	// ch129 should be named "#wardriving"
	if ch129["name"] != "#wardriving" {
		t.Errorf("expected hash 129 bucket named '#wardriving', got %q", ch129["name"])
	}
}

// TestChannelNameMatchesHash verifies the hash validation function.
func TestChannelNameMatchesHash(t *testing.T) {
	// #wardriving hashes to 129
	if !channelNameMatchesHash("#wardriving", "129") {
		t.Error("expected #wardriving to match hash 129")
	}
	if channelNameMatchesHash("#wardriving", "72") {
		t.Error("expected #wardriving to NOT match hash 72")
	}
	// Without leading # should also work
	if !channelNameMatchesHash("wardriving", "129") {
		t.Error("expected wardriving (without #) to match hash 129")
	}
}

// TestIsPlaceholderName verifies placeholder detection.
func TestIsPlaceholderName(t *testing.T) {
	if !isPlaceholderName("ch129") {
		t.Error("ch129 should be placeholder")
	}
	if !isPlaceholderName("ch0") {
		t.Error("ch0 should be placeholder")
	}
	if isPlaceholderName("#wardriving") {
		t.Error("#wardriving should NOT be placeholder")
	}
	if isPlaceholderName("Public") {
		t.Error("Public should NOT be placeholder")
	}
}
