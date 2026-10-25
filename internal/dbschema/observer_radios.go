package dbschema

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Radio settings an observer listened on, over time (#1869).
//
// observers.radio holds only the LAST "MHz,kHz,SF,CR" an observer reported,
// so once an observer is retuned every packet it ever heard reads as heard
// on the new setting. observer_radios keeps one row per stretch of time an
// observer (and, on a multi-radio node, one of its radios) stayed on one
// setting: the row is extended while the setting holds and a new row starts
// when it changes. Settings are stored canonical (CanonicalRadio), so two
// spellings of one setting never open a new row.
//
// radio_id is "" for the observer's main `radio` field. A multi-radio
// openHop node also reports `radios: [{id, radio}]` in its status and tags
// each packet with `rx_radio_id`; those ids are operator-chosen and unique
// only within one node, hence the (observer_id, radio_id) key.
//
// observation_radios carries that per-packet rx_radio_id, keyed by the
// observation row. It is sparse -- written only for packets that came with
// the field, which a single-radio node never sends -- so the observations
// table and every query that scans it stay untouched.
//
// Neither table is required by AssertReady: a server that comes up before
// the ingestor created them shows no radio history and probes again on its
// next refresh, the same rule as advert_route_evidence.
//
// PREFLIGHT: async=false reason="two small tables; the one-time seed copies one row per observer from observers"
func ensureObserverRadios(rw *sql.DB, logf Logger) error {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	if err := ensureMigrationsTable(rw); err != nil {
		return err
	}
	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS observer_radios (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			observer_id TEXT NOT NULL,
			radio_id    TEXT NOT NULL DEFAULT '',
			radio       TEXT NOT NULL,
			first_seen  TEXT NOT NULL,
			last_seen   TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_observer_radios_obs
			ON observer_radios(observer_id, radio_id, first_seen);
		CREATE TABLE IF NOT EXISTS observation_radios (
			observation_id INTEGER PRIMARY KEY,
			radio_id       TEXT NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("create observer radio tables: %w", err)
	}

	// Seed: every observer that already reports a radio gets one row
	// covering its whole known life. Until now the only record was the
	// current setting, so that is the best statement about the past there
	// is -- and the one the UI made all along.
	var done int
	err := rw.QueryRow(`SELECT 1 FROM _migrations WHERE name = 'observer_radios_seed_v1'`).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hasRadio, err := TableHasColumn(rw, "observers", "radio")
	if err != nil {
		return err
	}
	if hasRadio {
		n, err := seedObserverRadios(rw)
		if err != nil {
			return fmt.Errorf("seed observer_radios: %w", err)
		}
		if n > 0 {
			logf("[dbschema] seeded observer_radios with the current radio of %d observers (#1869)", n)
		}
	}
	_, err = rw.Exec(`INSERT OR IGNORE INTO _migrations (name) VALUES ('observer_radios_seed_v1')`)
	return err
}

func seedObserverRadios(rw *sql.DB) (int, error) {
	type row struct{ id, radio, first, last string }
	rows, err := rw.Query(`
		SELECT id, radio, COALESCE(first_seen, last_seen), COALESCE(last_seen, first_seen)
		FROM observers
		WHERE radio IS NOT NULL AND TRIM(radio) != ''
		  AND COALESCE(first_seen, last_seen) IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM observer_radios r WHERE r.observer_id = observers.id)`)
	if err != nil {
		return 0, err
	}
	var seed []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.radio, &r.first, &r.last); err != nil {
			rows.Close()
			return 0, err
		}
		seed = append(seed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range seed {
		if _, err := rw.Exec(`INSERT INTO observer_radios (observer_id, radio_id, radio, first_seen, last_seen)
			VALUES (?, '', ?, ?, ?)`, r.id, CanonicalRadio(r.radio), r.first, r.last); err != nil {
			return 0, err
		}
	}
	return len(seed), nil
}

// CanonicalRadio normalises an observer's "MHz,kHz,SF,CR" so that one setting
// reads the same from every client: "869.4320068,62.5,7,5" (a float32
// frequency printed in full) and "869.432,62.50,7,5" both become
// "869.432,62.5,7,5". Anything that does not parse as four finite numbers is
// returned trimmed, as is. Shared by the ingestor (what it stores) and the
// server (what a filter asks for).
func CanonicalRadio(s string) string {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return s
	}
	freq, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	bw, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	sf, err3 := strconv.Atoi(strings.TrimSpace(parts[2]))
	cr, err4 := strconv.Atoi(strings.TrimSpace(parts[3]))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil ||
		math.IsInf(freq, 0) || math.IsNaN(freq) || math.IsInf(bw, 0) || math.IsNaN(bw) {
		return s
	}
	return strconv.FormatFloat(math.Round(freq*1000)/1000, 'f', -1, 64) + "," +
		strconv.FormatFloat(math.Round(bw*100)/100, 'f', -1, 64) + "," +
		strconv.Itoa(sf) + "," + strconv.Itoa(cr)
}
