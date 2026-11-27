package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ductingVirtualClock is the one clock of a simulated run: the refresh
// loop's sleep and the source's pacing both move it, nothing waits.
type ductingVirtualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ductingVirtualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *ductingVirtualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ductingSimRun is what a simulated run of the refresh loop did.
type ductingSimRun struct {
	stored []time.Duration // start of every refresh that stored a snapshot, since the run's start
	idle   []time.Duration // start of every refresh that stored nothing
	waits  []time.Duration // every wait between refreshes
	log    string
}

// ductingSimulate drives the real run loop on a virtual clock for span: the
// default config with a fixed area of 450 cells (9 batches, 1,890 gfs calls
// a fetch), against a fake Open-Meteo that answers 500 to the fourth batch
// of every fetch for failFor (0: never fails; negative: always).
func ductingSimulate(t *testing.T, capped bool, failFor, span time.Duration) ductingSimRun {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	start := time.Unix(1795046400, 0)
	clock := &ductingVirtualClock{t: start}
	f := &fakeOpenMeteo{t: t}
	var mu sync.Mutex
	seen, bad := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lat := r.URL.Query().Get("latitude")
		mu.Lock()
		seen++
		if seen == 4 {
			bad = lat
		}
		fail := lat == bad && (failFor < 0 || clock.now().Sub(start) < failFor)
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	area := [4]float64{40, -10, 47.5, 5} // 15 x 30 cells of 0.5 deg
	svc := newDuctingService(&DuctingConfig{Enabled: true, BaseURL: srv.URL, Area: &area}, nil)
	svc.source.capped = capped // the fake is on a loopback URL
	svc.now, svc.source.now = clock.now, clock.now
	svc.source.sleep = func(ctx context.Context, d time.Duration) error {
		clock.advance(d)
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var run ductingSimRun
	lastGen, began := int64(0), time.Duration(0)
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		if gen := svc.snap.Load().resp.GeneratedAt; gen != lastGen {
			lastGen = gen
			run.stored = append(run.stored, began)
		} else {
			run.idle = append(run.idle, began)
		}
		run.waits = append(run.waits, d)
		clock.advance(d)
		began = clock.now().Sub(start)
		if began >= span {
			cancel()
		}
		return ctx.Err()
	}
	select {
	case <-svc.run(ctx):
	case <-time.After(120 * time.Second):
		t.Fatal("run did not exit")
	}
	run.log = buf.String()
	if n := len(svc.snap.Load().resp.Cells); n == 0 || n > 450 {
		t.Fatalf("%d cells in the last snapshot", n)
	}
	if !strings.Contains(run.log, "step 0.5 deg, 450 cells") {
		t.Fatalf("the grid is not 450 cells: %s", run.log)
	}
	return run
}

func ductingMinutes(ds []time.Duration) string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = fmt.Sprintf("%.0f", d.Minutes())
	}
	return strings.Join(out, ", ")
}

// ductingSimMaxGap is the longest time without a stored refresh, the end of
// the run included.
func ductingSimMaxGap(run ductingSimRun, span time.Duration) time.Duration {
	gap, prev := time.Duration(0), time.Duration(0)
	for _, at := range append(append([]time.Duration(nil), run.stored...), span) {
		if at-prev > gap {
			gap = at - prev
		}
		prev = at
	}
	return gap
}

