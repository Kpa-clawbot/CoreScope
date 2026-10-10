package main

import (
	"context"
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- fixture builders: real wire frames, encoded the way firmware does ---

// grpTxtFrame builds a FLOOD GRP_TXT encrypted under keyHex, exactly as
// decryptChannelMessage expects it: AES-128-ECB over ts(4 LE)+flags(1)+text,
// zero-padded, MAC = first two bytes of HMAC-SHA256(key||16 zero bytes).
func grpTxtFrame(t *testing.T, keyHex, text string) string {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 16 {
		t.Fatalf("bad key %q", keyHex)
	}
	plain := make([]byte, 5, 64)
	binary.LittleEndian.PutUint32(plain[0:4], 1767225600)
	plain = append(plain, []byte(text)...)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, 0)
	}
	block, _ := aes.NewCipher(key)
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	secret := make([]byte, 32)
	copy(secret, key)
	m := hmac.New(sha256.New, secret)
	m.Write(ct)
	mac := m.Sum(nil)[:2]
	hb, _ := channelKeyHashByte(keyHex)
	payload := append([]byte{hb}, mac...)
	payload = append(payload, ct...)
	return "15" + "00" + hex.EncodeToString(payload) // FLOOD|GRP_TXT, no hops
}

// regionCodeBytes is the transport code1 a sender in region key would put on
// payload - the inverse of matchingRegions, kept independent of it on purpose.
func regionCodeBytes(key []byte, payloadType byte, payload []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte{payloadType})
	m.Write(payload)
	sum := m.Sum(nil)
	code := uint16(sum[0]) | uint16(sum[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	return []byte{byte(code & 0xFF), byte(code >> 8)}
}

func regionKey(name string) []byte {
	h := sha256.Sum256([]byte(name))
	return h[:16]
}

// transportFrame builds a TRANSPORT_FLOOD frame of payloadType whose code1 is
// what region key would produce for payload.
func transportFrame(key []byte, payloadType byte, payload []byte) string {
	header := []byte{payloadType << 2} // route type 0 = TRANSPORT_FLOOD
	code1 := regionCodeBytes(key, payloadType, payload)
	return hex.EncodeToString(header) + hex.EncodeToString(code1) + "0000" + "00" + hex.EncodeToString(payload)
}

// advertPayload is the smallest ADVERT the decoder accepts: pubkey(32) +
// timestamp(4) + signature(64) + flags(1, repeater) + name.
func advertPayload(pubkey byte, name string) []byte {
	p := make([]byte, 0, 110)
	for i := 0; i < 32; i++ {
		p = append(p, pubkey)
	}
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, 1767225600)
	p = append(p, ts...)
	p = append(p, make([]byte, 64)...)
	p = append(p, 0x02)
	return append(p, []byte(name)...)
}

// insertTx writes a row the way InsertTransmission leaves one: scope is nil
// for a non-transport row, "" for unmatched, "#name" for matched.
func insertTx(t *testing.T, s *Store, raw string, payloadType int, channel string, decoded string, scope *string) int64 {
	t.Helper()
	var ch interface{}
	if channel != "" {
		ch = channel
	}
	res, err := s.db.Exec(`INSERT INTO transmissions(raw_hex, hash, first_seen, payload_type, decoded_json, channel_hash, scope_name)
		VALUES(?,?,?,?,?,?,?)`, raw, fmt.Sprintf("kb-%d", txSeq.Add(1)), "2026-10-01T00:00:00Z",
		payloadType, decoded, ch, scope)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// txSeq keeps fixture hashes unique; transmissions.hash is UNIQUE.
var txSeq atomic.Int64

func strp(s string) *string { return &s }

func regionSetOf(names ...string) *regionKeySet {
	all := map[string][]byte{}
	for _, n := range names {
		all[n] = regionKey(n)
	}
	s := &regionKeySet{}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: map[string]bool{}})
	return s
}

