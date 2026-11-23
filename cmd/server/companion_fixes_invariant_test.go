package main

import (
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Location privacy (B4): a companion GPS fix (lat/lon of client_receptions or
// client_rf_samples) is used only through the fix grid (hexgrid.go): every
// reader bins its rows (binFixesInBBox, binCoverageRows) or maps each fix to
// its cell centre (fixGrid.centre) before anything is filtered, binned,
// measured or served. TestCompanionFixReadsInvariant fails on a declaration
// whose SQL selects those columns without referencing one of the helpers,
// unless it is a listed reader. The parsing is node_coords_invariant_test.go's
// (nodeCoordUnits).

// fixGridHelpers are the identifiers a reader must reference.
var fixGridHelpers = []string{"binFixesInBBox", "binCoverageRows", "centre"}

// exactFixReaders read fixes as they are, each for a result that leaves it
// only through another privacy step. via, when set, is the declaration that
// must apply fuzzEstimate to what the reader returns.
var exactFixReaders = map[string]struct{ file, why, via string }{
	"directReceptions": {"reported_nodes.go",
		"the /api/nodes/reported estimate is a centroid of exact fixes; it is served only through fuzzEstimate (Fuzz, radius + R)",
		"computeReportedNodes"},
	"leaderboardRes": {"rx_dashboard.go",
		"reads only MAX(ABS(lat)) of the whole window, to pick one hex resolution no finer than the radius; the value is never served", ""},
	"routeDescriptions": {"openapi.go",
		"API documentation prose that names the tables; no query", ""},
}

var fixTableRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+["` + "`" + `]?(?:client_receptions|client_rf_samples)["` + "`" + `]?(?:\s+(?:as\s+)?(\w+))?`)

// readsCompanionFixes reports whether the unit's SQL selects lat/lon from the
// companion tables: some literal names a table and some SQL literal names
// lat/lon bare or through the table's alias.
func readsCompanionFixes(u nodeCoordUnit) bool {
	var tables bool
	aliases := map[string]bool{"client_receptions": true, "client_rf_samples": true}
	for _, l := range u.lits {
		for _, m := range fixTableRe.FindAllStringSubmatch(l, -1) {
			tables = true
			if a := strings.ToLower(m[1]); a != "" && !sqlKeywords[a] {
				aliases[a] = true
			}
		}
	}
	if !tables {
		return false
	}
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, regexp.QuoteMeta(a))
	}
	qualified := regexp.MustCompile(`(?i)\b(?:` + strings.Join(names, "|") + `)\.(?:lat|lon)\b`)
	for _, l := range u.lits {
		if !strings.ContainsAny(l, " ,\t\n") {
			continue
		}
		if bareCoordRe.MatchString(l) || qualified.MatchString(l) {
			return true
		}
	}
	return false
}

func companionFixViolations(units []nodeCoordUnit) []string {
	var out []string
	refs := map[string]map[string]bool{}
	for _, u := range units {
		refs[u.file+":"+u.name] = u.refs
	}
	for _, u := range units {
		if !readsCompanionFixes(u) {
			continue
		}
		where := u.pos.String() + " " + u.name
		if r, ok := exactFixReaders[u.name]; ok && r.file == u.file {
			if r.via != "" && !refs[r.file+":"+r.via]["fuzzEstimate"] {
				out = append(out, where+": exact reader whose result is not served through fuzzEstimate in "+r.via)
			}
			continue
		}
		binned := false
		for _, h := range fixGridHelpers {
			binned = binned || u.refs[h]
		}
		if !binned {
			out = append(out, where+": reads companion lat/lon without the fix grid ("+strings.Join(fixGridHelpers, ", ")+")")
		}
	}
	return out
}

func TestCompanionFixReadsInvariant(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var units []nodeCoordUnit
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, nodeCoordUnits(t, fset, name, src)...)
	}
	seen := map[string]bool{}
	readers := 0
	for _, u := range units {
		seen[u.file+":"+u.name] = true
		if readsCompanionFixes(u) {
			readers++
		}
	}
	for name, r := range exactFixReaders {
		if !seen[r.file+":"+name] {
			t.Errorf("exactFixReaders lists %s, which is not in %s", name, r.file)
		}
	}
	// The detector must see the readers it guards: coverage (global, per
	// node), RF noise, track, analytics summary, leaderboard, reported.
	if readers < 7 {
		t.Errorf("only %d companion fix readers detected", readers)
	}
	v := companionFixViolations(units)
	sort.Strings(v)
	for _, s := range v {
		t.Error(s)
	}
}

// The detector itself, on made-up sources.
func TestCompanionFixReadsInvariantDetector(t *testing.T) {
	cases := []struct {
		name, src string
		bad       bool
	}{
		{"plain", `func f() { q("SELECT lat, lon FROM client_receptions") }`, true},
		{"rf samples", "func f() { q(`SELECT lat, lon, noise_floor FROM client_rf_samples WHERE x`) }", true},
		{"where only", `func f() { w := []string{"lat BETWEEN ? AND ?"}; q("SELECT snr FROM client_receptions WHERE " + w[0]) }`, true},
		{"alias", `func f() { q("SELECT cr.lat FROM client_receptions cr JOIN nodes n ON n.k = cr.k") }`, true},
		{"binned", `func f(g *fixGrid) { q("SELECT lat, lon FROM client_receptions"); binFixesInBBox(r, g, b, p) }`, false},
		{"centre", `func f(g *fixGrid) { q("SELECT lat, lon FROM client_rf_samples"); g.centre(a, b) }`, false},
		{"no coordinates", `func f() { q("SELECT rx_pubkey, MAX(rx_at) FROM client_receptions GROUP BY rx_pubkey") }`, false},
		{"other table", `func f() { q("SELECT lat, lon FROM nodes") }`, false},
		{"exact reader, served fuzzed", `func directReceptions() { q("SELECT lat, lon FROM client_receptions") }
			func computeReportedNodes() { fuzzEstimate(f, pk, e) }`, false},
		{"exact reader, not fuzzed", `func directReceptions() { q("SELECT lat, lon FROM client_receptions") }
			func computeReportedNodes() {}`, true},
	}
	for _, tc := range cases {
		fset := token.NewFileSet()
		units := nodeCoordUnits(t, fset, "reported_nodes.go", []byte("package main\n"+tc.src))
		if bad := len(companionFixViolations(units)) > 0; bad != tc.bad {
			t.Errorf("%s: violation=%v, want %v", tc.name, bad, tc.bad)
		}
	}
}
