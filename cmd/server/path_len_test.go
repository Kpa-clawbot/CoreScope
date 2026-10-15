package main

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func pathLenReference(pathJSON string) int {
	if pathJSON == "" {
		return 0
	}
	var hops []interface{}
	if json.Unmarshal([]byte(pathJSON), &hops) != nil {
		return 0
	}
	return len(hops)
}

// pathLen picks the best observation of every transmission, so it runs for
// every observation on every load and ingest. Decoding the path into a
// []interface{} just to count it allocated about 275MB over one startup on a
// week of a busy network. A path is an array of hex strings: those are
// counted without decoding.
func TestPathLenDoesNotAllocate(t *testing.T) {
	for _, pj := range []string{`[]`, `["aa"]`, `["a1b2c3","d4e5f6","0708"]`, ` [ "aa" , "bb" ] `} {
		if n := testing.AllocsPerRun(100, func() { _ = pathLen(pj) }); n != 0 {
			t.Errorf("pathLen(%s) allocates %.0f times, want 0", pj, n)
		}
	}
}

// pathLen counts exactly what decoding would, including the odd cases it
// hands to the decoder.
func TestPathLenMatchesDecoding(t *testing.T) {
	cases := []string{
		"", "[]", "[ ]", `["aa"]`, `["aa","bb"]`, ` ["aa" ,"bb" ]`, "\t[\n\"aa\"\r]\n",
		`["aa",]`, `["aa"`, `["aa"]x`, `[,]`, `["a\"b","c"]`, `["é"]`, `["a\\"]`,
		`[1,2,3]`, `["aa",null]`, `[["aa"]]`, `{"a":1}`, `"aa"`, `null`, `[""]`, `["",""]`,
		"[\"a\x01\"]", `["aa" "bb"]`, `["aa",,"bb"]`, "[\"\xff\xfe\"]", `[ "aa" , ]`,
	}
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{`[`, `]`, `"`, `,`, ` `, `a`, `1`, `\`, `n`, `{`, `}`, `:`}
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for j := rng.Intn(12); j >= 0; j-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		cases = append(cases, b.String())
	}
	for _, pj := range cases {
		if got, want := pathLen(pj), pathLenReference(pj); got != want {
			t.Fatalf("pathLen(%q) = %d, decoding gives %d", pj, got, want)
		}
	}
}
