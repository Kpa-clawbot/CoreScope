package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Route type constants (header bits 1-0)
const (
	RouteTransportFlood  = 0
	RouteFlood           = 1
	RouteDirect          = 2
	RouteTransportDirect = 3
)

// Payload type constants (header bits 5-2)
const (
	PayloadREQ        = 0x00
	PayloadRESPONSE   = 0x01
	PayloadTXT_MSG    = 0x02
	PayloadACK        = 0x03
	PayloadADVERT     = 0x04
	PayloadGRP_TXT    = 0x05
	PayloadGRP_DATA   = 0x06
	PayloadANON_REQ   = 0x07
	PayloadPATH       = 0x08
	PayloadTRACE      = 0x09
	PayloadMULTIPART  = 0x0A
	PayloadCONTROL    = 0x0B
	PayloadRAW_CUSTOM = 0x0F
)

var routeTypeNames = map[int]string{
	0: "TRANSPORT_FLOOD",
	1: "FLOOD",
	2: "DIRECT",
	3: "TRANSPORT_DIRECT",
}

// Header is the decoded packet header.
type Header struct {
	RouteType       int    `json:"routeType"`
	RouteTypeName   string `json:"routeTypeName"`
	PayloadType     int    `json:"payloadType"`
	PayloadTypeName string `json:"payloadTypeName"`
	PayloadVersion  int    `json:"payloadVersion"`
}

// TransportCodes are present on TRANSPORT_FLOOD and TRANSPORT_DIRECT routes.
type TransportCodes struct {
	Code1 string `json:"code1"`
	Code2 string `json:"code2"`
}

// Path holds decoded path/hop information.
type Path struct {
	HashSize  int      `json:"hashSize"`
	HashCount int      `json:"hashCount"`
	Hops      []string `json:"hops"`
}

// AdvertFlags holds decoded advert flag bits.
type AdvertFlags struct {
	Raw         int  `json:"raw"`
	Type        int  `json:"type"`
	Chat        bool `json:"chat"`
	Repeater    bool `json:"repeater"`
	Room        bool `json:"room"`
	Sensor      bool `json:"sensor"`
	HasLocation bool `json:"hasLocation"`
	HasFeat1    bool `json:"hasFeat1"`
	HasFeat2    bool `json:"hasFeat2"`
	HasName     bool `json:"hasName"`
}

// Payload is a generic decoded payload. Fields are populated depending on type.
type Payload struct {
	Type            string       `json:"type"`
	DestHash        string       `json:"destHash,omitempty"`
	SrcHash         string       `json:"srcHash,omitempty"`
	MAC             string       `json:"mac,omitempty"`
	EncryptedData   string       `json:"encryptedData,omitempty"`
	ExtraHash       string       `json:"extraHash,omitempty"`
	PubKey          string       `json:"pubKey,omitempty"`
	Timestamp       uint32       `json:"timestamp,omitempty"`
	TimestampISO    string       `json:"timestampISO,omitempty"`
	Signature       string       `json:"signature,omitempty"`
	Flags           *AdvertFlags `json:"flags,omitempty"`
	Lat             *float64     `json:"lat,omitempty"`
	Lon             *float64     `json:"lon,omitempty"`
	Name            string       `json:"name,omitempty"`
	ChannelHash     int          `json:"channelHash,omitempty"`
	EphemeralPubKey string       `json:"ephemeralPubKey,omitempty"`
	PathData        string       `json:"pathData,omitempty"`
	Tag             uint32       `json:"tag,omitempty"`
	AuthCode        uint32       `json:"authCode,omitempty"`
	TraceFlags      *int         `json:"traceFlags,omitempty"`
	RawHex          string       `json:"raw,omitempty"`
	Error           string       `json:"error,omitempty"`
}

// DecodedPacket is the full decoded result.
type DecodedPacket struct {
	Header         Header          `json:"header"`
	TransportCodes *TransportCodes `json:"transportCodes"`
	Path           Path            `json:"path"`
	Payload        Payload         `json:"payload"`
	Raw            string          `json:"raw"`
}

