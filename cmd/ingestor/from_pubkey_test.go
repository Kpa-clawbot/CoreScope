package main

// Tests for #1143: ingestor must populate transmissions.from_pubkey at
// write time (cheap — already parsing decoded_json) so attribution queries
// don't rely on JSON substring matches.

import (
	"database/sql"
	"testing"
)

func TestInsertTransmission_FromPubkeyPopulatedForAdvert(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const pk = "f7181c468dfe7c55aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	data := &PacketData{
		RawHex:         "AABBCC",
		Timestamp:      "2026-03-25T00:00:00Z",
		ObserverID:     "obs1",
		Hash:           "advert_hash_1143",
		RouteType:      1,
		PayloadType:    4, // ADVERT
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"ADVERT","pubKey":"` + pk + `","name":"X"}`,
		FromPubkey:     pk,
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	var got sql.NullString
	s.db.QueryRow("SELECT from_pubkey FROM transmissions WHERE hash = ?", data.Hash).Scan(&got)
	if !got.Valid || got.String != pk {
		t.Fatalf("from_pubkey = %v (valid=%v), want %q", got.String, got.Valid, pk)
	}
}

func TestInsertTransmission_FromPubkeyNullForNonAdvert(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := &PacketData{
		RawHex:         "AA",
		Timestamp:      "2026-03-25T00:00:00Z",
		ObserverID:     "obs1",
		Hash:           "txt_hash_1143",
		RouteType:      1,
		PayloadType:    2, // TXT_MSG
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		// FromPubkey deliberately empty — non-ADVERTs don't carry one.
	}
	if _, err := s.InsertTransmission(data); err != nil {
		t.Fatal(err)
	}

	var got sql.NullString
	s.db.QueryRow("SELECT from_pubkey FROM transmissions WHERE hash = ?", data.Hash).Scan(&got)
	if got.Valid {
		t.Fatalf("from_pubkey for non-ADVERT must be NULL, got %q", got.String)
	}
}

func TestBuildPacketData_PopulatesFromPubkey(t *testing.T) {
	const pk = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	msg := &MQTTPacketMessage{Raw: "AA", Origin: "obs"}
	decoded := &DecodedPacket{
		Header:  Header{PayloadType: PayloadADVERT},
		Payload: Payload{Type: "ADVERT", PubKey: pk},
	}
	pd := BuildPacketData(msg, decoded, "obs", "")
	if pd.FromPubkey != pk {
		t.Fatalf("BuildPacketData FromPubkey = %q, want %q", pd.FromPubkey, pk)
	}

	// Non-ADVERT: must not carry a pubkey.
	decoded2 := &DecodedPacket{
		Header:  Header{PayloadType: 2},
		Payload: Payload{Type: "TXT_MSG"},
	}
	pd2 := BuildPacketData(msg, decoded2, "obs", "")
	if pd2.FromPubkey != "" {
		t.Fatalf("BuildPacketData FromPubkey for non-ADVERT = %q, want empty", pd2.FromPubkey)
	}
}
