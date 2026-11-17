package main

import "testing"

func TestRepeaterKnown(t *testing.T) {
	const (
		rep  = "aa11000000000000000000000000000000000000000000000000000000000001"
		comp = "bb22000000000000000000000000000000000000000000000000000000000002"
		old  = "cc33000000000000000000000000000000000000000000000000000000000003"
		none = "dd44000000000000000000000000000000000000000000000000000000000004"
	)
	db := setupTestDB(t)
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, role) VALUES ('` + rep + `', 'r', 'Repeater')`,
		`INSERT INTO nodes (public_key, name, role) VALUES ('` + comp + `', 'c', 'companion')`,
		`CREATE TABLE inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`,
		`INSERT INTO inactive_nodes (public_key, name, role) VALUES ('` + old + `', 'o', 'repeater')`,
	} {
		if _, err := db.conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name         string
		pk           string
		found, isRep bool
	}{
		{"repeater", rep, true, true},
		{"companion", comp, true, false},
		{"retired repeater", old, true, true},
		{"unknown", none, false, false},
	}
	for _, c := range cases {
		f, r, err := db.RepeaterKnown(c.pk)
		if err != nil || f != c.found || r != c.isRep {
			t.Errorf("%s: got (%v,%v,%v); want (%v,%v,nil)", c.name, f, r, err, c.found, c.isRep)
		}
	}

	plain := setupTestDB(t)
	f, r, err := plain.RepeaterKnown(none)
	if err != nil || f || r {
		t.Errorf("no inactive_nodes: got (%v,%v,%v); want (false,false,nil)", f, r, err)
	}

	var nilDB *DB
	if _, _, err := nilDB.RepeaterKnown(rep); err == nil {
		t.Error("nil db: want error")
	}
	if _, _, err := (&DB{}).RepeaterKnown(rep); err == nil {
		t.Error("nil conn: want error")
	}
}
