package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures ---

// grpTxtFrameAt is grpTxtFrame with a chosen sender_timestamp.
func grpTxtFrameAt(t *testing.T, keyHex, text string, ts uint32) string {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil || (len(key) != 16 && len(key) != 32) {
		t.Fatalf("bad key %q", keyHex)
	}
	plain := make([]byte, 5, 64)
	binary.LittleEndian.PutUint32(plain[0:4], ts)
	plain = append(plain, []byte(text)...)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, 0)
	}
	block, _ := aes.NewCipher(key[:16]) // AES-128 on the first 16 bytes of a 32-byte secret
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	secret := make([]byte, 32)
	copy(secret, key)
	m := hmac.New(sha256.New, secret)
	m.Write(ct)
	hb, _ := channelKeyHashByte(keyHex)
	payload := append([]byte{hb}, m.Sum(nil)[:2]...)
	payload = append(payload, ct...)
	return "15" + "00" + hex.EncodeToString(payload)
}

func encTag(t *testing.T, keyHex string) string {
	t.Helper()
	hb, ok := channelKeyHashByte(keyHex)
	if !ok {
		t.Fatalf("no hash byte for %s", keyHex)
	}
	return fmt.Sprintf("enc_%02X", hb)
}

// testFirstSeen is the first_seen insertTx stamps on every row.
var testFirstSeen = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func testCatalogSettings() catalogKeySettings {
	return catalogKeySettings{
		url:            "unused",
		refresh:        time.Hour,
		channels:       true,
		regions:        true,
		channelMinHits: 1,
		regionMinHits:  3,
		regionWindow:   14 * 24 * time.Hour,
		regionAlpha:    0.05,
		maxPromoted:    256,
	}
}

func newTestCatalogWorker(s *Store, set catalogKeySettings, channels map[string]string, rs *regionKeySet) (*catalogKeyWorker, *channelKeySet, *int) {
	if rs == nil {
		rs = regionSetOf()
	}
	cs := newChannelKeySet(channels, "", 0)
	kicks := new(int)
	w := newCatalogKeyWorker(s, set, cs, rs, func() { *kicks++ })
	w.now = func() time.Time { return testFirstSeen.Add(48 * time.Hour) }
	w.yield = 0
	return w, cs, kicks
}

func catalogRow(t *testing.T, s *Store, kind, name string) (status string, hits, threshold int, ok bool) {
	t.Helper()
	err := s.db.QueryRow(`SELECT status, hits, threshold FROM catalog_keys WHERE kind=? AND name=?`, kind, name).
		Scan(&status, &hits, &threshold)
	if err != nil {
		return "", 0, 0, false
	}
	return status, hits, threshold, true
}

// collidingPayload returns a payload whose code1 is identical under keys a
// and b, i.e. a transmission that is evidence for neither.
func collidingPayload(t *testing.T, a, b []byte, payloadType byte) []byte {
	t.Helper()
	for i := 0; i < 1<<22; i++ {
		p := []byte(fmt.Sprintf("collide-%d", i))
		if bytes.Equal(regionCodeBytes(a, payloadType, p), regionCodeBytes(b, payloadType, p)) {
			return p
		}
	}
	t.Fatal("no colliding payload found")
	return nil
}

// --- catalogue parsing ---

func TestParseKeyCatalogBothShapes(t *testing.T) {
	webapp := []byte(`{"channels":[{"n":"#saarland"},{"n":"#saarland"},{"n":"public","s":"8b3387e9c5cdea6ac9e5edbaa115cd72"},
		{"n":"#priv","s":"00112233445566778899aabbccddeeff"},{"n":"#broken","s":"zz"},{"n":""}],
		"regions":["de-sl","#rhein-main",{"n":"fr"},"*","has space","de-sl"]}`)
	cat, err := parseKeyCatalog(webapp)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]catalogChannel{}
	for _, c := range cat.Channels {
		got[c.Name] = c
	}
	if len(cat.Channels) != 3 {
		t.Fatalf("channels = %+v, want #saarland, public, #priv (duplicate, unreadable secret and empty name dropped)", cat.Channels)
	}
	if c := got["#saarland"]; c.Explicit || c.KeyHex != deriveHashtagChannelKey("#saarland") {
		t.Fatalf("#saarland = %+v, want derived key", c)
	}
	if c := got["#priv"]; !c.Explicit {
		t.Fatalf("#priv carries a non-derived secret, must be explicit: %+v", c)
	}
	if c := got["public"]; !c.Explicit {
		t.Fatalf("public is not a hashtag name, its secret is explicit: %+v", c)
	}
	wantRegions := []string{"de-sl", "rhein-main", "fr"}
	if fmt.Sprint(cat.Regions) != fmt.Sprint(wantRegions) {
		t.Fatalf("regions = %v, want %v", cat.Regions, wantRegions)
	}

	derived, _ := hex.DecodeString(deriveHashtagChannelKey("#bonn"))
	countries := []byte(`{"countries":{"de":[{"channel":"#bonn","key":"` + base64.StdEncoding.EncodeToString(derived) + `"},{"channel":"#koeln"}]}}`)
	cat, err = parseKeyCatalog(countries)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Channels) != 2 || cat.Channels[0].Explicit || cat.Channels[1].Explicit {
		t.Fatalf("knownChannels shape = %+v, want two derived channels (a base64 key equal to the derived one is not private)", cat.Channels)
	}
	if _, err := parseKeyCatalog([]byte(`{"channels":[],"regions":[]}`)); err == nil {
		t.Fatal("an empty catalogue must be an error, so the worker keeps the keys in force")
	}
}

