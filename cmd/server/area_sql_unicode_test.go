package main

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

type areaUnicodeCase struct {
	name    string
	payload string
	matches bool
}

// PostgreSQL rejects NUL escapes in unrelated fields; SQLite's JSON path
// lookup truncates a field name at NUL. Neither may change area membership.
func TestAreaFilterSQLUnicodeEscapes(t *testing.T) {
	checkAreaFilterSQLEscapes(t, []areaUnicodeCase{
		{"plain", `{"pubKey":"inside"}`, true},
		{"channel-nul", `{"type":"CHAN","text":"message\u0000tail"}`, false},
		{"message-nul", `{"type":"TXT_MSG","text":"message\u0000tail"}`, false},
		{"unrelated-nul-value", `{"pubKey":"inside","name":"name\u0000tail"}`, true},
		{"unrelated-nul-key", `{"pubKey":"inside","other\u0000":"value"}`, true},
		{"nul-originator", `{"pubKey":"inside\u0000"}`, false},
		{"nul-within-originator", `{"pubKey":"in\u0000side"}`, false},
		{"nul-field-key", `{"pubKey\u0000":"inside"}`, false},
		{"nul-key-distinct", `{"pubKey":"inside","pubKey\u0000":"outside"}`, true},
		{"literal-nul-spelling", `{"pubKey":"inside\\u0000"}`, true},
		{"literal-sentinel", `{"pubKey":"inside\\0"}`, true},
		{"unicode-backslash", `{"pubKey":"inside\u005c0"}`, true},
		{"uppercase-unicode-backslash", `{"pubKey":"inside\u005C0"}`, true},
		{"backslash", `{"pubKey":"inside\\"}`, true},
		{"double-backslash", `{"pubKey":"inside\\\\"}`, true},
		{"literal-nul-field-key", `{"pubKey\\u0000":"inside"}`, false},
		{"escaped-field-key", `{"pub\u004Bey":"inside"}`, true},
		{"nul-legacy-precedence", `{"public_key":"inside\u0000","pubKey":"inside"}`, false},
		{"legacy-precedence", `{"public_key":"inside","pubKey":"outside","text":"\u0000"}`, true},
		{"empty-legacy-fallback", `{"public_key":"","pubKey":"inside","text":"\u0000"}`, true},
		{"nonstring-legacy-fallback", `{"public_key":7,"pubKey":"inside","text":"\u0000"}`, true},
		{"nul-unused-fallback", `{"public_key":"inside","pubKey":"\u0000"}`, true},
		{"other-control-escapes", `{"pubKey":"control\t\n\r\b\f\"\/"}`, true},
		{"last-pubkey-wins", `{"pubKey":"outside","pubKey":"inside"}`, true},
		{"last-pubkey-excludes", `{"pubKey":"inside","pubKey":"outside"}`, false},
		{"last-legacy-nonstring-fallback", `{"public_key":"outside","public_key":7,"pubKey":"inside"}`, true},
		{"last-legacy-nonstring-excludes", `{"public_key":"inside","public_key":7,"pubKey":"outside"}`, false},
		{"last-pubkey-nonstring", `{"pubKey":"inside","pubKey":7}`, false},
		{"escaped-duplicate-key", `{"pub\u004Bey":"outside","pubKey":"inside"}`, true},
	})
}

