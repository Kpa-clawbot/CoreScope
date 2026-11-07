package dbschema

import (
	"testing"
)

// #1869: the first Apply seeds observer_radios with every observer's
// current radio, once; later Applies leave the history alone.
func TestEnsureObserverRadiosSeedsOnce(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	for _, s := range []string{
		`ALTER TABLE observers ADD COLUMN radio TEXT`,
		`ALTER TABLE observers ADD COLUMN first_seen TEXT`,
		`ALTER TABLE observers ADD COLUMN last_seen TEXT`,
		`INSERT INTO observers (id, name, radio, first_seen, last_seen) VALUES
			('A', 'a', ' 869.4320068,62.50,7,5', '2026-08-01T00:00:00Z', '2026-10-25T09:00:00Z'),
			('B', 'b', NULL,               '2026-08-01T00:00:00Z', '2026-10-25T09:00:00Z'),
			('C', 'c', '  ',               '2026-08-01T00:00:00Z', '2026-10-25T09:00:00Z')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureObserverRadios(db, nil); err != nil {
		t.Fatal(err)
	}
	var obs, radio, first, last string
	if err := db.QueryRow(`SELECT observer_id, radio, first_seen, last_seen FROM observer_radios`).
		Scan(&obs, &radio, &first, &last); err != nil {
		t.Fatal(err)
	}
	if obs != "A" || radio != "869.432,62.5,7,5" || first != "2026-08-01T00:00:00Z" || last != "2026-10-25T09:00:00Z" {
		t.Errorf("seed row = %s %s %s %s", obs, radio, first, last)
	}

	// Retuned since: a second Apply must not add a row that claims the new
	// setting covered the whole past.
	if _, err := db.Exec(`UPDATE observers SET radio = '869.618,62.5,8,8'`); err != nil {
		t.Fatal(err)
	}
	if err := ensureObserverRadios(db, func(string, ...interface{}) {}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observer_radios`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rows after second Apply = %d, want 1", n)
	}
}

// A fixture without observers.radio still gets the tables.
func TestEnsureObserverRadiosWithoutRadioColumn(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	if err := Apply(db, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"observer_radios", "observation_radios"} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestCanonicalRadio(t *testing.T) {
	for in, want := range map[string]string{
		"869.4320068,62.5,7,5": "869.432,62.5,7,5",
		" 869.618,62.50,8,8 ":  "869.618,62.5,8,8",
		"910.5250244,250,11,5": "910.525,250,11,5",
		"869.618, 62.5, 8, 8":  "869.618,62.5,8,8",
		"433.175000,125.0,9,6": "433.175,125,9,6",
		"SX1262":               "SX1262",
		"869.432,62.5,seven,5": "869.432,62.5,seven,5",
		"869.432,,7,5":         "869.432,,7,5",
		"NaN,62.5,7,5":         "NaN,62.5,7,5",
		"Inf,62.5,7,5":         "Inf,62.5,7,5",
		"":                     "",
	} {
		if got := CanonicalRadio(in); got != want {
			t.Errorf("CanonicalRadio(%q) = %q, want %q", in, got, want)
		}
	}
}