// A partial fetch is retried early only while the call budget has room for
// the retry and for the regular refreshes after it. Before that rule, one
// batch failing for good sent 5 whole-grid fetches in 195 minutes and the
// daily cap then refused every refresh for 26 hours.
func TestDuctingServicePartialRetryKeepsRefreshing(t *testing.T) {
	const span = 48 * time.Hour
	limit := defaultDuctingRefreshMinutes*time.Minute + time.Hour
	for name, failFor := range map[string]time.Duration{
		"one batch fails for good":              -1,
		"one batch fails for the first 3 hours": 3 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			run := ductingSimulate(t, true, failFor, span)
			t.Logf("stored refreshes at minute %s", ductingMinutes(run.stored))
			t.Logf("refreshes that stored nothing at minute %s", ductingMinutes(run.idle))
			if len(run.stored) == 0 || run.stored[0] != 0 {
				t.Fatalf("no refresh stored at the start: %v", run.stored)
			}
			if gap := ductingSimMaxGap(run, span); gap > limit {
				t.Errorf("longest time without a stored refresh %v, want at most %v", gap, limit)
			}
			if refused := strings.Contains(run.log, errDuctingBudget.Error()); len(run.idle) != 0 || refused {
				t.Errorf("%d refreshes stored nothing (budget refused: %v)", len(run.idle), refused)
			}
			// The first incomplete fetch is still retried after 5 minutes:
			// the budget has room for one extra fetch a day.
			if run.waits[0] != ductingBackoffBase {
				t.Errorf("first wait %v, want the early retry after %v", run.waits[0], ductingBackoffBase)
			}
			// The second is not: a third fetch within the hour is past the
			// hourly cap. The loop waits a refresh interval and says why.
			if run.waits[1] != defaultDuctingRefreshMinutes*time.Minute {
				t.Errorf("second wait %v, want the refresh interval", run.waits[1])
			}
			if !strings.Contains(run.log, "[ducting] no early retry: the call budget is kept for the regular refreshes; next refresh in 6h0m0s") {
				t.Errorf("the skipped retry is not logged: %s", run.log)
			}
		})
	}
}

// A self-hosted source has no call budget: every incomplete fetch is
// retried on the failure backoff.
func TestDuctingServicePartialRetrySelfHostedStaysEarly(t *testing.T) {
	run := ductingSimulate(t, false, -1, 12*time.Hour)
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 360}
	if len(run.waits) < len(want) {
		t.Fatalf("waits %v", run.waits)
	}
	for i, w := range want {
		if run.waits[i] != w*time.Minute {
			t.Errorf("wait %d: %v, want %v", i, run.waits[i], w*time.Minute)
		}
	}
	if len(run.idle) != 0 {
		t.Errorf("refreshes that stored nothing at minute %s", ductingMinutes(run.idle))
	}
}

// affords counts the recorded calls and the planned fetches before each
// time, in the rolling hour and day, and never changes the ledger.
func TestDuctingSourceAffords(t *testing.T) {
	now := time.Unix(1795046400, 0)
	at := func(mins ...int) []time.Time {
		out := make([]time.Time, len(mins))
		for i, m := range mins {
			out[i] = now.Add(time.Duration(m) * time.Minute)
		}
		return out
	}
	m, _ := ductingModel("gfs")
	src := newDuctingSource("", m) // the public API: capped
	src.now = func() time.Time { return now }
	const calls = 1890.0 // 450 gfs points
	for _, age := range []int{0, -300} {
		src.ledger = append(src.ledger, ductingLedgerEntry{at: now.Add(time.Duration(age) * time.Minute), calls: calls})
	}
	for _, tc := range []struct {
		name string
		at   []time.Time
		want bool
	}{
		{"nothing planned", nil, true},
		{"second fetch in the hour", at(5), true},
		{"third fetch in the hour", at(5, 15), false}, // 5,670 > 4,500
		{"an hour apart", at(5, 65), true},
		{"fifth fetch in the day", at(5, 365, 725), true},        // 9,450
		{"sixth fetch in the day", at(5, 365, 725, 1085), false}, // 11,340 > 9,500
		// At 1140 the fetch of 300 minutes ago is exactly a day old: out.
		{"oldest fetch has left the day", at(5, 365, 725, 1140), true},
		{"both recorded fetches have left", at(5, 365, 725, 1140, 1440), true},
	} {
		if got := src.affords(calls, tc.at); got != tc.want {
			t.Errorf("%s: affords = %v, want %v", tc.name, got, tc.want)
		}
	}
	if len(src.ledger) != 2 {
		t.Errorf("affords changed the ledger: %d entries", len(src.ledger))
	}
	src.capped = false
	if !src.affords(calls, at(5, 15, 25, 35)) {
		t.Error("a source with no call caps affords anything")
	}
}
