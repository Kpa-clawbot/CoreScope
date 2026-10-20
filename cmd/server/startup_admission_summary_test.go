package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStartupAdmissionSummaryReheardOldHashKeepsCoverageFloor(t *testing.T) {
	dsn := postgresTestDSN(t)
	writer, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := applyTestSchema(t, writer); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	reheardFirst := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	inactive := now.Add(-48 * time.Hour)
	if _, err := writer.Exec(`INSERT INTO observers(rowid,id,name) VALUES(1,'summary-observer','Summary fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen,route_type,payload_type,decoded_json)
		VALUES(1,'AA','summary-reheard',$1,$2,0,4,'{}'),
		      (2,'BB','summary-inactive',$3,$4,0,4,'{}')`,
		reheardFirst, now.Unix(), inactive.Format(time.RFC3339), inactive.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp)
		VALUES(1,1,1,'[]',$1),(2,2,1,'[]',$2)`, now.Unix(), inactive.Unix()); err != nil {
		t.Fatal(err)
	}
	db, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()
	s := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 24, HotStartupHours: 0})
	started := time.Now().UTC()
	logs := captureLog(func() {
		if err := s.RunStartupLoad(1); err != nil {
			t.Fatal(err)
		}
	})
	finished := time.Now().UTC()
	if len(s.packets) != 1 || s.byTxID[1] == nil || s.byTxID[2] != nil {
		t.Fatal("fixture must retain the re-heard hash and omit the inactive hash")
	}
	if !strings.Contains(logs, fmt.Sprintf("firstSeenRange=[%q,%q]", reheardFirst, reheardFirst)) {
		t.Fatal("summary lost the actual retained first-seen range", logs)
	}
	_, rest, found := strings.Cut(logs, `coverageFrom="`)
	coverage, _, closed := strings.Cut(rest, `"`)
	if !found || !closed {
		t.Fatal("summary has no quoted coverage floor", logs)
	}
	floor, err := time.Parse(time.RFC3339, coverage)
	if err != nil || floor.Before(started.Add(-24*time.Hour).Truncate(time.Second)) || floor.After(finished.Add(-24*time.Hour)) {
		t.Fatalf("summary claims coverageFrom=%q outside the complete 24-hour window; retained first_seen=%q: %v", coverage, reheardFirst, err)
	}
}

func TestStartupAdmissionSummary(t *testing.T) {
	for _, tc := range []struct {
		name          string
		transmissions int
		observations  int
		hotHours      float64
		budgetMB      int
		closeDB       bool
		reason        string
	}{
		{"empty", 0, 0, 0, 0, false, "window_complete"},
		{"full", 4, 2, 0, 0, false, "window_complete"},
		{"background", 4, 2, 1, 0, false, "window_complete"},
		{"budget", 40, 300, 0, 1, false, "memory_budget"},
		{"background_budget", 40, 300, 1, 1, false, "memory_budget"},
		{"error", 4, 2, 1, 1, true, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := startupMemoryFixture(t, tc.transmissions, tc.observations, tc.hotHours == 0)
			s := NewPacketStore(db, &PacketStoreConfig{
				MaxMemoryMB: tc.budgetMB, RetentionHours: 24, HotStartupHours: tc.hotHours,
			})
			if tc.closeDB {
				db.conn.Close()
			}
			logs := captureLog(func() {
				err := s.RunStartupLoad(2)
				if (err != nil) != tc.closeDB {
					t.Fatalf("startup error=%v, want error=%v", err, tc.closeDB)
				}
			})
			var summaries []string
			for _, line := range strings.Split(logs, "\n") {
				if strings.Contains(line, "[store] startup admission:") {
					summaries = append(summaries, line)
				}
			}
			if len(summaries) != 1 {
				t.Fatalf("want one terminal admission summary, got %d:\n%s", len(summaries), logs)
			}
			first, last := "", ""
			if len(s.packets) > 0 {
				first, last = s.packets[0].FirstSeen, s.packets[len(s.packets)-1].FirstSeen
			}
			for _, want := range []string{
				fmt.Sprintf("budgetMB=%d", tc.budgetMB),
				fmt.Sprintf("transmissions=%d observations=%d", len(s.packets), s.totalObs),
				fmt.Sprintf("firstSeenRange=[%q,%q]", first, last),
				fmt.Sprintf("coverageFrom=%q", s.oldestLoaded),
				"stop=" + tc.reason,
				"formerPolicyTransmissionEstimate=",
			} {
				if !strings.Contains(summaries[0], want) {
					t.Errorf("summary missing %q: %s", want, summaries[0])
				}
			}
			if tc.name == "background" && len(s.packets) != tc.transmissions {
				t.Fatalf("summary preceded background fill: %d/%d packets", len(s.packets), tc.transmissions)
			}
		})
	}
}