// --- channel gate ---

func TestCatalogChannelConfirmedByStoredTrafficAndBackfilled(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#saarland")
	row := insertTx(t, s, grpTxtFrame(t, key, "Alice: moin"), 5, encTag(t, key), `{"type":"GRP_TXT"}`, nil)

	w, cs, kicks := newTestCatalogWorker(s, testCatalogSettings(), map[string]string{}, nil)
	cat := &keyCatalog{Channels: []catalogChannel{{Name: "#saarland", KeyHex: key}, {Name: "#nobody", KeyHex: deriveHashtagChannelKey("#nobody")}}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, hits, _, ok := catalogRow(t, s, keyBackfillKindChannel, "#saarland"); !ok || st != catalogStatusPromoted || hits != 1 {
		t.Fatalf("#saarland row = %q hits=%d ok=%v, want promoted with 1 hit", st, hits, ok)
	}
	if _, _, _, ok := catalogRow(t, s, keyBackfillKindChannel, "#nobody"); ok {
		t.Fatal("#nobody has no traffic and must not be recorded")
	}
	if cs.Snapshot()["#saarland"] != key {
		t.Fatal("promoted channel is not in the key set ingest decodes with")
	}
	if *kicks != 1 {
		t.Fatalf("backfill kicked %d times, want 1", *kicks)
	}

	// The kicked backfill decides the row exactly as ingest would.
	b := newKeyBackfiller(s, cs, nil)
	b.yield = 0
	if err := b.runKind(context.Background(), keyBackfillKindChannel); err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, s, "channel_hash", row); got != "#saarland" {
		t.Fatalf("row channel_hash = %q after backfill, want #saarland", got)
	}
}