func newTestBackfiller(s *Store, channelKeys map[string]string, rs *regionKeySet) *keyBackfiller {
	b := newKeyBackfiller(s, channelKeys, rs)
	b.yield = 0
	return b
}

func feedRows(t *testing.T, s *Store) []int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT tx_id FROM tx_rewrite_feed ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func colOf(t *testing.T, s *Store, col string, id int64) string {
	t.Helper()
	var v *string
	if err := s.db.QueryRow(`SELECT `+col+` FROM transmissions WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return "<NULL>"
	}
	return *v
}

// --- channels ---

func TestKeyBackfillChannelDecryptsOnlyUnresolvedRows(t *testing.T) {
	s := newTestStore(t)
	saar := deriveHashtagChannelKey("#saar")
	other := deriveHashtagChannelKey("#nobody-has-this")

	encSaar := grpTxtFrame(t, saar, "Alice: moin")
	hb, _ := channelKeyHashByte(saar)
	encTag := fmt.Sprintf("enc_%02X", hb)
	resolvable := insertTx(t, s, encSaar, 5, encTag, `{"type":"GRP_TXT"}`, nil)

	// Already decrypted: never touched, even though the key would decrypt it.
	already := insertTx(t, s, encSaar+"00", 5, "#saar", `{"type":"CHAN","keep":1}`, nil)

	// A different, still-unknown channel stays encrypted.
	encOther := grpTxtFrame(t, other, "Bob: hi")
	hbo, _ := channelKeyHashByte(other)
	unknown := insertTx(t, s, encOther, 5, fmt.Sprintf("enc_%02X", hbo), `{"type":"GRP_TXT"}`, nil)

	b := newTestBackfiller(s, map[string]string{"#saar": saar}, nil)
	if err := b.runKind(context.Background(), keyBackfillKindChannel); err != nil {
		t.Fatal(err)
	}

	if got := colOf(t, s, "channel_hash", resolvable); got != "#saar" {
		t.Fatalf("resolvable row channel_hash = %q, want #saar", got)
	}
	if dj := colOf(t, s, "decoded_json", resolvable); !strings.Contains(dj, `"type":"CHAN"`) || !strings.Contains(dj, "moin") {
		t.Fatalf("decoded_json not what ingest would store: %s", dj)
	}
	if got := colOf(t, s, "decoded_json", already); got != `{"type":"CHAN","keep":1}` {
		t.Fatalf("already-decrypted row was rewritten: %s", got)
	}
	if got := colOf(t, s, "channel_hash", unknown); !strings.HasPrefix(got, "enc_") {
		t.Fatalf("row with no key was touched: %s", got)
	}
	if f := feedRows(t, s); len(f) != 1 || f[0] != resolvable {
		t.Fatalf("feed = %v, want exactly [%d]", f, resolvable)
	}
}

// Same decision as ingest: a backfilled row's decoded_json is byte-for-byte
// what BuildPacketData would have stored had the key been known at insert.
func TestKeyBackfillChannelMatchesIngestDecision(t *testing.T) {
	s := newTestStore(t)
	key := deriveHashtagChannelKey("#parity")
	raw := grpTxtFrame(t, key, "Carol: same bytes")
	hb, _ := channelKeyHashByte(key)
	id := insertTx(t, s, raw, 5, fmt.Sprintf("enc_%02X", hb), `{"type":"GRP_TXT"}`, nil)

	keys := map[string]string{"#parity": key}
	b := newTestBackfiller(s, keys, nil)
	if err := b.runKind(context.Background(), keyBackfillKindChannel); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePacket(raw, keys, false)
	if err != nil {
		t.Fatal(err)
	}
	pd := BuildPacketData(&MQTTPacketMessage{Raw: raw}, decoded, "obs", "", nil)
	if got := colOf(t, s, "decoded_json", id); got != pd.DecodedJSON {
		t.Fatalf("backfill decoded_json differs from ingest:\n got %s\nwant %s", got, pd.DecodedJSON)
	}
	if got := colOf(t, s, "channel_hash", id); got != pd.ChannelHash {
		t.Fatalf("backfill channel_hash %q, ingest %q", got, pd.ChannelHash)
	}
}

// --- regions ---

func TestKeyBackfillRegionNamesUniqueAndAbstainsOnAmbiguity(t *testing.T) {
	s := newTestStore(t)
	payload := []byte("transport-scoped payload")

	// Find two names whose codes collide on this exact payload: the
	// ambiguous case #1609 says must stay unnamed.
	seen := map[string]string{}
	var a, c string
	for i := 0; a == ""; i++ {
		n := fmt.Sprintf("#r%d", i)
		code := hex.EncodeToString(regionCodeBytes(regionKey(n), 5, payload))
		if prev, ok := seen[code]; ok {
			a, c = prev, n
		}
		seen[code] = n
	}

	uniqueRow := insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte("only saar")), 5, "", "{}", strp(""))
	ambiguous := insertTx(t, s, transportFrame(regionKey(a), 5, payload), 5, "", "{}", strp(""))
	named := insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte("named already")), 5, "", "{}", strp("#elsewhere"))
	notTransport := insertTx(t, s, "1500"+hex.EncodeToString([]byte("plain flood")), 5, "", "{}", nil)

	b := newTestBackfiller(s, nil, regionSetOf("#saar", a, c))
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, s, "scope_name", uniqueRow); got != "#saar" {
		t.Fatalf("unique match = %q, want #saar", got)
	}
	if got := colOf(t, s, "scope_name", ambiguous); got != "" {
		t.Fatalf("ambiguous collision between %s and %s was named %q; must stay unmatched", a, c, got)
	}
	if got := colOf(t, s, "scope_name", named); got != "#elsewhere" {
		t.Fatalf("already-named row rewritten to %q", got)
	}
	if got := colOf(t, s, "scope_name", notTransport); got != "<NULL>" {
		t.Fatalf("non-transport row got scope %q", got)
	}
	if f := feedRows(t, s); len(f) != 1 || f[0] != uniqueRow {
		t.Fatalf("feed = %v, want [%d]", f, uniqueRow)
	}
}

// The backfill must not pollute the ingest-path tally that the tier-3 spec is
// gated on: those counters describe live traffic.
func TestKeyBackfillDoesNotTouchScopeMatchCounters(t *testing.T) {
	s := newTestStore(t)
	insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte("x")), 5, "", "{}", strp(""))
	before := scopeMatchCounters.unique.Load() + scopeMatchCounters.none.Load()
	b := newTestBackfiller(s, nil, regionSetOf("#saar"))
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if after := scopeMatchCounters.unique.Load() + scopeMatchCounters.none.Load(); after != before {
		t.Fatalf("scope match counters moved by %d", after-before)
	}
}

func TestKeyBackfillAdvertFillsOnlyEmptyDefaultScope(t *testing.T) {
	s := newTestStore(t)
	pkEmpty := strings.Repeat("aa", 32)
	pkKnown := strings.Repeat("bb", 32)
	for _, q := range []string{
		`INSERT INTO nodes(public_key, name) VALUES('` + pkEmpty + `','empty')`,
		`INSERT INTO nodes(public_key, name, default_scope) VALUES('` + pkKnown + `','known','#newer')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	insertTx(t, s, transportFrame(regionKey("#saar"), 4, advertPayload(0xaa, "E")), 4, "", "{}", strp(""))
	insertTx(t, s, transportFrame(regionKey("#saar"), 4, advertPayload(0xbb, "K")), 4, "", "{}", strp(""))

	b := newTestBackfiller(s, nil, regionSetOf("#saar"))
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	var e, k string
	s.db.QueryRow(`SELECT COALESCE(default_scope,'') FROM nodes WHERE public_key=?`, pkEmpty).Scan(&e)
	s.db.QueryRow(`SELECT COALESCE(default_scope,'') FROM nodes WHERE public_key=?`, pkKnown).Scan(&k)
	if e != "#saar" {
		t.Fatalf("empty default_scope = %q, want #saar", e)
	}
	if k != "#newer" {
		t.Fatalf("known default_scope overwritten with %q", k)
	}
}

// --- lifecycle: idempotence, resume, key-set growth ---

func TestKeyBackfillIsIdempotentAndRecordsDoneKeys(t *testing.T) {
	s := newTestStore(t)
	insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte("one")), 5, "", "{}", strp(""))
	b := newTestBackfiller(s, nil, regionSetOf("#saar"))
	for i := 0; i < 2; i++ {
		if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
			t.Fatal(err)
		}
	}
	if f := feedRows(t, s); len(f) != 1 {
		t.Fatalf("second pass rewrote again: feed=%v", f)
	}
	var done, state int
	s.db.QueryRow(`SELECT COUNT(*) FROM key_backfill_done WHERE kind='region'`).Scan(&done)
	s.db.QueryRow(`SELECT COUNT(*) FROM key_backfill_state`).Scan(&state)
	if done != 1 || state != 0 {
		t.Fatalf("done=%d state=%d, want 1 and 0", done, state)
	}
}

