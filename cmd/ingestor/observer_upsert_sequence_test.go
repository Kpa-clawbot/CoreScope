package main

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// observers.idx is INTEGER PRIMARY KEY AUTOINCREMENT, and SQLite advances
// sqlite_sequence for every INSERT it attempts — including one that ON
// CONFLICT turns into an update. The observer upserts run per ingested
// packet, so they must take an UPDATE when the row exists and INSERT only
// for a new observer: otherwise every packet writes the sequence page too.

func observersSeq(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'observers'`).Scan(&seq); err != nil {
		t.Fatalf("read observers sequence: %v", err)
	}
	return seq
}

func observerIdx(t *testing.T, db *sql.DB, id string) int64 {
	t.Helper()
	var idx int64
	if err := db.QueryRow(`SELECT idx FROM observers WHERE id = ?`, id).Scan(&idx); err != nil {
		t.Fatalf("idx of %s: %v", id, err)
	}
	return idx
}

func TestUpsertObserverAtLeavesSequenceAlone(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserverAt("obs-a", "A", "sjc", nil, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	seq := observersSeq(t, s.db)

	const n = 1000
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		if err := s.UpsertObserverAt("obs-a", "A", "sjc", nil, ts); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	if got := observersSeq(t, s.db); got != seq {
		t.Fatalf("observers sequence moved %d -> %d over %d upserts of an existing observer", seq, got, n)
	}
	var count int
	var lastSeen string
	if err := s.db.QueryRow(`SELECT packet_count, last_seen FROM observers WHERE id = 'obs-a'`).Scan(&count, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if want := base.Add(n * time.Second).Format(time.RFC3339); count != n+1 || lastSeen != want {
		t.Fatalf("obs-a packet_count=%d last_seen=%s, want %d / %s", count, lastSeen, n+1, want)
	}
	if got := s.Stats.ObserverUpserts.Load(); got != n+1 {
		t.Fatalf("ObserverUpserts = %d, want %d", got, n+1)
	}

	var maxIdx int64
	if err := s.db.QueryRow(`SELECT MAX(idx) FROM observers`).Scan(&maxIdx); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertObserverAt("obs-b", "B", "sjc", nil, "2026-01-02T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if got := observerIdx(t, s.db, "obs-b"); got != maxIdx+1 {
		t.Fatalf("new observer idx = %d, want %d", got, maxIdx+1)
	}
	var count2 int
	if err := s.db.QueryRow(`SELECT packet_count FROM observers WHERE id = 'obs-b'`).Scan(&count2); err != nil || count2 != 1 {
		t.Fatalf("new observer packet_count = %d (%v), want 1", count2, err)
	}
}

func TestRecordNaiveSkewLeavesSequenceAlone(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertObserverAt("obs-a", "A", "sjc", nil, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	seq := observersSeq(t, s.db)

	const n = 1000
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		if err := s.RecordNaiveSkew("obs-a", -3600, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("skew %d: %v", i, err)
		}
	}
	if got := observersSeq(t, s.db); got != seq {
		t.Fatalf("observers sequence moved %d -> %d over %d skew records of an existing observer", seq, got, n)
	}
	var count int
	if err := s.db.QueryRow(`SELECT clock_skew_count_24h FROM observers WHERE id = 'obs-a'`).Scan(&count); err != nil || count != n {
		t.Fatalf("obs-a clock_skew_count_24h = %d (%v), want %d", count, err, n)
	}

	// An observer the analyzer has not seen yet still gets a row.
	if err := s.RecordNaiveSkew("obs-new", 7200, now); err != nil {
		t.Fatal(err)
	}
	var skew sql.NullInt64
	var newCount int
	var lastNaive string
	if err := s.db.QueryRow(`SELECT clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at FROM observers WHERE id = 'obs-new'`).
		Scan(&skew, &newCount, &lastNaive); err != nil {
		t.Fatalf("unknown observer row: %v", err)
	}
	if skew.Int64 != 7200 || newCount != 1 || lastNaive != now.Format(time.RFC3339) {
		t.Fatalf("unknown observer row: skew=%v count=%d last=%s", skew, newCount, lastNaive)
	}
	if got := observerIdx(t, s.db, "obs-new"); got != seq+1 {
		t.Fatalf("new observer idx = %d, want %d", got, seq+1)
	}
}

// observerRowCols is every observers column but the two keys, which differ by
// construction between the rows an equivalence case compares.
const observerRowCols = `name, iata, last_seen, first_seen, packet_count, model, firmware,
	client_version, radio, battery_mv, uptime_secs, noise_floor, inactive, last_packet_at,
	clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at, can_relay, can_relay_seen`

func observerRow(t *testing.T, db *sql.DB, id string) []string {
	t.Helper()
	rows := dumpQuery(t, db, `SELECT `+observerRowCols+` FROM observers WHERE id = '`+id+`'`)
	if len(rows) != 1 {
		t.Fatalf("observer %s: %d rows", id, len(rows))
	}
	return rows
}

// seedObserverPair inserts two observers, "upd" and "old", with identical
// columns.
func seedObserverPair(t *testing.T, db *sql.DB, vals []any) {
	t.Helper()
	for _, id := range []string{"upd", "old"} {
		args := append([]any{id}, vals...)
		if _, err := db.Exec(`INSERT INTO observers (id, `+observerRowCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func boolp(b bool) *bool { return &b }

// The UPDATE fast path leaves an existing observer exactly as the INSERT ...
// ON CONFLICT DO UPDATE statement (still the fallback) does.
func TestUpdateObserverMatchesUpsert(t *testing.T) {
	const now = "2026-06-01T12:00:00Z"
	full := []any{"Old name", "SJC", "2026-06-01T10:00:00Z", "2026-01-01T00:00:00Z", 7,
		"L1", "1.15", "cv1", "sx1262", 3700, 99, -110.5, 0, "2026-06-01T09:00:00Z",
		-30, 2, "2026-06-01T08:00:00Z", 1, 0}
	with := func(i int, v any) []any {
		out := append([]any(nil), full...)
		out[i] = v
		return out
	}
	cases := []struct {
		name     string
		seed     []any
		obsName  string
		meta     *ObserverMeta
		lastSeen string
		want     map[int]string // column index -> expected value, beyond equivalence
	}{
		{"nil meta keeps every column", full, "New name", nil, "2026-06-01T11:00:00Z",
			map[int]string{0: "New name", 2: "2026-06-01T11:00:00Z", 4: "8", 5: "L1", 17: "1", 18: "0"}},
		{"empty strings overwrite, nil fields keep", full, "", &ObserverMeta{Model: strp(""), Firmware: strp("1.16")}, "2026-06-01T11:00:00Z",
			map[int]string{0: "", 5: "", 6: "1.16", 7: "cv1"}},
		{"NULL columns take meta", []any{nil, nil, nil, nil, 0, nil, nil, nil, nil, nil, nil, nil, 0, nil, nil, 0, nil, 1, 0},
			"N", &ObserverMeta{Model: strp("L2")}, "2026-06-01T11:00:00Z",
			map[int]string{0: "N", 2: "2026-06-01T11:00:00Z", 5: "L2"}},
		{"last_seen does not move backwards", full, "N", nil, "2026-06-01T09:00:00Z",
			map[int]string{2: "2026-06-01T10:00:00Z"}},
		{"future last_seen clamps to ingest time", with(2, "2027-01-01T00:00:00Z"), "N", nil, "2026-06-01T11:00:00Z",
			map[int]string{2: now}},
		{"NULL packet_count stays NULL", with(4, nil), "N", nil, "2026-06-01T11:00:00Z",
			map[int]string{4: "<nil>"}},
		{"can_relay unset keeps seen=1 state", with(18, 1), "N", nil, "2026-06-01T11:00:00Z",
			map[int]string{17: "1", 18: "1"}},
		{"repeat:off", full, "N", &ObserverMeta{CanRelay: boolp(false)}, "2026-06-01T11:00:00Z",
			map[int]string{17: "0", 18: "1"}},
		{"repeat:on after off", with(17, 0), "N", &ObserverMeta{CanRelay: boolp(true)}, "2026-06-01T11:00:00Z",
			map[int]string{17: "1", 18: "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedObserverPair(t, s.db, tc.seed)
			seeded := observerRow(t, s.db, "old")

			u := observerUpsert{name: tc.obsName, iata: "SJC", lastSeen: tc.lastSeen, ingestNow: now}
			u.model, u.firmware, u.clientVersion, u.radio, u.batteryMv, u.uptimeSecs, u.noiseFloor, u.canRelay = observerMetaColumns(tc.meta)
			u.id = "upd"
			updated, err := s.updateObserver(u)
			if err != nil || !updated {
				t.Fatalf("updateObserver = %v, %v; want true, nil", updated, err)
			}
			u.id = "old"
			if err := s.insertObserver(u); err != nil {
				t.Fatal(err)
			}

			got, want := observerRow(t, s.db, "upd"), observerRow(t, s.db, "old")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("UPDATE path differs from the upsert:\nseed  =%v\nupdate=%v\nupsert=%v", seeded, got, want)
			}
			cols := dumpQueryCols(t, s.db, `SELECT `+observerRowCols+` FROM observers WHERE id = 'upd'`)
			for i, v := range tc.want {
				if cols[i] != v {
					t.Errorf("column %d = %q, want %q (row %v)", i, cols[i], v, got)
				}
			}
		})
	}

	t.Run("unknown observer", func(t *testing.T) {
		s := newTestStore(t)
		updated, err := s.updateObserver(observerUpsert{id: "nobody", name: "N", lastSeen: now, ingestNow: now})
		if err != nil || updated {
			t.Fatalf("updateObserver on a missing row = %v, %v; want false, nil", updated, err)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observers`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("observers rows = %d (%v), want 0", n, err)
		}
	})
}

