package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/packetpath"
)

// MediaMesh: radio2, a second LoRa profile that Keymind Cascade repeaters,
// Wadamesh TAK standalones and MeshCore Open TAK use for photos and voice
// (schema and the observer contract: internal/dbschema/mediamesh.go). This
// file reads what observers say about it and records it:
//
//   - packets with `mediamesh: true` -> observation_mediamesh (sparse);
//   - the node that sent a MediaMesh packet an observer heard, a status
//     `radios[]` entry with `mediamesh: true`, and a neighbors report's
//     `radio2` -> node_mediamesh, newest evidence first.
//
// Every key is optional. A message without them (every observer before this
// change) is ingested exactly as before: nothing here writes for it.

const (
	mediaMeshModeOff  = "off"
	mediaMeshModeRx   = "rx"
	mediaMeshModeRxTx = "rxtx"

	mediaMeshSourceStatus   = "status"   // the node's own status message
	mediaMeshSourceNeighbor = "neighbor" // a zero-hop neighbour's report of it
	mediaMeshSourceHeard    = "heard"    // heard sending a MediaMesh packet

	// A node heard sending on radio2 is re-stamped at most this often: a
	// photo is dozens of packets, and each would otherwise be an UPSERT.
	mediaMeshHeardDebounce = time.Minute
	// Bounds the debounce map (one entry per transmitting node); it is reset
	// when full, which costs at most one extra UPSERT per node.
	mediaMeshHeardMapMax = 8192
	// node_mediamesh rows not refreshed for this long are pruned.
	mediaMeshNodeRetention = 30 * 24 * time.Hour
)

// mediaMeshFlag reads the packet's `mediamesh` key: a JSON boolean, with the
// strings "true" and "1" tolerated (firmware formats other numbers as strings).
// Missing or anything else is false.
func mediaMeshFlag(msg map[string]interface{}) bool {
	switch v := msg["mediamesh"].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1"
	case float64:
		return v == 1
	}
	return false
}

// normalizeMediaMeshMode keeps the three modes the contract defines.
func normalizeMediaMeshMode(v interface{}) string {
	s, _ := v.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case mediaMeshModeRx:
		return mediaMeshModeRx
	case mediaMeshModeRxTx, "rx&tx":
		return mediaMeshModeRxTx
	case mediaMeshModeOff:
		return mediaMeshModeOff
	}
	return ""
}

// MediaMeshReport is one piece of evidence about a node's radio2.
type MediaMeshReport struct {
	PubKey string // lower-case 64-hex node key
	Mode   string // rx | rxtx | off
	Radio  string // canonical "MHz,kHz,SF,CR"; "" when off or unknown
	Source string
}

