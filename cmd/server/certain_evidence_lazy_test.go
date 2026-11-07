package main

import (
	"fmt"
	"testing"
	"time"
)

const certainGroupJSON = `{"type":"GRP_TXT","channel":"#synthetic","channelHashHex":"12","sender":"synthetic","text":"Synthetic relay fixture","timestamp":1000,"encryptedData":"00000000000000000000000000000000","mac":"0000"}`

func TestCertainGroupRelayLeavesDecodedCacheCold(t *testing.T) {
	for _, missingType := range []bool{false, true} {
		for _, indexFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("missingType=%t/indexFirst=%t", missingType, indexFirst), func(t *testing.T) {
				s := chStore()
				pm := chPM()
				tx := chTx(1, 1, PayloadGRP_TXT, 2, time.Now())
				tx.DecodedJSON = certainGroupJSON
				obs := chObservation(tx, chC)
				if missingType {
					tx.PayloadType = nil
				}
				if indexFirst {
					s.indexColdLoadRelayHops(tx, []string{chC}, pm, obs)
					if s.certainHeard[chC] != tx.FirstSeen {
						t.Fatal("genuine unique flood relay was not recorded")
					}
					if tx.parsedDecoded != nil {
						t.Errorf("cold indexing retained %d unrelated decoded fields", len(tx.parsedDecoded))
					}
				}
				if !certainFor(tx, chC, pm) {
					t.Fatal("genuine unique flood relay is not certain")
				}
				if tx.parsedDecoded != nil {
					t.Errorf("packet certainty retained %d unrelated decoded fields", len(tx.parsedDecoded))
				}
				unparsed := false
				tx.decodedOnce.Do(func() { unparsed = true })
				if !unparsed {
					t.Error("certainty consumed the lazy decoder without needing the payload")
				}
			})
		}
	}
}

func TestCertainSignaturePolicyWithoutPayloadMetadata(t *testing.T) {
	for _, missingType := range []bool{false, true} {
		for _, c := range []struct {
			name, field string
			failed      bool
		}{
			{"failed", `,"signatureValid":false`, true},
			{"verified", `,"signatureValid":true`, false},
			{"unchecked", "", false},
			{"null is unchecked", `,"signatureValid":null`, false},
			{"escaped field", `,"signature\u0056alid":false`, true},
		} {
			t.Run(fmt.Sprintf("missingType=%t/%s", missingType, c.name), func(t *testing.T) {
				tx := chTx(1, 1, PayloadADVERT, 2, time.Now())
				tx.DecodedJSON = fmt.Sprintf(`{"pubKey":%q%s}`, chA, c.field)
				if missingType {
					tx.PayloadType = nil
				}
				s := chStore()
				pm := chPM()
				obs := chObservation(tx, chC)
				s.indexByNode(tx)
				s.indexColdLoadRelayHops(tx, []string{chC}, pm, obs)
				for _, pk := range []string{chA, chC} {
					if got := certainFor(tx, pk, pm); got == c.failed {
						t.Errorf("certain=%v with failed=%v", got, c.failed)
					}
					if got := s.certainHeard[pk] != ""; got == c.failed {
						t.Errorf("last heard=%v with failed=%v", got, c.failed)
					}
				}
			})
		}
	}
}
