package main

import (
	"database/sql"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// Radio history of observers (#1869). Schema and the reasoning behind it:
// internal/dbschema/observer_radios.go.

// maxRadioIDLen bounds the operator-chosen radio id a multi-radio node puts
// on its packets and in its status `radios` map.
const maxRadioIDLen = 64

// RadioReport is one radio an observer says it listens on: the main `radio`
// field of the status message (ID "") or one entry of its `radios` map.
// Radio is canonical (dbschema.CanonicalRadio).
type RadioReport struct {
	ID    string
	Radio string
}

// extractRadioReports reads the radio settings out of an MQTT status message.
// The main `radio` field comes first; a multi-radio openHop node adds a
// `radios: [{id, radio, ...}]` list whose ids its packets carry as
// `rx_radio_id`. Entries without an id or a setting are skipped.
func extractRadioReports(msg map[string]interface{}) []RadioReport {
	var out []RadioReport
	if v, ok := msg["radio"].(string); ok {
		if v = clampObserverField(dbschema.CanonicalRadio(v), maxObserverTextLen); v != "" {
			out = append(out, RadioReport{Radio: v})
		}
	}
	list, _ := msg["radios"].([]interface{})
	for _, item := range list {
		e, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := e["id"].(string)
		radio, _ := e["radio"].(string)
		id = clampObserverField(strings.TrimSpace(id), maxRadioIDLen)
		radio = clampObserverField(dbschema.CanonicalRadio(radio), maxObserverTextLen)
		if id == "" || radio == "" {
			continue
		}
		out = append(out, RadioReport{ID: id, Radio: radio})
	}
	return out
}

// RecordObserverRadios extends the observer's current stretch on each
// reported setting, or opens a new stretch when the setting changed.
//
// Called for live status messages only: a retained replay describes a past
// of unknown age and must not stretch a row up to now. A retune that only
// ever reaches the analyzer as a retained message (the observer went quiet
// right after it) therefore shows in observers.radio but not here.
//
// `now` is when the status was handled, not when the observer retuned: the
// observer reports its setting only every few minutes, so packets heard
// between a retune and the next status are attributed to the previous
// setting. The server resolves by first_seen alone.
func (s *Store) RecordObserverRadios(observerID string, reports []RadioReport, now time.Time) error {
	if observerID == "" || len(reports) == 0 {
		return nil
	}
	ts := now.UTC().Format(time.RFC3339)
	var firstErr error
	for _, r := range reports {
		var id int64
		var radio string
		err := s.db.QueryRow(`
			SELECT id, radio FROM observer_radios
			WHERE observer_id = ? AND radio_id = ?
			ORDER BY first_seen DESC, id DESC LIMIT 1`, observerID, r.ID).Scan(&id, &radio)
		switch {
		case err == nil && radio == r.Radio:
			_, err = s.db.Exec(`UPDATE observer_radios SET last_seen = ? WHERE id = ? AND last_seen < ?`, ts, id, ts)
		case err == nil || errors.Is(err, sql.ErrNoRows):
			_, err = s.db.Exec(`
				INSERT INTO observer_radios (observer_id, radio_id, radio, first_seen, last_seen)
				VALUES (?, ?, ?, ?, ?)`, observerID, r.ID, r.Radio, ts, ts)
		}
		if err != nil {
			// Keep going: one radio's failure must not drop the others.
			s.Stats.WriteErrors.Add(1)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// recordObservationRadio notes which radio of a multi-radio observer heard
// the observation. Caller holds writerMu (InsertTransmission).
func (s *Store) recordObservationRadio(observationID int64, radioID string) {
	if observationID == 0 || radioID == "" {
		return
	}
	if _, err := s.stmtInsertObservationRadio.Exec(observationID, radioID); err != nil {
		s.Stats.WriteErrors.Add(1)
		log.Printf("[db] observation radio (non-fatal): %v", err)
	}
}