// mediaMeshFromRadio2 reads a neighbors-message `radio2` object.
func mediaMeshFromRadio2(pubkey string, v interface{}, source string) (MediaMeshReport, bool) {
	obj, ok := v.(map[string]interface{})
	if !ok {
		return MediaMeshReport{}, false
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if !targetPubkeyRe.MatchString(pubkey) {
		return MediaMeshReport{}, false
	}
	mode := normalizeMediaMeshMode(obj["mode"])
	if mode == "" {
		return MediaMeshReport{}, false
	}
	r := MediaMeshReport{PubKey: pubkey, Mode: mode, Source: source}
	if mode != mediaMeshModeOff {
		if radio, ok := obj["radio"].(string); ok {
			r.Radio = clampObserverField(dbschema.CanonicalRadio(radio), maxObserverTextLen)
		}
	}
	return r, true
}

// mediaMeshFromStatus reads a status message's `radios` list: the entry with
// `mediamesh: true`, for the observer's own node.
func mediaMeshFromStatus(observerID string, msg map[string]interface{}) (MediaMeshReport, bool) {
	list, _ := msg["radios"].([]interface{})
	for _, item := range list {
		e, ok := item.(map[string]interface{})
		if !ok || !mediaMeshFlag(e) {
			continue
		}
		mode := normalizeMediaMeshMode(e["mode"])
		if mode == "" || mode == mediaMeshModeOff {
			mode = mediaMeshModeRx // listed as on, mode not given: at least listening
		}
		return mediaMeshFromRadio2(observerID, map[string]interface{}{
			"mode": mode, "radio": e["radio"],
		}, mediaMeshSourceStatus)
	}
	return MediaMeshReport{}, false
}

// mediaMeshFromNeighbors reads a neighbors report: `self.radio2` for the
// observer's own node and each neighbour's `radio2`. A missing key is unknown
// and yields nothing.
func mediaMeshFromNeighbors(originID string, msg map[string]interface{}) []MediaMeshReport {
	var out []MediaMeshReport
	if self, ok := msg["self"].(map[string]interface{}); ok {
		if r, ok := mediaMeshFromRadio2(originID, self["radio2"], mediaMeshSourceStatus); ok {
			out = append(out, r)
		}
	}
	neighbors, _ := msg["neighbors"].([]interface{})
	for _, raw := range neighbors {
		n, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		pubkey, _ := n["pubkey"].(string)
		if r, ok := mediaMeshFromRadio2(pubkey, n["radio2"], mediaMeshSourceNeighbor); ok {
			out = append(out, r)
		}
	}
	return out
}

// mediaMeshSender is the node that transmitted a MediaMesh packet the
// observer received:
//   - a flood packet with an empty path: its originator, heard straight;
//   - a flood packet with a path: the last hop, the forwarder the observer
//     heard. Only an unambiguously resolved hop counts (a 1-byte prefix
//     shared by several repeaters is not evidence, as for last_heard, #2260);
//   - a direct-route ADVERT with an empty path: a zero-hop advert, from its
//     originator.
//
// Any other direct-route packet gives nothing: its path is the route still
// to travel, and an empty one can mean the last repeater on it sent it.
func mediaMeshSender(data *PacketData, resolved []*string) string {
	if data.Direction != nil && strings.EqualFold(*data.Direction, "tx") {
		return ""
	}
	if !packetpath.PathBytesAreHops(byte(data.PayloadType)) {
		return ""
	}
	hops := parsePathArray(data.PathJSON)
	flood := pathHopsTransmitted(data)
	if len(hops) == 0 {
		if flood || data.PayloadType == PayloadADVERT {
			return strings.ToLower(data.FromPubkey)
		}
		return ""
	}
	if !flood {
		return ""
	}
	if len(resolved) != len(hops) {
		return ""
	}
	last := resolved[len(resolved)-1]
	if last == nil {
		return ""
	}
	return strings.ToLower(*last)
}

// mediaMeshHeard debounces "heard sending on radio2" writes per node.
type mediaMeshHeard struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (h *mediaMeshHeard) due(pubkey string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil || len(h.last) >= mediaMeshHeardMapMax {
		h.last = make(map[string]time.Time)
	}
	if t, ok := h.last[pubkey]; ok && now.Sub(t) < mediaMeshHeardDebounce {
		return false
	}
	h.last[pubkey] = now
	return true
}

// recordObservationMediaMesh marks one observation as MediaMesh.
func (s *Store) recordObservationMediaMesh(observationID int64) {
	if observationID == 0 {
		return
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO observation_mediamesh (observation_id) VALUES (?)`,
		observationID); err != nil {
		s.Stats.WriteErrors.Add(1)
		log.Printf("[db] observation mediamesh (non-fatal): %v", err)
	}
}

// RecordMediaMesh stores evidence about a node's radio2, newest first. A
// "heard" report (mode rxtx, no radio) keeps a radio the node reported itself.
func (s *Store) RecordMediaMesh(r MediaMeshReport, now time.Time) error {
	if r.PubKey == "" || r.Mode == "" || r.Source == "" {
		return nil
	}
	ts := now.UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO node_mediamesh (public_key, mode, radio, source, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(public_key) DO UPDATE SET
			mode      = excluded.mode,
			radio     = CASE WHEN excluded.mode = 'off' THEN NULL
			                 ELSE COALESCE(excluded.radio, radio) END,
			source    = excluded.source,
			last_seen = excluded.last_seen
		WHERE excluded.last_seen >= node_mediamesh.last_seen`,
		r.PubKey, r.Mode, nilIfEmpty(r.Radio), r.Source, ts)
	if err != nil {
		s.Stats.WriteErrors.Add(1)
	}
	return err
}

// noteMediaMeshObservation runs for every stored observation (the caller
// holds writerMu): nothing for a non-MediaMesh one, else the sparse row and,
// when the sender resolves, a debounced "heard" mark for it.
func (s *Store) noteMediaMeshObservation(obsID int64, data *PacketData, resolved []*string) {
	if !data.MediaMesh {
		return
	}
	s.recordObservationMediaMesh(obsID)
	sender := mediaMeshSender(data, resolved)
	if !targetPubkeyRe.MatchString(sender) {
		return
	}
	now := time.Now()
	if !s.mediaMeshHeard.due(sender, now) {
		return
	}
	if err := s.RecordMediaMesh(MediaMeshReport{
		PubKey: sender, Mode: mediaMeshModeRxTx, Source: mediaMeshSourceHeard,
	}, now); err != nil {
		log.Printf("[db] node mediamesh heard (non-fatal): %v", err)
	}
}

// PruneMediaMeshNodes drops node_mediamesh rows not refreshed for 30 days.
func (s *Store) PruneMediaMeshNodes(now time.Time) (int64, error) {
	cutoff := now.Add(-mediaMeshNodeRetention).UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM node_mediamesh WHERE last_seen < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
