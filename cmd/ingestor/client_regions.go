package main

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// A mobile client (CoreDrive RX) asks a repeater it can hear directly which
// regions it is configured to flood, and publishes the answer on
// meshcore/client/{PUBLIC_KEY}/regions:
//
//	{"origin_id":"<rx pubkey>","origin":"<name>","timestamp":"<ISO-8601>",
//	 "type":"REGIONS","target":"<repeater pubkey>","regions":["*","hu"],
//	 "truncated":false,"repeater_clock":1789722571,"gps":{...}}
//
// The answer is the repeater's own configuration read back off the node, the
// same fact the observer /neighbors report writes to nodes.configured_scope,
// so it lands in node_declared_regions, the second source the Scope Audit and
// the auto region-key tier already merge newest-answer-wins.

// targetPubkeyRe accepts only a full 32-byte repeater pubkey. The regions
// request is addressed to one repeater, so the app always knows the full key;
// a prefix here would make the audit row ambiguous.
var targetPubkeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Bounds on one declared list. The list arrives from a client and nothing
// upstream validates its size, while the audit's verifier pays a pass over
// the window's packets per distinct name (see capVerifyRegions server-side).
// Real lists are a handful of short names; anything past these bounds is
// dropped and the answer marked truncated rather than stored whole.
const (
	maxDeclaredRegions   = 64
	maxDeclaredRegionLen = 64
)

// ClientRegionsAnswer is one validated region answer ready to store.
type ClientRegionsAnswer struct {
	Target     string
	RxPubkey   string
	ObservedAt string
	IngestedAt string
	RegionsCSV string
	Truncated  bool
}

// handleClientRegions processes a region-discovery answer from the mobile
// client topic. rxPubkey is the companion pubkey from the topic (ACL-bound by
// the broker), as for /packets and /rf.
func handleClientRegions(store *Store, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] regions: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	ans, reason := buildClientRegionsAnswer(rxPubkey, msg, tag)
	if ans == nil {
		log.Printf("MQTT [%s] regions from %.8s: %s, dropping", tag, rxPubkey, reason)
		return
	}
	if err := store.InsertDeclaredRegions(ans); err != nil {
		log.Printf("MQTT [%s] regions insert for %.12s: %v", tag, ans.Target, err)
		return
	}
	log.Printf("MQTT [%s] regions: %.12s declares [%s] (via %.8s, at %s)", tag, ans.Target, ans.RegionsCSV, rxPubkey, ans.ObservedAt)
	if name := stringField(msg, "origin"); name != "" {
		if err := store.UpsertClientObserver(rxPubkey, name, ans.IngestedAt); err != nil {
			log.Printf("MQTT [%s] client_observer upsert: %v", tag, err)
		}
	}
}

// buildClientRegionsAnswer validates a /regions payload. It returns nil and a
// reason when the message is not a usable answer.
//
// A missing or non-array "regions" is rejected, never read as an empty list:
// "answered with nothing" is a real finding the audit reports, and minting it
// from a malformed message would claim a repeater floods nothing.
func buildClientRegionsAnswer(rxPubkey string, msg map[string]interface{}, tag string) (*ClientRegionsAnswer, string) {
	if typ := stringField(msg, "type"); typ != "" && !strings.EqualFold(typ, "REGIONS") {
		return nil, fmt.Sprintf("unexpected type %q", typ)
	}
	target := strings.ToLower(strings.TrimSpace(stringField(msg, "target")))
	if !targetPubkeyRe.MatchString(target) {
		return nil, "target is not a full 64-hex pubkey"
	}
	raw, ok := msg["regions"].([]interface{})
	if !ok {
		return nil, "missing regions array"
	}
	truncated, _ := msg["truncated"].(bool)

	names := make([]string, 0, len(raw))
	for _, v := range raw {
		name, isStr := v.(string)
		name = strings.TrimSpace(name)
		// A comma cannot be represented in regions_csv, and a name that
		// could not be stored faithfully is better dropped and flagged than
		// silently split into two regions the repeater never declared.
		if !isStr || name == "" || strings.Contains(name, ",") || len(name) > maxDeclaredRegionLen {
			truncated = true
			continue
		}
		if len(names) == maxDeclaredRegions {
			truncated = true
			break
		}
		names = append(names, name)
	}

	// The answer's own instant orders it against every other answer for this
	// repeater: a drive buffered offline arrives late and must not overwrite a
	// newer one. resolveRxTimeCore already rejects future and >30-day-stale
	// clocks; RFC3339 matches configured_scope_at so the server's
	// newest-wins comparison is a plain string compare across both sources.
	rxTime, _ := resolveRxTimeCore(msg, tag)
	return &ClientRegionsAnswer{
		Target:     target,
		RxPubkey:   rxPubkey,
		ObservedAt: rxTime.Format(time.RFC3339),
		IngestedAt: time.Now().UTC().Format(time.RFC3339),
		RegionsCSV: strings.Join(names, ","),
		Truncated:  truncated,
	}, ""
}

// InsertDeclaredRegions stores one answer and drops any older answer for the
// same target. Every reader wants only the newest row per target, and a
// repeater a commuter passes daily is asked daily, so keeping the history
// would grow the table without bound for no consumer. A late-arriving older
// answer is inserted and then removed by the same statement pair, so it can
// never displace a newer one.
func (s *Store) InsertDeclaredRegions(a *ClientRegionsAnswer) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	truncated := 0
	if a.Truncated {
		truncated = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(target, rx_pubkey, observed_at) DO UPDATE SET
			ingested_at = excluded.ingested_at,
			regions_csv = excluded.regions_csv,
			truncated   = excluded.truncated`,
		a.Target, a.RxPubkey, a.ObservedAt, a.IngestedAt, a.RegionsCSV, truncated); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		DELETE FROM node_declared_regions
		WHERE target = ?
		  AND observed_at < (SELECT MAX(observed_at) FROM node_declared_regions WHERE target = ?)`,
		a.Target, a.Target); err != nil {
		return err
	}
	return tx.Commit()
}