func TestAreaFilterSQLSurrogateEscapes(t *testing.T) {
	// SQLite preserves lone surrogates as invalid UTF-8; normalizing those
	// originators is separate from PostgreSQL's extraction-error regression.
	postgresOnly(t)
	checkAreaFilterSQLEscapes(t, []areaUnicodeCase{
		{"unrelated-high-surrogate", `{"pubKey":"inside","name":"name\ud800tail"}`, true},
		{"unrelated-low-surrogate", `{"pubKey":"inside","name":"name\udc00tail"}`, true},
		{"high-surrogate-originator", `{"pubKey":"inside\ud800"}`, true},
		{"low-surrogate-originator", `{"pubKey":"inside\udc00"}`, true},
		{"valid-surrogate-pair", `{"pubKey":"inside\ud83d\ude00"}`, true},
		{"high-then-pair", `{"pubKey":"inside\ud800\ud83d\ude00"}`, true},
		{"pair-then-low", `{"pubKey":"inside\ud83d\ude00\udfff"}`, true},
		{"literal-surrogate", `{"pubKey":"inside\\ud800"}`, true},
		{"surrogate-field-key", `{"pubKey\ud800":"inside"}`, false},
	})
}

func checkAreaFilterSQLEscapes(t *testing.T, cases []areaUnicodeCase) {
	t.Helper()
	db, writer := openPostgresValueFixture(t)
	keys := []string{
		"inside", `inside\u0000`, `inside\0`, `inside\`, `inside\\`,
		"inside\U0001f600", "inside\ufffd", "inside\ufffd\U0001f600", "inside\U0001f600\ufffd",
		`inside\ud800`, "control\t\n\r\b\f\"/",
	}
	copyTestRows(t, writer, "nodes", []string{"public_key", "lat", "lon"}, len(keys), func(i int) []any {
		return []any{keys[i], 11.0, 11.0}
	})
	stamp := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	copyTestRows(t, writer, "transmissions", []string{"id", "raw_hex", "hash", "first_seen", "last_seen", "payload_type", "decoded_json"}, len(cases), func(i int) []any {
		seen := stamp.Add(time.Duration(i) * time.Minute)
		kind := PayloadADVERT
		if cases[i].name == "channel-nul" {
			kind = PayloadGRP_TXT
		} else if cases[i].name == "message-nul" {
			kind = PayloadTXT_MSG
		}
		return []any{i + 1, "AA", cases[i].name, seen.Format(time.RFC3339), seen.Unix(), kind, cases[i].payload}
	})
	cfg := &Config{Areas: map[string]AreaEntry{
		"AREA": {Polygon: [][2]float64{{10, 10}, {10, 12}, {12, 12}, {12, 10}}},
	}}
	complete := NewPacketStore(db, &PacketStoreConfig{})
	complete.config = cfg
	if err := complete.Load(); err != nil {
		t.Fatal(err)
	}
	// The startup-budget regression separately exercises actual admission.
	// Here every old packet stays in SQL, so an extraction error cannot be
	// hidden by the store's fallback to a complete in-memory copy.
	limited := newTestStoreWithDB(t, db, cfg)
	limited.startupMemoryLimited = true
	limited.oldestLoaded = stamp.Add(time.Hour).Format(time.RFC3339)
	want := make([]string, 0)
	for i := len(cases) - 1; i >= 0; i-- {
		if cases[i].matches {
			want = append(want, cases[i].name)
		}
	}
	for _, source := range []struct {
		name  string
		store *PacketStore
	}{{"memory", complete}, {"sql-fallback", limited}} {
		for _, grouped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/grouped-%t", source.name, grouped), func(t *testing.T) {
				query := source.store.QueryPackets
				if grouped {
					query = source.store.QueryGroupedPackets
				}
				for offset := 0; offset <= len(want); offset += 3 {
					result := query(PacketQuery{Area: "AREA", Limit: 3, Offset: offset})
					if result.Total != len(want) {
						t.Errorf("offset %d total=%d, want %d", offset, result.Total, len(want))
					}
					got := make([]string, 0, len(result.Packets))
					for _, packet := range result.Packets {
						hash, _ := packet["hash"].(string)
						got = append(got, hash)
					}
					wantPage := want[offset:min(offset+3, len(want))]
					if !slices.Equal(got, wantPage) {
						t.Errorf("offset %d hashes=%v, want %v", offset, got, wantPage)
					}
				}
			})
		}
	}
}