func TestCatalogChannelRejectsImplausibleTimestamp(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#oldclock")
	tag := encTag(t, key)
	tooOld := uint32(testFirstSeen.Add(-2 * catalogTimestampPast).Unix())
	future := uint32(testFirstSeen.Add(2 * catalogTimestampFuture).Unix())
	insertTx(t, s, grpTxtFrameAt(t, key, "A: x", tooOld), 5, tag, "{}", nil)
	insertTx(t, s, grpTxtFrameAt(t, key, "B: y", future), 5, tag, "{}", nil)

	w, cs, _ := newTestCatalogWorker(s, testCatalogSettings(), map[string]string{}, nil)
	if err := w.refresh(context.Background(), &keyCatalog{Channels: []catalogChannel{{Name: "#oldclock", KeyHex: key}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.Snapshot()["#oldclock"]; ok {
		t.Fatal("a sender_timestamp outside first_seen-1y..+1d must not count as evidence")
	}
}

func TestCatalogChannelMinHits(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#twice")
	insertTx(t, s, grpTxtFrame(t, key, "A: 1"), 5, encTag(t, key), "{}", nil)
	set := testCatalogSettings()
	set.channelMinHits = 2
	w, cs, _ := newTestCatalogWorker(s, set, map[string]string{}, nil)
	cat := &keyCatalog{Channels: []catalogChannel{{Name: "#twice", KeyHex: key}}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.Snapshot()["#twice"]; ok {
		t.Fatal("one transmission must not satisfy channelMinHits=2")
	}
	insertTx(t, s, grpTxtFrame(t, key, "B: 2"), 5, encTag(t, key), "{}", nil)
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.Snapshot()["#twice"]; !ok {
		t.Fatal("two distinct transmissions satisfy channelMinHits=2")
	}
}

func TestCatalogChannelExplicitSecretsAreOptIn(t *testing.T) {
	s := newTestStore(t)
	key := "00112233445566778899aabbccddeeff"
	insertTx(t, s, grpTxtFrame(t, key, "A: private"), 5, encTag(t, key), "{}", nil)
	cat := &keyCatalog{Channels: []catalogChannel{{Name: "#priv", KeyHex: key, Explicit: true}}}

	w, cs, _ := newTestCatalogWorker(s, testCatalogSettings(), map[string]string{}, nil)
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.Snapshot()["#priv"]; ok {
		t.Fatal("a shared private key from a catalogue must stay out unless includeExplicitSecrets")
	}
	set := testCatalogSettings()
	set.includeExplicitSecrets = true
	w2, cs2, _ := newTestCatalogWorker(s, set, map[string]string{}, nil)
	if err := w2.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if cs2.Snapshot()["#priv"] != key {
		t.Fatal("with includeExplicitSecrets the confirmed private channel is promoted")
	}
}

// A catalogue name never takes over a channel the operator configured, by
// name or by secret.
func TestCatalogChannelNeverShadowsHigherTier(t *testing.T) {
	configured := map[string]string{"#mine": deriveHashtagChannelKey("#mine"), "Ops": "00112233445566778899aabbccddeeff"}
	cs := newChannelKeySet(configured, "", 0)
	added := cs.setCatalog(map[string]string{
		"#mine":  deriveHashtagChannelKey("#other-key"),
		"#alias": "00112233445566778899AABBCCDDEEFF", // Ops' secret, other case
		"#new":   deriveHashtagChannelKey("#new"),
	})
	snap := cs.Snapshot()
	if snap["#mine"] != configured["#mine"] {
		t.Fatal("catalogue replaced a configured key")
	}
	if _, ok := snap["#alias"]; ok {
		t.Fatal("catalogue re-registered a configured secret under another name")
	}
	if fmt.Sprint(added) != "[#new]" {
		t.Fatalf("added = %v, want [#new]", added)
	}
	// Clearing the tier removes only catalogue keys.
	cs.setCatalog(nil)
	if _, ok := cs.Snapshot()["#new"]; ok || len(cs.Snapshot()) != 2 {
		t.Fatalf("after clearing the tier: %v", cs.Snapshot())
	}
}

// --- region gate ---

func TestRegionEvidenceThresholdIsPopulationAware(t *testing.T) {
	// The reference run from #2107: N=2709 unknown-scope transmissions,
	// M=1924 names. Three unique hits clear alpha=0.05 there.
	if k := regionEvidenceThreshold(2709, 1924, 0.05, 3); k != 3 {
		t.Fatalf("k(2709,1924) = %d, want 3", k)
	}
	prev := 0
	for _, n := range []int{0, 2709, 20000, 200000} {
		k := regionEvidenceThreshold(n, 1924, 0.05, 3)
		if k < prev {
			t.Fatalf("threshold fell from %d to %d as traffic grew to %d", prev, k, n)
		}
		lambda := float64(n) / regionCodeSpace
		if 1924*poissonTail(lambda, k) > 0.05 {
			t.Fatalf("n=%d k=%d leaves %.3f expected false names", n, k, 1924*poissonTail(lambda, k))
		}
		if k > 3 && 1924*poissonTail(lambda, k-1) <= 0.05 {
			t.Fatalf("n=%d k=%d is not the smallest threshold", n, k)
		}
		prev = k
	}
	if k := regionEvidenceThreshold(20000, 1924, 0.05, 3); k <= 3 {
		t.Fatalf("at N=20000 a fixed 3 lets ~7 names through by chance; got k=%d", k)
	}
	// Tail sanity against the closed form for k=1.
	if got, want := poissonTail(0.5, 1), 1-math.Exp(-0.5); math.Abs(got-want) > 1e-12 {
		t.Fatalf("poissonTail(0.5,1) = %v, want %v", got, want)
	}
}

func TestCatalogRegionUniqueEvidenceOnlyAndProposedByDefault(t *testing.T) {
	s := newTestStore(t)
	rm := regionKey("#rhein-main")
	for i := 0; i < 3; i++ {
		insertTx(t, s, transportFrame(rm, 5, []byte(fmt.Sprintf("rm-%d", i))), 5, "", "{}", strp(""))
	}
	// Two names, each with two unique hits plus one transmission that
	// matches both: counting that one would lift both to the threshold.
	a, b := regionKey("#cand-a"), regionKey("#cand-b")
	for i := 0; i < 2; i++ {
		insertTx(t, s, transportFrame(a, 5, []byte(fmt.Sprintf("a-%d", i))), 5, "", "{}", strp(""))
		insertTx(t, s, transportFrame(b, 5, []byte(fmt.Sprintf("b-%d", i))), 5, "", "{}", strp(""))
	}
	insertTx(t, s, transportFrame(a, 5, collidingPayload(t, a, b, 5)), 5, "", "{}", strp(""))
	// Already named: not unknown-scope, not evidence.
	insertTx(t, s, transportFrame(a, 5, []byte("named")), 5, "", "{}", strp("#elsewhere"))

	rs := regionSetOf()
	w, _, kicks := newTestCatalogWorker(s, testCatalogSettings(), nil, rs)
	cat := &keyCatalog{Regions: []string{"rhein-main", "cand-a", "cand-b"}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, hits, k, ok := catalogRow(t, s, keyBackfillKindRegion, "#rhein-main"); !ok || st != catalogStatusProposed || hits != 3 || k != 3 {
		t.Fatalf("#rhein-main = %q hits=%d k=%d ok=%v, want proposed, 3 hits, threshold 3", st, hits, k, ok)
	}
	for _, n := range []string{"#cand-a", "#cand-b"} {
		if _, _, _, ok := catalogRow(t, s, keyBackfillKindRegion, n); ok {
			t.Fatalf("%s reached the threshold only through an ambiguous transmission", n)
		}
	}
	if _, ok := rs.snapshot().all["#rhein-main"]; ok || *kicks != 0 {
		t.Fatal("without autoPromoteRegions a confirmed region is proposed, not put in force")
	}

	set := testCatalogSettings()
	set.autoPromoteRegions = true
	w.set = set
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindRegion, "#rhein-main"); st != catalogStatusPromoted {
		t.Fatalf("with autoPromoteRegions the proposal is promoted, got %q", st)
	}
	snap := rs.snapshot()
	if !snap.isCatalog("#rhein-main") || snap.isExplicit("#rhein-main") {
		t.Fatal("promoted region must sit in the catalogue tier")
	}
	if *kicks != 1 {
		t.Fatalf("backfill kicked %d times, want 1", *kicks)
	}
}

func TestCatalogRegionIgnoresTrafficKeysInForceMatch(t *testing.T) {
	s := newTestStore(t)
	saar := regionKey("#saar")
	cand := regionKey("#cand")
	// Rows matching a key in force (stored before the key existed, waiting
	// for the backfill) say nothing about catalogue names, even colliding ones.
	insertTx(t, s, transportFrame(saar, 5, collidingPayload(t, saar, cand, 5)), 5, "", "{}", strp(""))
	insertTx(t, s, transportFrame(saar, 5, []byte("saar only")), 5, "", "{}", strp(""))
	w, _, _ := newTestCatalogWorker(s, testCatalogSettings(), nil, regionSetOf("#saar"))
	g, err := w.gateRegions(context.Background(), []string{"saar", "cand"})
	if err != nil {
		t.Fatal(err)
	}
	if g.m != 1 {
		t.Fatalf("m = %d, want 1: a name already in force is not a candidate", g.m)
	}
	if g.n != 0 || len(g.evidence) != 0 {
		t.Fatalf("n=%d evidence=%v, want nothing counted", g.n, g.evidence)
	}
}

func TestCatalogRegionWindow(t *testing.T) {
	s := newTestStore(t)
	rm := regionKey("#rhein-main")
	for i := 0; i < 3; i++ {
		insertTx(t, s, transportFrame(rm, 5, []byte(fmt.Sprintf("rm-%d", i))), 5, "", "{}", strp(""))
	}
	w, _, _ := newTestCatalogWorker(s, testCatalogSettings(), nil, nil)
	w.now = func() time.Time { return testFirstSeen.Add(30 * 24 * time.Hour) } // rows are outside 14 days
	g, err := w.gateRegions(context.Background(), []string{"rhein-main"})
	if err != nil {
		t.Fatal(err)
	}
	if g.n != 0 || len(g.evidence) != 0 {
		t.Fatalf("rows older than the window were counted: n=%d", g.n)
	}
}

// --- tiers and tie-breaks ---

// code1 as the decoder reports it: uppercase hex.
func TestRegionMatchCatalogNeverWinsTieBreak(t *testing.T) {
	derived, cat, cat2 := regionKey("#derived"), regionKey("#from-catalog"), regionKey("#from-catalog-2")
	p := collidingPayload(t, derived, cat, 5)
	code := strings.ToUpper(hex.EncodeToString(regionCodeBytes(derived, 5, p)))

	rs := &regionKeySet{enabled: true, max: 10}
	rs.cur.Store(&regionKeySnapshot{all: map[string][]byte{}, explicit: map[string]bool{}, catalog: map[string]bool{}})
	rs.refreshDerived([]string{"derived"})
	rs.setCatalog(map[string][]byte{"#from-catalog": cat})
	if m := rs.snapshot().match(5, p, code); m.Name != "#derived" || m.Reason != scopeReasonOverCatalog {
		t.Fatalf("derived vs catalogue = %+v, want #derived (over-catalog)", m)
	}

	// Two catalogue names and nothing else: no principled winner.
	p2 := collidingPayload(t, cat, cat2, 5)
	code2 := strings.ToUpper(hex.EncodeToString(regionCodeBytes(cat, 5, p2)))
	rs2 := regionSetOf()
	rs2.setCatalog(map[string][]byte{"#from-catalog": cat, "#from-catalog-2": cat2})
	if m := rs2.snapshot().match(5, p2, code2); m.Name != "" || m.Reason != scopeReasonAmbiguous {
		t.Fatalf("catalogue vs catalogue = %+v, want ambiguous", m)
	}

	// A lone catalogue match names the packet: that is what promotion is for.
	lone := []byte("only catalogue")
	if m := rs2.snapshot().match(5, lone, strings.ToUpper(hex.EncodeToString(regionCodeBytes(cat2, 5, lone)))); m.Name != "#from-catalog-2" {
		t.Fatalf("unique catalogue match = %+v", m)
	}
}

func TestRegionTiersSurviveEachOthersRefresh(t *testing.T) {
	cfg := &Config{HashRegions: []string{"#explicit"}, AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	rs := newRegionKeySet(cfg)
	rs.setCatalog(map[string][]byte{"#cat": regionKey("#cat"), "#explicit": regionKey("#explicit"), "#later-derived": regionKey("#later-derived")})
	rs.refreshDerived([]string{"later-derived"})
	snap := rs.snapshot()
	if !snap.isCatalog("#cat") {
		t.Fatal("a derived refresh dropped the catalogue tier")
	}
	if snap.isCatalog("#explicit") || !snap.isExplicit("#explicit") {
		t.Fatal("an explicit name must stay explicit when the catalogue lists it too")
	}
	if snap.isCatalog("#later-derived") {
		t.Fatal("a name that becomes declared moves up to the derived tier")
	}
	rs.setCatalog(nil)
	snap = rs.snapshot()
	if _, ok := snap.all["#cat"]; ok {
		t.Fatal("clearing the catalogue tier left its key in force")
	}
	if _, ok := snap.all["#later-derived"]; !ok {
		t.Fatal("clearing the catalogue tier dropped a derived key")
	}
}

// --- persistence ---

func TestCatalogPromotionIsStickyAndReloadedAtStartup(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#sticky")
	row := insertTx(t, s, grpTxtFrame(t, key, "A: hi"), 5, encTag(t, key), "{}", nil)
	set := testCatalogSettings()
	w, _, _ := newTestCatalogWorker(s, set, map[string]string{}, nil)
	cat := &keyCatalog{Channels: []catalogChannel{{Name: "#sticky", KeyHex: key}}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	// The evidence is gone (the backfill decrypted the row); promotion stays.
	if _, err := s.db.Exec(`UPDATE transmissions SET channel_hash='#sticky' WHERE id=?`, row); err != nil {
		t.Fatal(err)
	}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindChannel, "#sticky"); st != catalogStatusPromoted {
		t.Fatalf("promotion must be sticky, got %q", st)
	}

	// A fresh process: apply alone, before any fetch, restores the tier.
	w2, cs2, _ := newTestCatalogWorker(s, set, map[string]string{}, nil)
	if err := w2.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cs2.Snapshot()["#sticky"] != key {
		t.Fatal("promoted keys are not reloaded at startup")
	}
	// Switching channels off keeps the row but not the key.
	set.channels = false
	w3, cs3, _ := newTestCatalogWorker(s, set, map[string]string{}, nil)
	if err := w3.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := cs3.Snapshot()["#sticky"]; ok {
		t.Fatal("channels:false must keep promoted channels out of force")
	}
}

func TestCatalogStaleProposalsAreDropped(t *testing.T) {
	s := newTestStore(t)
	rm := regionKey("#rhein-main")
	ids := []int64{}
	for i := 0; i < 3; i++ {
		ids = append(ids, insertTx(t, s, transportFrame(rm, 5, []byte(fmt.Sprintf("rm-%d", i))), 5, "", "{}", strp("")))
	}
	w, _, _ := newTestCatalogWorker(s, testCatalogSettings(), nil, nil)
	cat := &keyCatalog{Regions: []string{"rhein-main"}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := catalogRow(t, s, keyBackfillKindRegion, "#rhein-main"); !ok {
		t.Fatal("expected a proposal")
	}
	if _, err := s.db.Exec(`DELETE FROM transmissions WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := catalogRow(t, s, keyBackfillKindRegion, "#rhein-main"); ok {
		t.Fatal("a proposal the current window no longer supports must be dropped")
	}
}

func TestCatalogKeysSettingsDefaults(t *testing.T) {
	var cfg Config
	if cfg.CatalogKeysEnabled() {
		t.Fatal("absent block must be off")
	}
	cfg.CatalogKeys = &CatalogKeysConfig{Enabled: true}
	if cfg.CatalogKeysEnabled() {
		t.Fatal("enabled without a url must stay off")
	}
	cfg.CatalogKeys.URL = " https://example.invalid/catalog.json "
	if !cfg.CatalogKeysEnabled() {
		t.Fatal("enabled with a url")
	}
	s := cfg.CatalogKeysSettings()
	if !s.channels || !s.regions || s.autoPromoteRegions || s.includeExplicitSecrets {
		t.Fatalf("switch defaults wrong: %+v", s)
	}
	if s.regionMinHits != 3 || s.channelMinHits != 1 || s.regionWindow != 14*24*time.Hour || s.regionAlpha != 0.05 || s.refresh != 24*time.Hour || s.maxPromoted != 256 {
		t.Fatalf("numeric defaults wrong: %+v", s)
	}
	off := false
	cfg.CatalogKeys.Regions = &off
	cfg.CatalogKeys.RegionAlpha = 2
	if s := cfg.CatalogKeysSettings(); s.regions || s.regionAlpha != 0.05 {
		t.Fatalf("regions:false / out-of-range alpha: %+v", s)
	}
}

// config.example.json is copied to config.json on a fresh install
// (docker/entrypoint.sh), so its block must leave the feature off.
func TestCatalogKeys_ShippedDefaultIsOff(t *testing.T) {
	cfg, err := LoadConfig("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CatalogKeys == nil {
		t.Fatal("config.example.json should document the catalogKeys block")
	}
	if cfg.CatalogKeysEnabled() {
		t.Fatal("catalogKeys must ship disabled")
	}
}

// --- review follow-ups (#2366) ---

// A 256-bit channel secret: AES-128 on its first half, the MAC and the
// channel hash over all 32 bytes (firmware BaseChatMesh::setChannel,
// Utils::MACThenDecrypt).
func TestCatalogSecretHexAccepts256BitSecrets(t *testing.T) {
	k32 := "00112233445566778899aabbccddeeff0123456789abcdeffedcba9876543210"
	if got, ok := catalogSecretHex(strings.ToUpper(k32)); !ok || got != k32 {
		t.Fatalf("32-byte hex = %q ok=%v, want %q", got, ok, k32)
	}
	raw, _ := hex.DecodeString(k32)
	if got, ok := catalogSecretHex(base64.StdEncoding.EncodeToString(raw)); !ok || got != k32 {
		t.Fatalf("32-byte base64 = %q ok=%v", got, ok)
	}
	// Upper half zero: that is the 128-bit key, and it must fingerprint and
	// hash like one.
	k16 := "00112233445566778899aabbccddeeff"
	if got, ok := catalogSecretHex(k16 + strings.Repeat("0", 32)); !ok || got != k16 {
		t.Fatalf("zero-padded 32-byte secret = %q ok=%v, want the 16-byte key", got, ok)
	}
	if _, ok := catalogSecretHex(strings.Repeat("ab", 24)); ok {
		t.Fatal("a 24-byte secret is neither size the firmware accepts")
	}
	hb16, _ := channelKeyHashByte(k16)
	hbPadded, _ := channelKeyHashByte(k16 + strings.Repeat("0", 32))
	if hb16 != hbPadded {
		t.Fatal("a zero-padded 32-byte secret must hash as its 16-byte key")
	}
	h := sha256.Sum256(raw)
	if hb, ok := channelKeyHashByte(k32); !ok || hb != h[0] {
		t.Fatalf("channel hash of a 256-bit secret = %02X ok=%v, want sha256(all 32 bytes)[0] = %02X", hb, ok, h[0])
	}
}

func TestCatalogChannel256BitSecretConfirmedAndDecoded(t *testing.T) {
	s := newTestStore(t)
	k32 := "8f1e2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff0"
	frame := grpTxtFrameAt(t, k32, "Op: twofiftysix", uint32(testFirstSeen.Unix()))
	insertTx(t, s, frame, 5, encTag(t, k32), "{}", nil)
	set := testCatalogSettings()
	set.includeExplicitSecrets = true
	w, cs, kicks := newTestCatalogWorker(s, set, map[string]string{}, nil)
	cat, err := parseKeyCatalog([]byte(`{"channels":[{"n":"#wide","s":"` + k32 + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Channels) != 1 || cat.Channels[0].KeyHex != k32 || !cat.Channels[0].Explicit {
		t.Fatalf("parsed %+v, want one explicit 256-bit candidate", cat.Channels)
	}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, hits, _, _ := catalogRow(t, s, keyBackfillKindChannel, "#wide"); st != catalogStatusPromoted || hits != 1 {
		t.Fatalf("#wide = %q hits=%d, want promoted on 1 hit", st, hits)
	}
	if cs.Snapshot()["#wide"] != k32 || *kicks != 1 {
		t.Fatalf("256-bit key not put in force (kicks=%d)", *kicks)
	}
	d, err := DecodePacket(frame, map[string]string{"#wide": k32}, false)
	if err != nil || d.Payload.Text != "Op: twofiftysix" {
		t.Fatalf("decode with a 256-bit secret: %+v err=%v", d.Payload, err)
	}
}

func TestCatalogFetchHTTPAndSizeCap(t *testing.T) {
	body := []byte(`{"regions":["de-sl"]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			rw.Write(body)
		case "/exact":
			rw.Write(bytes.Repeat([]byte(" "), catalogMaxBytes))
		case "/big":
			rw.Write(bytes.Repeat([]byte(" "), catalogMaxBytes+1))
		default:
			http.Error(rw, "nope", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	w, _, _ := newTestCatalogWorker(newTestStore(t), testCatalogSettings(), nil, nil)
	fetch := func(src string) ([]byte, error) {
		w.set.url = src
		return w.fetch(context.Background())
	}
	if got, err := fetch(srv.URL + "/ok"); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("GET /ok = %q, %v", got, err)
	}
	if _, err := fetch(srv.URL + "/fail"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("non-200 must fail, got %v", err)
	}
	if got, err := fetch(srv.URL + "/exact"); err != nil || len(got) != catalogMaxBytes {
		t.Fatalf("a catalogue of exactly the cap must be read: len=%d err=%v", len(got), err)
	}
	if _, err := fetch(srv.URL + "/big"); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a catalogue over the cap must be refused, got %v", err)
	}
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), catalogMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fetch(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a local file over the cap must be refused, got %v", err)
	}
	// A failed fetch leaves the promoted tier alone.
	w.set.url = srv.URL + "/fail"
	w.runOnce(context.Background())
}

func TestCatalogAutoPromoteOffKeepsPromotedRegions(t *testing.T) {
	s := newTestStore(t)
	first, second := regionKey("#first"), regionKey("#second")
	for i := 0; i < 3; i++ {
		insertTx(t, s, transportFrame(first, 5, []byte(fmt.Sprintf("first-%d", i))), 5, "", "{}", strp(""))
	}
	on := testCatalogSettings()
	on.autoPromoteRegions = true
	rs := regionSetOf()
	w, _, _ := newTestCatalogWorker(s, on, nil, rs)
	cat := &keyCatalog{Regions: []string{"first", "second"}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindRegion, "#first"); st != catalogStatusPromoted {
		t.Fatalf("#first = %q, want promoted", st)
	}

	// Switched off, with evidence for a second name arriving afterwards.
	for i := 0; i < 3; i++ {
		insertTx(t, s, transportFrame(second, 5, []byte(fmt.Sprintf("second-%d", i))), 5, "", "{}", strp(""))
	}
	w2, _, _ := newTestCatalogWorker(s, testCatalogSettings(), nil, rs)
	if err := w2.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w2.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindRegion, "#first"); st != catalogStatusPromoted {
		t.Fatalf("switching autoPromoteRegions off demoted #first to %q", st)
	}
	if !rs.snapshot().isCatalog("#first") {
		t.Fatal("switching autoPromoteRegions off took #first out of force")
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindRegion, "#second"); st != catalogStatusProposed {
		t.Fatalf("#second = %q, want proposed once autoPromoteRegions is off", st)
	}
	if _, ok := rs.snapshot().all["#second"]; ok {
		t.Fatal("a proposal must not be in force")
	}
}

// TestCatalogRegionRevertProcedure runs the steps in docs/catalogue-keys.md:
// reject the name, un-name its history, forget the region passes, restart.
func TestCatalogRegionRevertProcedure(t *testing.T) {
	s := newTestStore(t)
	wrong := regionKey("#wrong")
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, insertTx(t, s, transportFrame(wrong, 5, []byte(fmt.Sprintf("w-%d", i))), 5, "", "{}", strp("")))
	}
	set := testCatalogSettings()
	set.autoPromoteRegions = true
	rs := regionSetOf("#kept")
	w, _, _ := newTestCatalogWorker(s, set, nil, rs)
	cat := &keyCatalog{Regions: []string{"wrong"}}
	if err := w.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	b := newTestBackfiller(s, nil, rs)
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	scope := func(id int64) string {
		var sc string
		if err := s.db.QueryRow(`SELECT scope_name FROM transmissions WHERE id=?`, id).Scan(&sc); err != nil {
			t.Fatal(err)
		}
		return sc
	}
	if scope(ids[0]) != "#wrong" {
		t.Fatalf("setup: backfill should have named history #wrong, got %q", scope(ids[0]))
	}

	// The documented SQL, with the ingestor stopped.
	for _, q := range []string{
		`UPDATE catalog_keys SET status='rejected' WHERE kind='region' AND name='#wrong'`,
		`UPDATE transmissions SET scope_name='' WHERE scope_name='#wrong'`,
		`DELETE FROM key_backfill_done WHERE kind='region'`,
		`DELETE FROM key_backfill_state WHERE kind='region'`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// Restart: apply before ingest, then the backfill pass.
	rs2 := regionSetOf("#kept")
	w2, _, kicks := newTestCatalogWorker(s, set, nil, rs2)
	if err := w2.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := rs2.snapshot().all["#wrong"]; ok || *kicks != 0 {
		t.Fatal("a rejected name must not be loaded at startup")
	}
	b2 := newTestBackfiller(s, nil, rs2)
	if err := b2.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	var done int
	s.db.QueryRow(`SELECT COUNT(*) FROM key_backfill_done WHERE kind='region'`).Scan(&done)
	if done != 1 {
		t.Fatalf("the remaining region key must get a fresh pass, key_backfill_done has %d row(s)", done)
	}
	for _, id := range ids {
		if sc := scope(id); sc != "" {
			t.Fatalf("row %d still named %q after the revert", id, sc)
		}
	}

	// The next catalogue run sees the same evidence and leaves it alone.
	if err := w2.refresh(context.Background(), cat); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindRegion, "#wrong"); st != catalogStatusRejected {
		t.Fatalf("#wrong = %q after a refresh, want it to stay rejected", st)
	}
	if _, ok := rs2.snapshot().all["#wrong"]; ok {
		t.Fatal("a rejected name was put back in force")
	}
}

func TestCatalogRejectedChannelIsSkipped(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#nope")
	insertTx(t, s, grpTxtFrame(t, key, "A: hi"), 5, encTag(t, key), "{}", nil)
	if _, err := s.db.Exec(`INSERT INTO catalog_keys(kind, name, key_hex, status) VALUES('channel','#nope',?,'rejected')`, key); err != nil {
		t.Fatal(err)
	}
	w, cs, _ := newTestCatalogWorker(s, testCatalogSettings(), map[string]string{}, nil)
	if err := w.refresh(context.Background(), &keyCatalog{Channels: []catalogChannel{{Name: "#nope", KeyHex: key}}}); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := catalogRow(t, s, keyBackfillKindChannel, "#nope"); st != catalogStatusRejected {
		t.Fatalf("#nope = %q, want rejected", st)
	}
	if _, ok := cs.Snapshot()["#nope"]; ok {
		t.Fatal("a rejected channel was put in force")
	}
}

// regionMatcher must decide exactly like matchingRegions, collisions included.
func TestRegionMatcherAgreesWithMatchingRegions(t *testing.T) {
	keys := map[string][]byte{}
	for i := 0; i < 64; i++ {
		n := fmt.Sprintf("#r%02d", i)
		keys[n] = regionKey(n)
	}
	a, b := keys["#r01"], keys["#r02"]
	payloads := [][]byte{collidingPayload(t, a, b, 5), []byte("x")}
	for i := 0; i < 200; i++ {
		payloads = append(payloads, []byte(fmt.Sprintf("payload-%d", i)))
	}
	m := newRegionMatcher(keys)
	var out []string
	for _, p := range payloads {
		for _, pt := range []byte{2, 5} {
			code := regionCodeBytes(a, pt, p)
			code1 := strings.ToUpper(hex.EncodeToString(code))
			want := matchingRegions(keys, pt, p, code1)
			sortStrings(want)
			v, ok := parseCode1(code1)
			if !ok {
				t.Fatalf("parseCode1(%q) failed", code1)
			}
			out = m.match(pt, p, v, out)
			if fmt.Sprint(out) != fmt.Sprint(want) {
				t.Fatalf("payload %q type %d: matcher %v, matchingRegions %v", p, pt, out, want)
			}
			if m.any(pt, p, v) != (len(want) > 0) {
				t.Fatalf("any() disagrees for %q", p)
			}
		}
	}
	if _, ok := parseCode1("zz12"); ok {
		t.Fatal("parseCode1 accepted non-hex")
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// BenchmarkRegionGatePerTransmission is the region gate's inner loop: one
// unknown-scope transmission against the #2107 reference catalogue size
// (1,924 names). A gate run costs rows x this; catalogRegionScanCap bounds
// rows at 250,000.
func BenchmarkRegionGatePerTransmission(b *testing.B) {
	const names = 1924
	keys := make(map[string][]byte, names)
	for i := 0; i < names; i++ {
		n := fmt.Sprintf("#bench-%04d", i)
		keys[n] = regionKey(n)
	}
	payload := bytes.Repeat([]byte{0xA5}, 48) // a typical GRP_TXT payload
	code1 := strings.ToUpper(hex.EncodeToString(regionCodeBytes(regionKey("#elsewhere"), 5, payload)))
	v, _ := parseCode1(code1)
	b.Run("matchingRegions", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			matchingRegions(keys, 5, payload, code1)
		}
	})
	b.Run("regionMatcher", func(b *testing.B) {
		m := newRegionMatcher(keys)
		var out []string
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out = m.match(5, payload, v, out)
		}
	})
}