func decodeHeader(b byte) Header {
	rt := int(b & 0x03)
	pt := int((b >> 2) & 0x0F)
	pv := int((b >> 6) & 0x03)

	rtName := routeTypeNames[rt]
	if rtName == "" {
		rtName = "UNKNOWN"
	}
	ptName := payloadTypeNames[pt]
	if ptName == "" {
		ptName = "UNKNOWN"
	}

	return Header{
		RouteType:       rt,
		RouteTypeName:   rtName,
		PayloadType:     pt,
		PayloadTypeName: ptName,
		PayloadVersion:  pv,
	}
}

func decodePath(pathByte byte, buf []byte, offset int) (Path, int) {
	hashSize := int(pathByte>>6) + 1
	hashCount := int(pathByte & 0x3F)
	totalBytes := hashSize * hashCount
	hops := make([]string, 0, hashCount)

	for i := 0; i < hashCount; i++ {
		start := offset + i*hashSize
		end := start + hashSize
		if end > len(buf) {
			break
		}
		hops = append(hops, strings.ToUpper(hex.EncodeToString(buf[start:end])))
	}

	return Path{
		HashSize:  hashSize,
		HashCount: hashCount,
		Hops:      hops,
	}, totalBytes
}

func isTransportRoute(routeType int) bool {
	return routeType == RouteTransportFlood || routeType == RouteTransportDirect
}

// cleanHex removes whitespace from a hex string.
func cleanHex(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// packetFrame holds parsed packet frame offsets used by both DecodePacket and BuildBreakdown.
type packetFrame struct {
	buf             []byte
	header          Header
	hasTransport    bool
	transportOffset int // start of transport codes (if present)
	pathOffset      int // offset of path length byte
	pathByte        byte
	hashSize        int
	hashCount       int
	pathDataOffset  int // start of path hop data
	payloadOffset   int // start of payload
}

// parsePacketFrame parses the common packet frame structure (header, transport codes, path).
// Returns nil if the packet is too short.
func parsePacketFrame(buf []byte) *packetFrame {
	if len(buf) < 2 {
		return nil
	}
	f := &packetFrame{buf: buf}
	f.header = decodeHeader(buf[0])
	offset := 1

	f.hasTransport = isTransportRoute(f.header.RouteType)
	if f.hasTransport {
		if len(buf) < offset+4 {
			return nil
		}
		f.transportOffset = offset
		offset += 4
	}

	if offset >= len(buf) {
		return nil
	}
	f.pathOffset = offset
	f.pathByte = buf[offset]
	offset++

	f.hashSize = int(f.pathByte>>6) + 1
	f.hashCount = int(f.pathByte & 0x3F)
	f.pathDataOffset = offset
	offset += f.hashSize * f.hashCount
	f.payloadOffset = offset
	return f
}

func decodeEncryptedPayload(typeName string, buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: typeName, Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:          typeName,
		DestHash:      hex.EncodeToString(buf[0:1]),
		SrcHash:       hex.EncodeToString(buf[1:2]),
		MAC:           hex.EncodeToString(buf[2:4]),
		EncryptedData: hex.EncodeToString(buf[4:]),
	}
}

func decodeAck(buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: "ACK", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	checksum := binary.LittleEndian.Uint32(buf[0:4])
	return Payload{
		Type:      "ACK",
		ExtraHash: fmt.Sprintf("%08x", checksum),
	}
}

func decodeAdvert(buf []byte) Payload {
	if len(buf) < 100 {
		return Payload{Type: "ADVERT", Error: "too short for advert", RawHex: hex.EncodeToString(buf)}
	}

	pubKey := hex.EncodeToString(buf[0:32])
	timestamp := binary.LittleEndian.Uint32(buf[32:36])
	signature := hex.EncodeToString(buf[36:100])
	appdata := buf[100:]

	p := Payload{
		Type:         "ADVERT",
		PubKey:       pubKey,
		Timestamp:    timestamp,
		TimestampISO: fmt.Sprintf("%s", epochToISO(timestamp)),
		Signature:    signature,
	}

	if len(appdata) > 0 {
		flags := appdata[0]
		advType := int(flags & 0x0F)
		hasFeat1 := flags&0x20 != 0
		hasFeat2 := flags&0x40 != 0
		p.Flags = &AdvertFlags{
			Raw:         int(flags),
			Type:        advType,
			Chat:        advType == 1,
			Repeater:    advType == 2,
			Room:        advType == 3,
			Sensor:      advType == 4,
			HasLocation: flags&0x10 != 0,
			HasFeat1:    hasFeat1,
			HasFeat2:    hasFeat2,
			HasName:     flags&0x80 != 0,
		}

		off := 1
		if p.Flags.HasLocation && len(appdata) >= off+8 {
			latRaw := int32(binary.LittleEndian.Uint32(appdata[off : off+4]))
			lonRaw := int32(binary.LittleEndian.Uint32(appdata[off+4 : off+8]))
			lat := float64(latRaw) / 1e6
			lon := float64(lonRaw) / 1e6
			p.Lat = &lat
			p.Lon = &lon
			off += 8
		}
		if hasFeat1 && len(appdata) >= off+2 {
			off += 2  // skip feat1 bytes (reserved for future use)
		}
		if hasFeat2 && len(appdata) >= off+2 {
			off += 2  // skip feat2 bytes (reserved for future use)
		}
		if p.Flags.HasName {
			name := string(appdata[off:])
			name = strings.TrimRight(name, "\x00")
			name = sanitizeName(name)
			p.Name = name
		}
	}

	return p
}

