package main

import (
	"database/sql"
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxNodeNameRunes bounds a reported node name. The firmware keeps the name in
// NodePrefs::node_name[32] (src/helpers/CommonCLI.h), so a real name is at most
// 31 bytes. The bound is in runes rather than bytes because the client decodes
// the reply as UTF-8: a name the firmware cut mid-character decodes to a
// U+FFFD, which is 3 bytes for what was 1-2 on air.
const maxNodeNameRunes = 31

// cleanNodeName validates a node name reported over /names. Returns the trimmed
// name and true, or "" and false when it is empty, too long, not valid UTF-8,
// or contains a control character. Trailing NULs are trimmed first: the reply
// is NUL-padded to the AES block size (see handleClientRegions), and a client
// build that did not trim it must not store the padding as part of the name.
func cleanNodeName(s string) (string, bool) {
	s = strings.TrimSpace(strings.TrimRight(s, "\x00"))
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxNodeNameRunes {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

// handleClientNodeNames processes one name answer from
// meshcore/client/{PUBLIC_KEY}/names: a mobile companion asked a repeater
// (target) for its own name (ANON_REQ_TYPE_OWNER) because this instance had
// none for it, and relays the answer. The topic pubkey (rxPubkey) is the
// reporting companion's identity (ACL-bound by the broker); `target` is
// payload data and must be a full 64-hex pubkey, for the same reason as on
// /regions (targetPubkeyRe): the client can only ask a full pubkey. GPS is
// optional; the name is meaningful without a position.
func handleClientNodeNames(store *Store, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] names: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	target, _ := msg["target"].(string)
	target = strings.ToLower(strings.TrimSpace(target))
	if !targetPubkeyRe.MatchString(target) {
		log.Printf("MQTT [%s] names: invalid target %.8q, dropping", tag, target)
		return
	}
	raw, _ := msg["name"].(string)
	name, ok := cleanNodeName(raw)
	if !ok {
		log.Printf("MQTT [%s] names: missing/invalid name for %.8s, dropping", tag, target)
		return
	}

	rxTime, _ := resolveRxTimeCore(msg, tag)

	var latPtr, lonPtr, posAccPtr *float64
	if gps, ok := msg["gps"].(map[string]interface{}); ok {
		lat, latOK := toFloat64(gps["lat"])
		lon, lonOK := toFloat64(gps["lon"])
		if latOK && lonOK && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			latPtr, lonPtr = &lat, &lon
			if acc, ok := toFloat64(gps["acc_m"]); ok {
				posAccPtr = &acc
			}
		}
	}

	o := &ClientNodeName{
		Target:        target,
		RxPubkey:      rxPubkey,
		ObservedAt:    rxTime.Format(rxTimeMillisLayout),
		IngestedAt:    time.Now().UTC().Format(time.RFC3339),
		Name:          name,
		Lat:           latPtr,
		Lon:           lonPtr,
		PosAccM:       posAccPtr,
		RepeaterClock: optInt64(msg, "repeater_clock"),
	}
	if _, err := store.InsertClientNodeName(o); err != nil {
		log.Printf("MQTT [%s] client_node_names insert: %v", tag, err)
	}
}

// ClientNodeName is one repeater's own name, as it answered an
// ANON_REQ_TYPE_OWNER request relayed by a mobile companion. Observations,
// never state, like node_declared_regions: the current name for a target is
// the one with the greatest observed_at.
type ClientNodeName struct {
	Target        string
	RxPubkey      string
	ObservedAt    string
	IngestedAt    string
	Name          string
	Lat, Lon      *float64
	PosAccM       *float64
	RepeaterClock *int64
}

// InsertClientNodeName writes one name observation. Idempotent via
// UNIQUE(target, rx_pubkey, observed_at); returns ins=false when the row
// already existed.
func (s *Store) InsertClientNodeName(o *ClientNodeName) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_node_names
			(target, rx_pubkey, observed_at, ingested_at, name, lat, lon, pos_acc_m, repeater_clock)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(target, rx_pubkey, observed_at) DO NOTHING`,
		o.Target, o.RxPubkey, o.ObservedAt, o.IngestedAt, o.Name, o.Lat, o.Lon, o.PosAccM, o.RepeaterClock)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CurrentClientNodeName returns the most recent name observation for a target,
// or nil if none exist. Ordered by observed_at, not ingested_at: a drive
// buffered offline must not overwrite a fresher reading.
func (s *Store) CurrentClientNodeName(target string) (*ClientNodeName, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	row := s.db.QueryRow(`
		SELECT target, rx_pubkey, observed_at, ingested_at, name, lat, lon, pos_acc_m, repeater_clock
		FROM client_node_names
		WHERE target = ?
		ORDER BY observed_at DESC LIMIT 1`, target)
	var o ClientNodeName
	if err := row.Scan(&o.Target, &o.RxPubkey, &o.ObservedAt, &o.IngestedAt, &o.Name, &o.Lat, &o.Lon, &o.PosAccM, &o.RepeaterClock); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}
