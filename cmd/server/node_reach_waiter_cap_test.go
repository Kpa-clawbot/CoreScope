package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Done is evaluated only when acquire reaches its queued select. This lets
// tests fill the queue without sleeps or inspecting its implementation.
type reachQueueContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *reachQueueContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func waitForReachQueue(t *testing.T, ctx *reachQueueContext) {
	t.Helper()
	select {
	case <-ctx.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cold build did not enter the queue")
	}
}

func requireReachQueueFull(t *testing.T, l *reachColdLimiter) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.acquire(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			l.release()
		}
		if err != errReachColdBusy {
			t.Fatalf("overflow acquire = %v, want errReachColdBusy", err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("overflow acquire waited with eight cold builds already queued; want immediate rejection")
	}

	// A full queue must reject before creating a timer or retaining another
	// queued build. With the channels initialized, rejection needs no allocation.
	allocs := testing.AllocsPerRun(100, func() {
		if err := l.acquire(context.Background()); err != errReachColdBusy {
			t.Fatalf("overflow acquire = %v, want errReachColdBusy", err)
		}
	})
	if allocs != 0 {
		t.Fatalf("full-queue rejection allocated %g times, want 0 (no wait timer)", allocs)
	}
}

// The ninth distinct queued leader must be shed, and every way out of the
// queue must return its admission so a later leader can wait in its place.
func TestReachColdLimiter_QueueCapAndAdmissionReuse(t *testing.T) {
	for _, exit := range []string{"success", "cancellation", "timeout"} {
		t.Run(exit, func(t *testing.T) {
			var l reachColdLimiter
			l.configure(2, 3*time.Second, nil)
			held := 0
			for i := 0; i < 2; i++ {
				if err := l.acquire(context.Background()); err != nil {
					t.Fatalf("hold slot %d: %v", i, err)
				}
				held++
			}
			t.Cleanup(func() {
				for i := 0; i < held; i++ {
					l.release()
				}
			})

			type result struct {
				index int
				err   error
			}
			results := make(chan result, 16)
			queue := func(index int) context.CancelFunc {
				ctx, cancel := context.WithCancel(context.Background())
				entered := &reachQueueContext{Context: ctx, entered: make(chan struct{})}
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					err := l.acquire(entered)
					results <- result{index, err}
					if err == nil {
						<-ctx.Done() // keep a promoted build active until cleanup
						l.release()
					}
				}()
				t.Cleanup(func() { cancel(); <-finished })
				waitForReachQueue(t, entered)
				return cancel
			}
			cancels := make([]context.CancelFunc, 8)
			for i := range cancels {
				cancels[i] = queue(i)
			}
			requireReachQueueFull(t, &l)

			vacated := 1
			switch exit {
			case "success":
				l.release()
				held--
				if got := <-results; got.err != nil {
					t.Fatalf("promoted acquire = %v, want nil", got.err)
				}
			case "cancellation":
				cancels[0]()
				if got := <-results; got.index != 0 || got.err != context.Canceled {
					t.Fatalf("cancelled acquire = %+v, want waiter 0 with context.Canceled", got)
				}
			case "timeout":
				vacated = len(cancels)
				for i := 0; i < vacated; i++ {
					if got := <-results; got.err != errReachColdBusy {
						t.Fatalf("timed-out acquire = %v, want errReachColdBusy", got.err)
					}
				}
			}
			for i := 0; i < vacated; i++ {
				queue(8 + i)
			}
			requireReachQueueFull(t, &l)
			if got := l.peakInflight(); got != 2 {
				t.Fatalf("peak active builds = %d, want 2", got)
			}
		})
	}
}

// A full queue protects the read pool while warm hits and same-key followers
// still work. Only the eight distinct cold leaders may start scans afterward.
func TestNodeReach_FullColdQueuePreservesWarmHitsAndCoalescing(t *testing.T) {
	srv, db, n := newColdLimitServer(t, 0, 10*time.Second, nil)
	path := "/api/nodes/" + n + "/reach?days="
	warm := serveReach(srv, path+"30")
	if warm.Code != http.StatusOK {
		t.Fatalf("warm-up status = %d, want 200", warm.Code)
	}
	for i := 0; i < 2; i++ {
		if err := srv.reach.cold.acquire(context.Background()); err != nil {
			t.Fatalf("hold slot %d: %v", i, err)
		}
	}
	held := true
	t.Cleanup(func() {
		if held {
			srv.reach.cold.release()
			srv.reach.cold.release()
		}
	})
	// Hold the fixture's sole connection: any premature query would queue on
	// the pool and increment WaitCount, even if it later completed normally.
	conn, err := db.conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	waits := db.conn.Stats().WaitCount

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 20)
	t.Cleanup(func() { cancel(); wg.Wait() })
	for days := 1; days <= 8; days++ {
		queued := &reachQueueContext{Context: ctx, entered: make(chan struct{})}
		wg.Add(1)
		go func(days int) {
			defer wg.Done()
			results <- serveReachCtx(srv, queued, path+strconv.Itoa(days))
		}(days)
		waitForReachQueue(t, queued)
	}

	overflowCtx, cancelOverflow := context.WithCancel(ctx)
	defer cancelOverflow()
	overflow := make(chan *httptest.ResponseRecorder, 1)
	go func() { overflow <- serveReachCtx(srv, overflowCtx, path+"9") }()
	select {
	case rr := <-overflow:
		if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "2" {
			t.Fatalf("full queue: status=%d Retry-After=%q, want 429 and 2", rr.Code, rr.Header().Get("Retry-After"))
		}
		var body struct {
			Error      string `json:"error"`
			RetryAfter int    `json:"retryAfter"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == "" || body.RetryAfter != 2 {
			t.Fatalf("full queue body = %s, want JSON error and retryAfter=2 (decode=%v)", rr.Body.String(), err)
		}
	case <-time.After(time.Second):
		cancelOverflow()
		<-overflow
		t.Fatal("ninth distinct cold leader queued; want immediate HTTP 429")
	}
	if rr := serveReach(srv, path+"30"); rr.Code != http.StatusOK || rr.Body.String() != warm.Body.String() {
		t.Fatalf("warm hit with full queue: status=%d body=%s", rr.Code, rr.Body.String())
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- serveReachCtx(srv, ctx, path+"1")
		}()
	}
	select {
	case rr := <-results:
		t.Fatalf("queued leader or same-key follower returned early: status=%d", rr.Code)
	case <-time.After(100 * time.Millisecond):
	}
	if got := db.conn.Stats().WaitCount; got != waits {
		t.Fatalf("pool wait count = %d, want %d (queued work must not request a connection)", got, waits)
	}
	if got := srv.reachCacheLen(); got != 1 {
		t.Fatalf("cache entries = %d, want only the warm report", got)
	}
	conn.Close()
	srv.reach.cold.release()
	srv.reach.cold.release()
	held = false
	for i := 0; i < 20; i++ {
		select {
		case rr := <-results:
			if rr.Code != http.StatusOK {
				t.Fatalf("after freeing slots: status=%d, want 200 (body=%s)", rr.Code, rr.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("admitted request did not finish after freeing slots")
		}
	}
	if got := srv.reach.cold.acquiredCount(); got != 11 {
		t.Fatalf("acquired = %d, want 1 warm-up + 2 holds + 8 distinct cold leaders", got)
	}
	if got := srv.reach.cold.rejectedCount(); got != 1 {
		t.Fatalf("rejected = %d, want only the excess distinct leader", got)
	}
}