func TestKeyBackfillResumesAfterCancelWithoutDuplicates(t *testing.T) {
	s := newTestStore(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte(fmt.Sprintf("p%d", i))), 5, "", "{}", strp("")))
	}
	rs := regionSetOf("#saar")

	// One id per window and a long yield: cancel lands between commits.
	b := newTestBackfiller(s, nil, rs)
	b.window = 1
	b.yield = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- b.runKind(ctx, keyBackfillKindRegion) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var cur int64
		s.db.QueryRow(`SELECT COALESCE((SELECT cursor FROM key_backfill_state WHERE kind='region'),0)`).Scan(&cur)
		if cur > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first batch never committed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("cancelled run reported success")
	}
	var cur, rewritten int64
	s.db.QueryRow(`SELECT cursor, rewritten FROM key_backfill_state WHERE kind='region'`).Scan(&cur, &rewritten)
	if cur != ids[0] || rewritten != 1 {
		t.Fatalf("after cancel cursor=%d rewritten=%d, want %d and 1", cur, rewritten, ids[0])
	}

	b2 := newTestBackfiller(s, nil, rs)
	if err := b2.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	f := feedRows(t, s)
	if len(f) != 5 {
		t.Fatalf("feed=%v, want each of the 5 rows exactly once", f)
	}
	for i, id := range ids {
		if f[i] != id {
			t.Fatalf("feed order %v, want %v", f, ids)
		}
	}
}