func decodeGrpTxt(buf []byte) Payload {
	if len(buf) < 3 {
		return Payload{Type: "GRP_TXT", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:          "GRP_TXT",
		ChannelHash:   int(buf[0]),
		MAC:           hex.EncodeToString(buf[1:3]),
		EncryptedData: hex.EncodeToString(buf[3:]),
	}
}

func decodeAnonReq(buf []byte) Payload {
	if len(buf) < 35 {
		return Payload{Type: "ANON_REQ", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:            "ANON_REQ",
		DestHash:        hex.EncodeToString(buf[0:1]),
		EphemeralPubKey: hex.EncodeToString(buf[1:33]),
		MAC:             hex.EncodeToString(buf[33:35]),
		EncryptedData:   hex.EncodeToString(buf[35:]),
	}
}

func decodePathPayload(buf []byte) Payload {
	if len(buf) < 4 {
		return Payload{Type: "PATH", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	return Payload{
		Type:     "PATH",
		DestHash: hex.EncodeToString(buf[0:1]),
		SrcHash:  hex.EncodeToString(buf[1:2]),
		MAC:      hex.EncodeToString(buf[2:4]),
		PathData: hex.EncodeToString(buf[4:]),
	}
}

func decodeTrace(buf []byte) Payload {
	if len(buf) < 9 {
		return Payload{Type: "TRACE", Error: "too short", RawHex: hex.EncodeToString(buf)}
	}
	tag := binary.LittleEndian.Uint32(buf[0:4])
	authCode := binary.LittleEndian.Uint32(buf[4:8])
	flags := int(buf[8])
	p := Payload{
		Type:       "TRACE",
		Tag:        tag,
		AuthCode:   authCode,
		TraceFlags: &flags,
	}
	if len(buf) > 9 {
		p.PathData = hex.EncodeToString(buf[9:])
	}
	return p
}

func decodePayload(payloadType int, buf []byte) Payload {
	switch payloadType {
	case PayloadREQ:
		return decodeEncryptedPayload("REQ", buf)
	case PayloadRESPONSE:
		return decodeEncryptedPayload("RESPONSE", buf)
	case PayloadTXT_MSG:
		return decodeEncryptedPayload("TXT_MSG", buf)
	case PayloadACK:
		return decodeAck(buf)
	case PayloadADVERT:
		return decodeAdvert(buf)
	case PayloadGRP_TXT:
		return decodeGrpTxt(buf)
	case PayloadANON_REQ:
		return decodeAnonReq(buf)
	case PayloadPATH:
		return decodePathPayload(buf)
	case PayloadTRACE:
		return decodeTrace(buf)
	default:
		return Payload{Type: "UNKNOWN", RawHex: hex.EncodeToString(buf)}
	}
}

// DecodePacket decodes a hex-encoded MeshCore packet.
func DecodePacket(hexString string) (*DecodedPacket, error) {
	hexString = cleanHex(hexString)

	buf, err := hex.DecodeString(hexString)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}

	f := parsePacketFrame(buf)
	if f == nil {
		return nil, fmt.Errorf("packet too short")
	}

	var tc *TransportCodes
	if f.hasTransport {
		tc = &TransportCodes{
			Code1: strings.ToUpper(hex.EncodeToString(buf[f.transportOffset : f.transportOffset+2])),
			Code2: strings.ToUpper(hex.EncodeToString(buf[f.transportOffset+2 : f.transportOffset+4])),
		}
	}

	path, _ := decodePath(f.pathByte, buf, f.pathDataOffset)
	payloadBuf := buf[f.payloadOffset:]
	payload := decodePayload(f.header.PayloadType, payloadBuf)

	// TRACE packets store hop IDs in the payload (buf[9:]) rather than the header
	// path field. The header path byte still encodes hashSize in bits 6-7, which
	// we use to split the payload path data into individual hop prefixes.
	if f.header.PayloadType == PayloadTRACE && payload.PathData != "" {
		pathBytes, err := hex.DecodeString(payload.PathData)
		if err == nil && path.HashSize > 0 {
			hops := make([]string, 0, len(pathBytes)/path.HashSize)
			for i := 0; i+path.HashSize <= len(pathBytes); i += path.HashSize {
				hops = append(hops, strings.ToUpper(hex.EncodeToString(pathBytes[i:i+path.HashSize])))
			}
			path.Hops = hops
			path.HashCount = len(hops)
		}
	}

	return &DecodedPacket{
		Header:         f.header,
		TransportCodes: tc,
		Path:           path,
		Payload:        payload,
		Raw:            strings.ToUpper(hexString),
	}, nil
}

// HexRange represents a labeled byte range for the hex breakdown visualization.
type HexRange struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Label string `json:"label"`
}

// Breakdown holds colored byte ranges returned by the packet detail endpoint.
type Breakdown struct {
	Ranges []HexRange `json:"ranges"`
}

// BuildBreakdown computes labeled byte ranges for each section of a MeshCore packet.
// The returned ranges are consumed by createColoredHexDump() and buildHexLegend()
// in the frontend (public/packets.js).
func BuildBreakdown(hexString string) *Breakdown {
	hexString = cleanHex(hexString)
	buf, err := hex.DecodeString(hexString)
	if err != nil || len(buf) < 2 {
		return &Breakdown{Ranges: []HexRange{}}
	}

	f := parsePacketFrame(buf)
	if f == nil {
		return &Breakdown{Ranges: []HexRange{{Start: 0, End: 0, Label: "Header"}}}
	}

	var ranges []HexRange

	// Header byte
	ranges = append(ranges, HexRange{Start: 0, End: 0, Label: "Header"})

	// Transport codes
	if f.hasTransport {
		ranges = append(ranges, HexRange{Start: f.transportOffset, End: f.transportOffset + 3, Label: "Transport Codes"})
	}

	// Path length byte
	ranges = append(ranges, HexRange{Start: f.pathOffset, End: f.pathOffset, Label: "Path Length"})

	// Path hops
	pathBytes := f.hashSize * f.hashCount
	if f.hashCount > 0 && f.pathDataOffset+pathBytes <= len(buf) {
		ranges = append(ranges, HexRange{Start: f.pathDataOffset, End: f.pathDataOffset + pathBytes - 1, Label: "Path"})
	}

	if f.payloadOffset >= len(buf) {
		return &Breakdown{Ranges: ranges}
	}

	// Payload — break ADVERT into named sub-fields; everything else is one Payload range
	if f.header.PayloadType == PayloadADVERT && len(buf)-f.payloadOffset >= 100 {
		ps := f.payloadOffset
		ranges = append(ranges, HexRange{Start: ps, End: ps + 31, Label: "PubKey"})
		ranges = append(ranges, HexRange{Start: ps + 32, End: ps + 35, Label: "Timestamp"})
		ranges = append(ranges, HexRange{Start: ps + 36, End: ps + 99, Label: "Signature"})

		appStart := ps + 100
		if appStart < len(buf) {
			ranges = append(ranges, HexRange{Start: appStart, End: appStart, Label: "Flags"})
			appFlags := buf[appStart]
			fOff := appStart + 1
			if appFlags&0x10 != 0 && fOff+8 <= len(buf) {
				ranges = append(ranges, HexRange{Start: fOff, End: fOff + 3, Label: "Latitude"})
				ranges = append(ranges, HexRange{Start: fOff + 4, End: fOff + 7, Label: "Longitude"})
				fOff += 8
			}
			if appFlags&0x20 != 0 && fOff+2 <= len(buf) {
				ranges = append(ranges, HexRange{Start: fOff, End: fOff + 1, Label: "Feature1"})
				fOff += 2
			}
			if appFlags&0x40 != 0 && fOff+2 <= len(buf) {
				ranges = append(ranges, HexRange{Start: fOff, End: fOff + 1, Label: "Feature2"})
				fOff += 2
			}
			if appFlags&0x80 != 0 && fOff < len(buf) {
				ranges = append(ranges, HexRange{Start: fOff, End: len(buf) - 1, Label: "Name"})
			} else if fOff < len(buf) {
				ranges = append(ranges, HexRange{Start: fOff, End: len(buf) - 1, Label: "AppData"})
			}
		}
	} else {
		ranges = append(ranges, HexRange{Start: f.payloadOffset, End: len(buf) - 1, Label: "Payload"})
	}

	return &Breakdown{Ranges: ranges}
}

// ComputeContentHash computes the SHA-256-based content hash (first 16 hex chars).
func ComputeContentHash(rawHex string) string {
	buf, err := hex.DecodeString(rawHex)
	if err != nil || len(buf) < 2 {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	headerByte := buf[0]
	offset := 1
	if isTransportRoute(int(headerByte & 0x03)) {
		offset += 4
	}
	if offset >= len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}
	pathByte := buf[offset]
	offset++
	hashSize := int((pathByte>>6)&0x3) + 1
	hashCount := int(pathByte & 0x3F)
	pathBytes := hashSize * hashCount

	payloadStart := offset + pathBytes
	if payloadStart > len(buf) {
		if len(rawHex) >= 16 {
			return rawHex[:16]
		}
		return rawHex
	}

	payload := buf[payloadStart:]
	toHash := append([]byte{headerByte}, payload...)

	h := sha256.Sum256(toHash)
	return hex.EncodeToString(h[:])[:16]
}

// PayloadJSON serializes the payload to JSON for DB storage.
func PayloadJSON(p *Payload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ValidateAdvert checks decoded advert data before DB insertion.
func ValidateAdvert(p *Payload) (bool, string) {
	if p == nil || p.Error != "" {
		reason := "null advert"
		if p != nil {
			reason = p.Error
		}
		return false, reason
	}

	pk := p.PubKey
	if len(pk) < 16 {
		return false, fmt.Sprintf("pubkey too short (%d hex chars)", len(pk))
	}
	allZero := true
	for _, c := range pk {
		if c != '0' {
			allZero = false
			break
		}
	}
	if allZero {
		return false, "pubkey is all zeros"
	}

	if p.Lat != nil {
		if math.IsInf(*p.Lat, 0) || math.IsNaN(*p.Lat) || *p.Lat < -90 || *p.Lat > 90 {
			return false, fmt.Sprintf("invalid lat: %f", *p.Lat)
		}
	}
	if p.Lon != nil {
		if math.IsInf(*p.Lon, 0) || math.IsNaN(*p.Lon) || *p.Lon < -180 || *p.Lon > 180 {
			return false, fmt.Sprintf("invalid lon: %f", *p.Lon)
		}
	}

	if p.Name != "" {
		for _, c := range p.Name {
			if (c >= 0x00 && c <= 0x08) || c == 0x0b || c == 0x0c || (c >= 0x0e && c <= 0x1f) || c == 0x7f {
				return false, "name contains control characters"
			}
		}
		if len(p.Name) > 64 {
			return false, fmt.Sprintf("name too long (%d chars)", len(p.Name))
		}
	}

	if p.Flags != nil {
		role := advertRole(p.Flags)
		validRoles := map[string]bool{"repeater": true, "companion": true, "room": true, "sensor": true}
		if !validRoles[role] {
			return false, fmt.Sprintf("unknown role: %s", role)
		}
	}

	return true, ""
}

// sanitizeName strips non-printable characters (< 0x20 except tab/newline) and DEL.
func sanitizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if c == '\t' || c == '\n' || (c >= 0x20 && c != 0x7f) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func advertRole(f *AdvertFlags) string {
	if f.Repeater {
		return "repeater"
	}
	if f.Room {
		return "room"
	}
	if f.Sensor {
		return "sensor"
	}
	return "companion"
}

func epochToISO(epoch uint32) string {
	t := time.Unix(int64(epoch), 0)
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
