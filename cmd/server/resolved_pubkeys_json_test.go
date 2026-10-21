package main

import (
	"math/rand"
	"strings"
	"testing"
)

// The bulk loaders decode each observation's resolved_path (a JSON array of
// pubkeys and nulls, ~420 chars on live data, set on most observations)
// only to collect its non-empty pubkeys. Decoding into []*string allocated
// a string and a pointer per hop plus two slices: about 210MB over one
// startup on a busy week. resolvedPubkeysFromJSON reads the usual array in
// place, and a pubkey it has seen before costs nothing: only the result is
// allocated.
func TestResolvedPubkeysFromJSONAllocations(t *testing.T) {
	pk := strings.Repeat("ab", 32)
	rp := `["` + pk + `",null,"` + strings.Repeat("cd", 32) + `",null,"` + strings.Repeat("ef", 32) + `"]`
	_ = resolvedPubkeysFromJSON(rp) // first sight keeps one copy of each
	if n := testing.AllocsPerRun(100, func() { _ = resolvedPubkeysFromJSON(rp) }); n > 1 {
		t.Fatalf("resolvedPubkeysFromJSON allocates %.0f times for 3 known pubkeys, want at most 1 (the result)", n)
	}
}

// It returns exactly what decoding and extracting would, odd inputs included.
func TestResolvedPubkeysFromJSONMatchesDecoding(t *testing.T) {
	cases := []string{
		"", "[]", "null", "[null]", `["aa"]`, `["aa",null,"bb"]`, ` [ "aa" , null ] `, `[""]`, `["",null,""]`,
		`["aa",]`, `["aa"`, `["aa"]x`, `[1]`, `["aa",1]`, `[nul]`, `[nulll]`, `["a\"b"]`, `["é"]`,
		`[["aa"]]`, `{"a":1}`, "[\"a\x01\"]", `["aa" "bb"]`, `[,]`, `[null,]`, "[\"\xff\"]", `[true]`,
	}
	rng := rand.New(rand.NewSource(2))
	alphabet := []string{`[`, `]`, `"`, `,`, ` `, `a`, `1`, `\`, `null`, `nul`, `{`, `:`}
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for j := rng.Intn(12); j >= 0; j-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		got, want := resolvedPubkeysFromJSON(s), extractResolvedPubkeys(unmarshalResolvedPath(s))
		if len(got) != len(want) {
			t.Fatalf("%q: got %q, want %q", s, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: got %q, want %q", s, got, want)
			}
		}
	}
}