// A key added after a run started must still see the rows below the old
// cursor: a stale run fingerprint restarts the scan from the beginning.
func TestKeyBackfillKeySetGrowthRestartsFromBeginning(t *testing.T) {
	s := newTestStore(t)
	low := insertTx(t, s, transportFrame(regionKey("#new"), 5, []byte("early")), 5, "", "{}", strp(""))
	high := insertTx(t, s, transportFrame(regionKey("#new"), 5, []byte("late")), 5, "", "{}", strp(""))
	// A half-finished run for an older key set, already past `low`.
	if _, err := s.db.Exec(`INSERT INTO key_backfill_state(kind, run_fp, cursor, upper) VALUES('region','stale-set',?,?)`, low, high); err != nil {
		t.Fatal(err)
	}
	b := newTestBackfiller(s, nil, regionSetOf("#old", "#new"))
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if colOf(t, s, "scope_name", low) != "#new" || colOf(t, s, "scope_name", high) != "#new" {
		t.Fatal("a key added mid-run did not reach every row")
	}
}

func TestKeyBackfillNewKeyLaterOnlyScansForThatKey(t *testing.T) {
	s := newTestStore(t)
	first := insertTx(t, s, transportFrame(regionKey("#a"), 5, []byte("a")), 5, "", "{}", strp(""))
	second := insertTx(t, s, transportFrame(regionKey("#b"), 5, []byte("b")), 5, "", "{}", strp(""))
	rs := regionSetOf("#a")
	b := newTestBackfiller(s, nil, rs)
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if colOf(t, s, "scope_name", second) != "" {
		t.Fatal("#b named before its key existed")
	}
	rs.cur.Store(&regionKeySnapshot{all: map[string][]byte{"#a": regionKey("#a"), "#b": regionKey("#b")}, explicit: map[string]bool{}})
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if colOf(t, s, "scope_name", first) != "#a" || colOf(t, s, "scope_name", second) != "#b" {
		t.Fatal("growth pass did not name the new key's row")
	}
	if f := feedRows(t, s); len(f) != 2 {
		t.Fatalf("feed=%v, want one entry per row", f)
	}
}