// The UPDATE fast path of RecordNaiveSkew leaves an existing observer exactly
// as the INSERT ... ON CONFLICT DO UPDATE fallback does.
func TestUpdateNaiveSkewMatchesUpsert(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	nowStr := now.Format(time.RFC3339)
	cutoff := now.Add(-24 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name      string
		lastNaive any
		count     any
		wantCount string
	}{
		{"first event", nil, 0, "1"},
		{"previous event outside 24h resets", "2026-05-30T12:00:00Z", 9, "1"},
		{"previous event inside 24h increments", "2026-06-01T11:00:00Z", 9, "10"},
		{"previous event exactly at the cutoff increments", cutoff, 9, "10"},
		{"NULL count inside 24h", "2026-06-01T11:00:00Z", nil, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedObserverPair(t, s.db, []any{"N", "SJC", "2026-06-01T10:00:00Z", "2026-01-01T00:00:00Z", 3,
				nil, nil, nil, nil, nil, nil, nil, 0, nil, 120, tc.count, tc.lastNaive, 1, 0})

			updated, err := s.updateNaiveSkew("upd", -3600, nowStr, cutoff)
			if err != nil || !updated {
				t.Fatalf("updateNaiveSkew = %v, %v; want true, nil", updated, err)
			}
			if err := s.insertNaiveSkew("old", -3600, nowStr, cutoff); err != nil {
				t.Fatal(err)
			}
			got, want := observerRow(t, s.db, "upd"), observerRow(t, s.db, "old")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("UPDATE path differs from the upsert:\nupdate=%v\nupsert=%v", got, want)
			}
			cols := dumpQueryCols(t, s.db, `SELECT clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at FROM observers WHERE id = 'upd'`)
			if cols[0] != "-3600" || cols[1] != tc.wantCount || cols[2] != nowStr {
				t.Fatalf("skew columns = %v, want [-3600 %s %s]", cols, tc.wantCount, nowStr)
			}
		})
	}

	t.Run("unknown observer", func(t *testing.T) {
		s := newTestStore(t)
		updated, err := s.updateNaiveSkew("nobody", 60, nowStr, cutoff)
		if err != nil || updated {
			t.Fatalf("updateNaiveSkew on a missing row = %v, %v; want false, nil", updated, err)
		}
	})
}

// dumpQueryCols returns the single row of query as one string per column.
func dumpQueryCols(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("%s: no row", query)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		out[i] = fmt.Sprintf("%v", v)
	}
	return out
}
