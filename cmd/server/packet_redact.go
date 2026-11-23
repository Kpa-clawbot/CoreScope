package main

import (
	"encoding/json"
	"strings"
)

// Located adverts (location privacy, B5). An ADVERT whose appdata carries a
// position gives it away three ways: the lat/lon bytes in the frame, the
// lat/lon keys of its decoded JSON, and the Ed25519 signature, which covers
// pubkey | timestamp | appdata and so lets anyone brute-force the exact
// position from the approximate one. With location privacy on, the server
// serves such a packet, and every observation of it, with raw_hex null,
// decoded_json without lat, lon and signature, and "location_hidden": true
// (contract in location_privacy.go). hash and signatureValid stay.
//
// The redaction runs ONCE, where a transmission enters server memory (load,
// chunked load, poll, rewrite feed): the store keeps only the served form,
// so no serving path can reach the exact bytes. What the store's analytics
// read from the frame (route type, path byte, frame length) survives in
// hiddenFrame, which holds no payload byte. DB-fallback paths that read
// raw_hex / decoded_json straight from SQL run the same function through
// redactPacketMap / redactObservationFrame.
//
// Feature off (nil fuzzer): every function here returns its input unchanged.

// locatedAdvertHiddenKeys are the decoded-JSON keys a located advert is
// served without. They are deleted here, never read; that is why
// TestDecodedCacheDroppedKeysAreUnread exempts this declaration like the
// decodedCacheDroppedKeys one.
//
// "raw" is the payload hex the decoder keeps when an advert is too short to
// decode: in a truncated advert it holds the signature bytes the frame has.
var locatedAdvertHiddenKeys = []string{
	"lat", "lon", "signature", "raw",
}

// advertLocationFlag is ADV_LATLON_MASK (firmware AdvertDataHelpers.h): the
// appdata flags bit saying latitude and longitude follow.
const advertLocationFlag = 0x10

// advertSignatureOffset is where the signature starts in an ADVERT payload:
// pubkey(32) + timestamp(4) (firmware docs/payloads.md).
const advertSignatureOffset = 36

// advertAppdataOffset is where appdata starts in an ADVERT payload:
// pubkey(32) + timestamp(4) + signature(64) (firmware docs/payloads.md).
const advertAppdataOffset = 100

// hiddenFrame is what the store keeps of a located advert's wire frame.
type hiddenFrame struct {
	// head is the frame's header, transport codes (transport routes), path
	// length byte and path bytes, in hex: what packetpath.PathByte /
	// HashSize read, served as frame_head. It never includes a payload byte,
	// and it is its own copy, not a substring of the frame.
	head string
	// bytes is the frame length in bytes (packet-size and airtime stats).
	bytes int
}

// hiddenFrameBaseBytes is the allocation of a hiddenFrame (string header +
// int) plus the head's own string header, for estimateStoreTxBytes.
const hiddenFrameBaseBytes = 24 + 16

// locationHidden reports whether tx is a located advert served redacted.
func (tx *StoreTx) locationHidden() bool { return tx.hidden != nil }

// frameHead is the hex the frame-header readers (PathByte, HashSize) take:
// the whole raw_hex, or for a located advert the kept head. Empty when the
// store holds no frame.
func (tx *StoreTx) frameHead() string {
	if tx.hidden != nil {
		return tx.hidden.head
	}
	return tx.RawHex
}

// frameBytes is the frame length in bytes, 0 when the store holds no frame.
func (tx *StoreTx) frameBytes() int {
	if tx.hidden != nil {
		return tx.hidden.bytes
	}
	return len(tx.RawHex) / 2
}