func TestKeyBackfillRemovedKeyIsReplayedWhenItReturns(t *testing.T) {
	s := newTestStore(t)
	rs := regionSetOf("#a")
	b := newTestBackfiller(s, nil, rs)
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	// #a leaves the derived tier; a packet for it arrives meanwhile.
	rs.cur.Store(&regionKeySnapshot{all: map[string][]byte{}, explicit: map[string]bool{}})
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	gap := insertTx(t, s, transportFrame(regionKey("#a"), 5, []byte("while absent")), 5, "", "{}", strp(""))
	rs.cur.Store(&regionKeySnapshot{all: map[string][]byte{"#a": regionKey("#a")}, explicit: map[string]bool{}})
	if err := b.runKind(context.Background(), keyBackfillKindRegion); err != nil {
		t.Fatal(err)
	}
	if got := colOf(t, s, "scope_name", gap); got != "#a" {
		t.Fatalf("returning key did not replay the gap: %q", got)
	}
}

// A row resolved between decide and commit (live ingest, another writer)
// keeps its value and gets no feed entry.
func TestKeyBackfillCommitLeavesConcurrentlyResolvedRow(t *testing.T) {
	s := newTestStore(t)
	id := insertTx(t, s, transportFrame(regionKey("#saar"), 5, []byte("race")), 5, "", "{}", strp(""))
	b := newTestBackfiller(s, nil, regionSetOf("#saar"))
	if _, err := s.db.Exec(`INSERT INTO key_backfill_state(kind, run_fp, cursor, upper) VALUES('region','fp',0,?)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE transmissions SET scope_name='#live' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	n, err := b.commit(context.Background(), keyBackfillKindRegion, "fp", id, []keyRewrite{{id: id, scopeName: "#saar"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || colOf(t, s, "scope_name", id) != "#live" || len(feedRows(t, s)) != 0 {
		t.Fatalf("n=%d scope=%q feed=%v; concurrent resolution must win", n, colOf(t, s, "scope_name", id), feedRows(t, s))
	}
}

func TestChannelKeyHashByteMatchesBuiltinPublic(t *testing.T) {
	// builtinChannelKeys documents Public's channel-hash byte as 0x11.
	if hb, ok := channelKeyHashByte(builtinChannelKeys()["Public"]); !ok || hb != 0x11 {
		t.Fatalf("Public hash byte = %#x ok=%v, want 0x11", hb, ok)
	}
}

func TestKeyBackfillPrunesOldFeedRows(t *testing.T) {
	s := newTestStore(t)
	id := insertTx(t, s, "1500aa", 5, "", "{}", nil)
	old := time.Now().Add(-48 * time.Hour).Unix()
	if _, err := s.db.Exec(`INSERT INTO tx_rewrite_feed(tx_id, created_at) VALUES(?,?),(?,?)`, id, old, id, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	newTestBackfiller(s, nil, nil).pass(context.Background())
	if f := feedRows(t, s); len(f) != 1 {
		t.Fatalf("feed after prune = %v, want only the recent row", f)
	}
}
