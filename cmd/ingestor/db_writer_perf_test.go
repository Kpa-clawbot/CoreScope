package main

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestWriterStarvationVisibleInPerf reproduces the #1339 class of bug:
// one component (neighbor_builder) holds the writer connection for an
// extended period; a second component (mqtt_handler) firing concurrent
// writes must show observable wait_ms in the perf snapshot.
//
// This is the gate test for issue #1340: SQLite write-lock instrumentation
// per component. If the wait_ms percentile collapses to zero, the
// observability gap remains and the regression class is invisible again.
//
// Runs ~60s — guarded by testing.Short() so fast unit-test passes can
// skip it locally, but CI runs `go test ./...` without -short.
func TestWriterStarvationVisibleInPerf(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 60s starvation test in short mode")
	}

	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const blockDur = 60 * time.Second

	// Blocker: acquire the writer via the wrapped Tx path, tag as
	// neighbor_builder, sleep 60s while holding the single conn,
	// then commit. This monopolises the writer for the duration.
	blockStarted := make(chan struct{})
	blockerDone := make(chan struct{})
	go func() {
		defer close(blockerDone)
		err := s.WriterTx("neighbor_builder", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE nodes SET name = name WHERE 0`); err != nil {
				return err
			}
			close(blockStarted)
			time.Sleep(blockDur)
			return nil
		})
		if err != nil {
			t.Errorf("blocker tx: %v", err)
		}
	}()

	// Wait for the blocker to be inside its transaction.
	<-blockStarted
	// Small safety margin so the blocker is firmly holding the conn.
	time.Sleep(100 * time.Millisecond)

	// Now fire several mqtt_handler writes. Each will block on the
	// single writer connection until the blocker commits.
	const followers = 5
	var wg sync.WaitGroup
	wg.Add(followers)
	for i := 0; i < followers; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, err := s.WriterExec(
				"mqtt_handler",
				`INSERT OR IGNORE INTO _migrations (name) VALUES (?)`,
				fmt.Sprintf("writer_starvation_test_%d", i),
			)
			if err != nil {
				t.Errorf("mqtt follower %d: %v", i, err)
			}
		}()
	}

	wg.Wait()
	<-blockerDone

	snap := s.WriterStatsSnapshot()
	mqtt, ok := snap["mqtt_handler"]
	if !ok {
		t.Fatalf("no perf snapshot for mqtt_handler component (got components: %v)", componentKeys(snap))
	}
	if mqtt.Count < followers {
		t.Fatalf("expected at least %d mqtt_handler samples, got %d", followers, mqtt.Count)
	}
	// This is the gate assertion. With instrumentation present the
	// follower writes should each register ~60s of wait_ms; p99 must
	// be well above 50_000ms. With instrumentation missing or broken
	// the percentile collapses to zero and this fails — which is the
	// exact regression class #1340 is meant to prevent.
	if mqtt.WaitMsP99 <= 50_000 {
		t.Fatalf("mqtt_handler wait_ms p99 = %.1fms, want > 50000ms; "+
			"writer starvation is invisible to /api/perf — issue #1340 not fixed",
			mqtt.WaitMsP99)
	}
}

func componentKeys(m map[string]WriterStatsSnapshot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