// redactLocatedAdvert returns the served form of one transmission (or one
// observation frame): rawHex and decodedJSON unchanged, hidden nil, unless
// location privacy is on (f non-nil) and the packet is an ADVERT that carries
// a location; then rawHex is "", decodedJSON lacks locatedAdvertHiddenKeys and
// hidden holds the frame header for the analytics.
//
// payloadType is the transmissions.payload_type column; nil means unknown,
// and the frame header (or the decoded type) decides. A non-advert costs one
// integer compare. An advert's JSON is walked once (topLevelMembers).
//
// "Carries a location" fails closed: the appdata flag bit in the frame, or
// lat / lon / flags.hasLocation in the decoded JSON, or a frame (or JSON)
// that cannot be read far enough to tell. A located advert at 0,0 is hidden
// like any other; an advert without the flag keeps its bytes.
func redactLocatedAdvert(f *locationFuzzer, payloadType *int, rawHex, decodedJSON string) (outRaw, outDecoded string, hidden *hiddenFrame) {
	if f == nil {
		return rawHex, decodedJSON, nil
	}
	if payloadType != nil {
		if *payloadType != PayloadADVERT {
			return rawHex, decodedJSON, nil
		}
	} else if !untypedIsAdvert(rawHex, decodedJSON) {
		return rawHex, decodedJSON, nil
	}

	frameLoc, head := advertFrameLocation(rawHex)
	var members []jsonMember
	jsonLoc := false
	if decodedJSON != "" {
		var ok bool
		if members, ok = topLevelMembers(decodedJSON); !ok {
			jsonLoc = true // unreadable: cannot tell, so hide
		}
		for _, m := range members {
			switch m.key {
			case "lat", "lon":
				jsonLoc = true
			case "flags":
				jsonLoc = jsonLoc || jsonFlagTrue(m.value, "hasLocation")
			}
		}
	}
	if !frameLoc && !jsonLoc {
		return rawHex, decodedJSON, nil
	}

	hidden = &hiddenFrame{head: head, bytes: len(rawHex) / 2}
	if members == nil {
		return "", "", hidden
	}
	var b strings.Builder
	b.Grow(len(decodedJSON))
	b.WriteByte('{')
	n := 0
	for _, m := range members {
		if isHiddenKey(m.key) {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(m.text)
		n++
	}
	b.WriteByte('}')
	return "", b.String(), hidden
}

func isHiddenKey(k string) bool {
	for _, h := range locatedAdvertHiddenKeys {
		if k == h {
			return true
		}
	}
	return false
}

// jsonMember is one top-level member of a JSON object: its unescaped key,
// its text as stored ("key":value) and the value's text.
type jsonMember struct {
	key, text, value string
}

// topLevelMembers splits a JSON object into its top-level members without
// decoding the values: json.Valid checks the whole text first, so the walk
// below only has to find where each member ends. Keys are unescaped (a key
// spelled with escapes is still recognised); duplicate keys are all
// returned. ok is false when s is not valid JSON or not an object. This is
// the one parse of a stored advert's JSON: unmarshalling it into a map and
// marshalling it back costs about ten times as much.
func topLevelMembers(s string) (members []jsonMember, ok bool) {
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	i := skipJSONSpace(s, 0)
	if i >= len(s) || s[i] != '{' {
		return nil, false
	}
	i = skipJSONSpace(s, i+1)
	members = make([]jsonMember, 0, 16)
	if s[i] == '}' {
		return members, true
	}
	for {
		start := i
		keyEnd := endOfJSONString(s, i)
		key := s[i+1 : keyEnd-1]
		if strings.IndexByte(key, '\\') >= 0 {
			if json.Unmarshal([]byte(s[i:keyEnd]), &key) != nil {
				return nil, false
			}
		}
		i = skipJSONSpace(s, keyEnd)
		i = skipJSONSpace(s, i+1) // past ':'
		valStart := i
		i = endOfJSONValue(s, i)
		members = append(members, jsonMember{key: key, text: s[start:i], value: s[valStart:i]})
		i = skipJSONSpace(s, i)
		if s[i] == '}' {
			return members, true
		}
		i = skipJSONSpace(s, i+1) // past ','
	}
}

// endOfJSONString returns the index just past the closing quote of the
// (valid) string starting at s[i].
func endOfJSONString(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

// endOfJSONValue returns the index just past the (valid) value at s[i].
func endOfJSONValue(s string, i int) int {
	switch s[i] {
	case '"':
		return endOfJSONString(s, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				j = endOfJSONString(s, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(s)
	}
	j := i
	for j < len(s) && s[j] != ',' && s[j] != '}' && s[j] != ']' && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '\r' {
		j++
	}
	return j
}

// untypedIsAdvert decides the type of a row whose payload_type is NULL
// (legacy rows): the frame header when there is one, else the decoded type.
func untypedIsAdvert(rawHex, decodedJSON string) bool {
	if h, ok := hexByte(rawHex, 0); ok {
		return int(h>>2&0x0F) == PayloadADVERT
	}
	return strings.Contains(decodedJSON, `"ADVERT"`)
}

// advertFrameLocation walks an ADVERT frame to its appdata flags byte and
// reports whether the frame must be hidden, plus the frame head kept for the
// analytics and served as frame_head. It fails closed:
//
//   - a frame that cannot be walked (bad hex, odd length, reserved path
//     size) is hidden;
//   - a frame that holds at least one signature byte (payload longer than
//     pubkey + timestamp, 36 bytes) but no readable flags byte is hidden: a
//     truncated signature still narrows a brute force of the position, and
//     a frame cut short cannot say it has none;
//   - otherwise the location bit of the flags byte decides.
//
// A frame that ends before the signature has no location and nothing to
// verify one with, so it keeps its bytes. An empty frame reports false.
//
// head is a copy (strings.Clone), never a substring of rawHex: a substring
// would keep the whole exact frame, position and signature included,
// reachable from the store.
func advertFrameLocation(rawHex string) (loc bool, head string) {
	if rawHex == "" {
		return false, ""
	}
	header, ok := hexByte(rawHex, 0)
	if !ok {
		return true, ""
	}
	off := 1
	if isTransportRoute(int(header & 0x03)) {
		off += 4
	}
	pathByte, ok := hexByte(rawHex, off)
	if !ok {
		// Shorter than the path byte: header and transport codes only, no
		// payload byte to hide.
		if len(rawHex)%2 != 0 {
			return true, ""
		}
		return false, strings.Clone(rawHex)
	}
	if len(rawHex)%2 != 0 || !isValidPathLen(pathByte) {
		return true, strings.Clone(rawHex[:2*(off+1)])
	}
	n := len(rawHex) / 2
	payloadAt := off + 1 + int(pathByte>>6+1)*int(pathByte&0x3F)
	head = strings.Clone(rawHex[:2*min(payloadAt, n)])
	flagsAt := payloadAt + advertAppdataOffset
	flags, ok := hexByte(rawHex, flagsAt)
	switch {
	case ok:
		return flags&advertLocationFlag != 0, head
	case flagsAt < n:
		return true, head // the flags byte is there but is not hex
	default:
		// Truncated before the flags byte: hidden once any signature byte
		// (payload offset 36 on) is in the frame.
		return n > payloadAt+advertSignatureOffset, head
	}
}

// jsonFlagTrue reports whether the JSON object text s has key set to true.
// s is normally the compact output of json.Marshal (the ingestor's), but
// whitespace is tolerated; a value other than false reports true when s
// names the key at all (fail closed).
func jsonFlagTrue(s, key string) bool {
	i := strings.Index(s, `"`+key+`"`)
	if i < 0 {
		return false
	}
	rest := strings.TrimLeft(s[i+len(key)+2:], " \t\r\n")
	if !strings.HasPrefix(rest, ":") {
		return true
	}
	rest = strings.TrimLeft(rest[1:], " \t\r\n")
	return !strings.HasPrefix(rest, "false")
}

// hexByte decodes byte i of a hex string without allocating.
func hexByte(s string, i int) (byte, bool) {
	if i < 0 || 2*i+1 >= len(s) {
		return 0, false
	}
	hi, ok1 := hexNib(s[2*i])
	lo, ok2 := hexNib(s[2*i+1])
	return hi<<4 | lo, ok1 && ok2
}

func hexNib(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// redactStoreTx applies redactLocatedAdvert to a transmission the store is
// about to hold (it is not yet shared, or the caller holds s.mu for writing).
func redactStoreTx(f *locationFuzzer, tx *StoreTx) {
	if f == nil {
		return
	}
	tx.RawHex, tx.DecodedJSON, tx.hidden = redactLocatedAdvert(f, tx.PayloadType, tx.RawHex, tx.DecodedJSON)
}

// markLocationHidden sets "location_hidden": true on a served packet or
// observation map built from tx. Nothing changes when tx is not hidden, so
// the feature off leaves every response as it was.
func markLocationHidden(m map[string]interface{}, tx *StoreTx) {
	if tx != nil && tx.hidden != nil {
		tx.hidden.mark(m)
	}
}

// mark sets the hidden-packet fields of the contract on a served map:
// location_hidden, and the frame's size and header (frame_bytes,
// frame_head) so size filters and hash-size badges keep working.
func (h *hiddenFrame) mark(m map[string]interface{}) {
	m["location_hidden"] = true
	m["frame_bytes"] = h.bytes
	m["frame_head"] = strOrNil(h.head)
}

// redactPacketMap redacts a packet or observation map read straight from SQL
// (raw_hex, decoded_json and payload_type as the DB scanners put them) in
// place, and marks it location_hidden. It reports whether it hid it.
func redactPacketMap(f *locationFuzzer, m map[string]interface{}) bool {
	if f == nil || m == nil {
		return false
	}
	var pt *int
	if v, ok := m["payload_type"].(int); ok {
		if v != PayloadADVERT {
			return false
		}
		pt = &v
	}
	raw, _ := m["raw_hex"].(string)
	dec, _ := m["decoded_json"].(string)
	raw, dec, hidden := redactLocatedAdvert(f, pt, raw, dec)
	if hidden == nil {
		return false
	}
	m["raw_hex"] = nil
	if _, ok := m["decoded_json"]; ok {
		m["decoded_json"] = strOrNil(dec)
	}
	hidden.mark(m)
	return true
}

// redactObservationFrame is the served form of one observation's own wire
// frame (observations.raw_hex): "" for a located advert, else the frame.
func redactObservationFrame(f *locationFuzzer, rawHex string) string {
	if f == nil {
		return rawHex
	}
	raw, _, hidden := redactLocatedAdvert(f, nil, rawHex, "")
	if hidden != nil {
		return ""
	}
	return raw
}
