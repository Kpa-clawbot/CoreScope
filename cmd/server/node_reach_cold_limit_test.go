package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Bound how many cold reach reports may scan the database at once across
// distinct node/window keys, so a burst of distinct cold keys cannot occupy
// the whole read pool and starve unrelated endpoints. The limit must be taken
// only for genuinely cold work (after the in-flight cache recheck), always
// released, and must leave warm hits and same-key singleflight coalescing
// untouched. Tests by dborup (dborup/CoreScope@195ec4c3), adapted.

// newColdLimitServer builds the shared reach integration fixture with the
// cold limiter configured for the test (limit, queue wait, acquire hook).
func newColdLimitServer(t *testing.T, limit int, wait time.Duration, hook func()) (*Server, *DB, string) {
	t.Helper()
	db, n := newReachIntegrationDB(t, `["AABB","01FA","CCDD"]`)
	t.Cleanup(func() { db.conn.Close() })
	// The fixture is an anonymous :memory: database, where every extra pool
	// connection would see its own empty schema. Pin the pool to one
	// connection so concurrent handler goroutines share the seeded data (the
	// limiter is taken before any query, so slot concurrency is unaffected).
	db.conn.SetMaxOpenConns(1)
	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}
	resetReachState(t, srv)
	srv.reach.cold.configure(limit, wait, hook)
	return srv, db, n
}

// serveReachCtx is serveReach with a caller-supplied request context so a
// test can cancel mid-scan.
func serveReachCtx(srv *Server, ctx context.Context, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/{pubkey}/reach", srv.handleNodeReach).Methods("GET")
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func clearReachCache(t *testing.T, srv *Server) {
	t.Helper()
	srv.reach.cacheMu.Lock()
	srv.reach.cache = map[string]reachCacheEntry{}
	srv.reach.cacheMu.Unlock()
}

// --- limiter unit semantics ---

// Many distinct holders in bursts within the queue capacity: the limiter must
// never let more than `limit` run at once, yet serve every admitted holder.
// The test tracks peak occupancy itself, independently of the limiter counters.
func TestReachColdLimiter_BoundsManyDistinctHolders(t *testing.T) {
	var l reachColdLimiter
	l.configure(2, 5*time.Second, nil)

	var live, peak atomic.Int64
	var wg sync.WaitGroup
	for batch := 0; batch < 4; batch++ {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := l.acquire(context.Background()); err != nil {
					t.Errorf("acquire: %v", err)
					return
				}
				n := live.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				live.Add(-1)
				l.release()
			}()
		}
		wg.Wait()
	}

	if got := peak.Load(); got != 2 {
		t.Fatalf("observed peak concurrency = %d, want exactly 2 (bound honoured and reached)", got)
	}
	if got := l.peakInflight(); got != peak.Load() {
		t.Fatalf("limiter peakInflight = %d, want %d", got, peak.Load())
	}
	if got := l.inflight.Load(); got != 0 {
		t.Fatalf("inflight after all releases = %d, want 0", got)
	}
	if got := l.acquiredCount(); got != 32 {
		t.Fatalf("acquiredCount = %d, want 32", got)
	}
	if got := l.rejectedCount(); got != 0 {
		t.Fatalf("rejectedCount = %d, want 0 with a generous queue wait", got)
	}
}

// The shipped defaults are part of the contract: the cap must leave read-pool
// headroom for other endpoints (pool is 4 connections in OpenDB), and the
// queue wait must be a real, bounded wait rather than an instant refusal.
func TestReachColdLimiter_DefaultsBoundToTwoWithBoundedWait(t *testing.T) {
	if reachColdBuildLimit != 2 {
		t.Fatalf("reachColdBuildLimit = %d, want 2 (half of the 4-connection read pool)", reachColdBuildLimit)
	}
	if reachColdBuildWait != 3*time.Second {
		t.Fatalf("reachColdBuildWait = %v, want the documented 3-second wait", reachColdBuildWait)
	}
	// Zero value must come up on the defaults, with no constructor.
	var l reachColdLimiter
	for i := 0; i < reachColdBuildLimit; i++ {
		if err := l.acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d on a zero-value limiter: %v", i, err)
		}
	}
	l.configure(0, 5*time.Millisecond, nil) // default cap, short wait for the shed check
	for i := 0; i < reachColdBuildLimit; i++ {
		if err := l.acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d after configure: %v", i, err)
		}
	}
	if err := l.acquire(context.Background()); err != errReachColdBusy {
		t.Fatalf("acquire past the default cap = %v, want errReachColdBusy", err)
	}

	// A zero-value limiter must also come up on the default wait: a request
	// that finds it full queues instead of failing at once.
	var z reachColdLimiter
	for i := 0; i < reachColdBuildLimit; i++ {
		if err := z.acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d on a zero-value limiter: %v", i, err)
		}
	}
	queued := make(chan error, 1)
	go func() { queued <- z.acquire(context.Background()) }()
	select {
	case err := <-queued:
		t.Fatalf("acquire on a full zero-value limiter returned %v at once, want it to wait", err)
	case <-time.After(100 * time.Millisecond):
	}
	z.release()
	if err := <-queued; err != nil {
		t.Fatalf("queued acquire after release = %v, want nil", err)
	}

	// Retry-After is documented in docs/api-spec.md.
	if reachColdRetryAfterSeconds != 2 {
		t.Fatalf("reachColdRetryAfterSeconds = %d, want 2 (as documented)", reachColdRetryAfterSeconds)
	}
}

// Cancellation while queued must not consume a slot and must surface the
// context error (not the busy sentinel), so the handler keeps its existing
// behaviour for a disconnecting client.
func TestReachColdLimiter_CancellationDoesNotConsumeSlot(t *testing.T) {
	var l reachColdLimiter
	l.configure(1, 5*time.Second, nil)
	if err := l.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.acquire(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("queued acquire after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire did not return after cancellation")
	}
	if got := l.inflight.Load(); got != 1 {
		t.Fatalf("inflight = %d, want 1 (cancelled waiter must not take a slot)", got)
	}
	if got := l.canceledCount(); got != 1 {
		t.Fatalf("canceledCount = %d, want 1", got)
	}

	// An already-cancelled context fails fast, without taking the free slot.
	l.release()
	dead, stop := context.WithCancel(context.Background())
	stop()
	if err := l.acquire(dead); err != context.Canceled {
		t.Fatalf("acquire with dead context = %v, want context.Canceled", err)
	}
	if got := l.inflight.Load(); got != 0 {
		t.Fatalf("inflight = %d, want 0", got)
	}
}

// Saturation then recovery: a full limiter rejects with the busy sentinel
// after the bounded queue wait, and accepts again as soon as a slot frees.
func TestReachColdLimiter_SaturationRejectsThenRecovers(t *testing.T) {
	var l reachColdLimiter
	l.configure(1, 5*time.Millisecond, nil)
	if err := l.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := l.acquire(context.Background()); err != errReachColdBusy {
		t.Fatalf("saturated acquire = %v, want errReachColdBusy", err)
	}
	if got := l.rejectedCount(); got != 1 {
		t.Fatalf("rejectedCount = %d, want 1", got)
	}
	l.release()
	if err := l.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release = %v, want nil (recovery)", err)
	}
	l.release()
	if got := l.inflight.Load(); got != 0 {
		t.Fatalf("inflight = %d, want 0", got)
	}
}

// --- handler integration ---

// Many distinct cold keys (3 bursts of 8 windows, within the queue capacity)
// must all be served, but never more than `limit` may scan at the same time.
func TestNodeReach_ColdScansBoundedAcrossDistinctKeys(t *testing.T) {
	hold := func() { time.Sleep(10 * time.Millisecond) }
	srv, _, n := newColdLimitServer(t, 2, 10*time.Second, hold)
	keys := []string{n, pk64("aabb"), pk64("ccdd")}

	var wg sync.WaitGroup
	for _, pk := range keys {
		for days := 1; days <= 8; days++ {
			wg.Add(1)
			go func(pk string, days int) {
				defer wg.Done()
				rr := serveReach(srv, "/api/nodes/"+pk+"/reach?days="+strconv.Itoa(days))
				if rr.Code != http.StatusOK {
					t.Errorf("%s days=%d: status=%d want 200 (body=%s)", pk[:8], days, rr.Code, rr.Body.String())
				}
			}(pk, days)
		}
		wg.Wait()
	}

	if got := srv.reach.cold.peakInflight(); got > 2 {
		t.Fatalf("peak concurrent cold scans = %d, want <= 2", got)
	}
	if got := srv.reach.cold.peakInflight(); got < 1 {
		t.Fatalf("peak concurrent cold scans = %d, want >= 1 (handler must take the limit)", got)
	}
	if got := srv.reach.cold.acquiredCount(); got != uint64(len(keys)*8) {
		t.Fatalf("acquiredCount = %d, want %d (one per distinct cold key)", got, len(keys)*8)
	}
	if got := srv.reach.cold.inflight.Load(); got != 0 {
		t.Fatalf("inflight after all requests = %d, want 0", got)
	}
}

// Identical keys stay coalesced by singleflight and take the limit once, so a
// thundering herd on one cold key costs one slot, not N.
func TestNodeReach_IdenticalKeysCoalesceIntoOneSlot(t *testing.T) {
	hold := func() { time.Sleep(25 * time.Millisecond) }
	srv, _, n := newColdLimitServer(t, 1, 10*time.Second, hold)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := serveReach(srv, "/api/nodes/"+n+"/reach?days=30")
			if rr.Code != http.StatusOK {
				t.Errorf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
			}
		}()
	}
	wg.Wait()

	if got := srv.reach.cold.acquiredCount(); got != 1 {
		t.Fatalf("acquiredCount = %d, want 1 (same-key coalescing must hold the limit once)", got)
	}
	if got := srv.reach.cold.rejectedCount(); got != 0 {
		t.Fatalf("rejectedCount = %d, want 0", got)
	}
}

// Warm hits must not touch the limiter at all: with every slot held and a
// long queue wait, a cached key still answers immediately.
func TestNodeReach_WarmHitSkipsColdLimit(t *testing.T) {
	srv, _, n := newColdLimitServer(t, 1, 10*time.Second, nil)
	path := "/api/nodes/" + n + "/reach?days=30"
	if rr := serveReach(srv, path); rr.Code != http.StatusOK {
		t.Fatalf("warm-up: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if got := srv.reach.cold.acquiredCount(); got != 1 {
		t.Fatalf("acquiredCount after cold warm-up = %d, want 1", got)
	}

	// Occupy the only slot; a warm hit must not queue behind it.
	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("test hold: %v", err)
	}
	defer srv.reach.cold.release()

	start := time.Now()
	rr := serveReach(srv, path)
	elapsed := time.Since(start)
	if rr.Code != http.StatusOK {
		t.Fatalf("warm hit under saturation: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("warm hit took %v under saturation, want immediate (no queue wait)", elapsed)
	}
	if got := srv.reach.cold.acquiredCount(); got != 2 {
		t.Fatalf("acquiredCount = %d, want 2 (one cold warm-up + the test's own hold only)", got)
	}
}

// A failing cold scan must release the slot: the 500 path is the one that
// leaks a semaphore if release is not deferred.
func TestNodeReach_ComputeErrorReleasesColdSlot(t *testing.T) {
	srv, db, n := newColdLimitServer(t, 1, 5*time.Millisecond, nil)
	path := "/api/nodes/" + n + "/reach?days=30"
	if rr := serveReach(srv, path); rr.Code != http.StatusOK {
		t.Fatalf("warm-up: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	clearReachCache(t, srv)
	if _, err := db.conn.Exec("DROP TABLE observations"); err != nil {
		t.Fatalf("drop observations: %v", err)
	}

	rr := serveReach(srv, path)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500 on scan failure (body=%s)", rr.Code, rr.Body.String())
	}
	if got := srv.reach.cold.inflight.Load(); got != 0 {
		t.Fatalf("inflight after failed scan = %d, want 0 (slot must be released on error)", got)
	}
	// The limiter must still hand out its only slot.
	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after failed scan = %v, want nil (slot leaked)", err)
	}
	srv.reach.cold.release()
}

// A cancelled request must release the slot too (handler path, not just the
// limiter unit).
func TestNodeReach_CancelledColdRequestReleasesSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	hold := func() { cancel(); time.Sleep(5 * time.Millisecond) }
	srv, _, n := newColdLimitServer(t, 1, 5*time.Millisecond, hold)

	rr := serveReachCtx(srv, ctx, "/api/nodes/"+n+"/reach?days=30")
	if rr.Code != http.StatusOK && rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 200 or 500 for a cancelled scan (body=%s)", rr.Code, rr.Body.String())
	}
	if got := srv.reach.cold.inflight.Load(); got != 0 {
		t.Fatalf("inflight after cancelled request = %d, want 0", got)
	}
	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after cancelled request = %v, want nil (slot leaked)", err)
	}
	srv.reach.cold.release()
}

// Saturation and recovery through the HTTP surface: a cold request that
// cannot get a slot answers 429 + Retry-After, caches nothing, and the very
// same request returns the identical report once a slot frees.
func TestNodeReach_SaturatedColdRequestReturns429AndRecovers(t *testing.T) {
	srv, _, n := newColdLimitServer(t, 1, 5*time.Millisecond, nil)
	path := "/api/nodes/" + n + "/reach?days=30"

	base := serveReach(srv, path)
	if base.Code != http.StatusOK {
		t.Fatalf("baseline: status=%d want 200 (body=%s)", base.Code, base.Body.String())
	}
	want := normaliseReachBody(t, base.Body.Bytes())
	clearReachCache(t, srv)

	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("test hold: %v", err)
	}
	rr := serveReach(srv, path)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("saturated cold request: status=%d want 429 (body=%s)", rr.Code, rr.Body.String())
	}
	if ra := rr.Header().Get("Retry-After"); ra != strconv.Itoa(reachColdRetryAfterSeconds) {
		t.Fatalf("Retry-After = %q, want %q", ra, strconv.Itoa(reachColdRetryAfterSeconds))
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Error      string `json:"error"`
		RetryAfter int    `json:"retryAfter"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON: %v (%s)", err, rr.Body.String())
	}
	if body.Error == "" || body.RetryAfter != reachColdRetryAfterSeconds {
		t.Fatalf("429 body = %+v, want non-empty error and retryAfter=%d", body, reachColdRetryAfterSeconds)
	}
	if got := srv.reachCacheLen(); got != 0 {
		t.Fatalf("reach cache len = %d after a shed request, want 0 (nothing cached)", got)
	}

	srv.reach.cold.release()
	rec := serveReach(srv, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("after recovery: status=%d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := normaliseReachBody(t, rec.Body.Bytes()); got != want {
		t.Fatalf("report changed after shed+recovery:\n got %s\nwant %s", got, want)
	}
	if got := srv.reachCacheLen(); got != 1 {
		t.Fatalf("reach cache len = %d after recovery, want 1", got)
	}
}

// normaliseReachBody re-marshals a reach report with the wall-clock-dependent
// window start removed and links/observers put in pubkey order, so two
// reports computed seconds apart compare equal. The ordering step is needed
// because links that tie on every sort key (bidir, bottleneck, we+they) come
// out in map-iteration order — a pre-existing tie-break gap, not something
// this change introduces.
func normaliseReachBody(t *testing.T, raw []byte) string {
	t.Helper()
	var resp NodeReachResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal reach body: %v (%s)", err, raw)
	}
	resp.Window.Since = ""
	sort.Slice(resp.Links, func(i, j int) bool { return resp.Links[i].Pubkey < resp.Links[j].Pubkey })
	sort.Slice(resp.DirectObservers, func(i, j int) bool {
		return resp.DirectObservers[i].Pubkey < resp.DirectObservers[j].Pubkey
	})
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("remarshal reach body: %v", err)
	}
	return string(out)
}

// The shared computation runs on the context of the request that started it.
// When that request goes away while queued for a slot, a second request for
// the same key that joined its flight is still connected and did nothing
// wrong: it must get 429 + Retry-After (retry recomputes), not 500.
func TestNodeReach_CoalescedWaiterGets429WhenStarterCancels(t *testing.T) {
	srv, _, n := newColdLimitServer(t, 1, 10*time.Second, nil)
	path := "/api/nodes/" + n + "/reach?days=30"

	// Hold the only slot so the starter queues inside the singleflight.
	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("test hold: %v", err)
	}
	defer srv.reach.cold.release()

	startCtx, cancelStarter := context.WithCancel(context.Background())
	starter := make(chan *httptest.ResponseRecorder, 1)
	go func() { starter <- serveReachCtx(srv, startCtx, path) }()
	time.Sleep(100 * time.Millisecond) // starter is queued in acquire

	joiner := make(chan *httptest.ResponseRecorder, 1)
	go func() { joiner <- serveReach(srv, path) }()
	time.Sleep(100 * time.Millisecond) // joiner waits on the starter's flight

	cancelStarter()
	select {
	case rr := <-joiner:
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("coalesced waiter after starter cancel: status=%d want 429 (body=%s)", rr.Code, rr.Body.String())
		}
		if ra := rr.Header().Get("Retry-After"); ra != strconv.Itoa(reachColdRetryAfterSeconds) {
			t.Fatalf("Retry-After = %q, want %q", ra, strconv.Itoa(reachColdRetryAfterSeconds))
		}
	case <-time.After(5 * time.Second):
		// A joiner that started its own flight would sit in the 10 s queue.
		t.Fatal("coalesced waiter did not return after the starter was cancelled")
	}
	<-starter
	if got := srv.reach.cold.canceledCount(); got != 1 {
		t.Fatalf("canceledCount = %d, want 1 (only the starter queued)", got)
	}
	if got := srv.reachCacheLen(); got != 0 {
		t.Fatalf("reach cache len = %d, want 0 (nothing cached)", got)
	}
}

// The behaviour change itself: a cold request that finds every slot taken is
// not refused at once. It waits, and is served as soon as a slot frees within
// the queue wait (the two-tab burst that an instant 429 used to fail).
func TestNodeReach_QueuedColdRequestServedWhenSlotFrees(t *testing.T) {
	srv, _, n := newColdLimitServer(t, 1, 10*time.Second, nil)
	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("test hold: %v", err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveReach(srv, "/api/nodes/"+n+"/reach?days=30") }()

	select {
	case rr := <-done:
		t.Fatalf("request returned %d while the only slot was held; want it to queue", rr.Code)
	case <-time.After(100 * time.Millisecond):
	}
	srv.reach.cold.release()
	select {
	case rr := <-done:
		if rr.Code != http.StatusOK {
			t.Fatalf("queued request after slot freed: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued request was not served after the slot freed")
	}
	if got := srv.reach.cold.rejectedCount(); got != 0 {
		t.Fatalf("rejectedCount = %d, want 0", got)
	}
}

// A request whose cache check missed, but whose key was filled before it
// entered the singleflight (another flight for the key just finished), is
// warm by then: the recheck inside the flight must answer it without taking a
// build slot, even when every slot is held.
func TestNodeReach_CacheFilledBeforeFlightSkipsColdLimit(t *testing.T) {
	srv, _, n := newColdLimitServer(t, 1, 5*time.Millisecond, nil)
	path := "/api/nodes/" + n + "/reach?days=30"
	if rr := serveReach(srv, path); rr.Code != http.StatusOK {
		t.Fatalf("warm-up: status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	saved := map[string]reachCacheEntry{}
	srv.reach.cacheMu.Lock()
	for k, e := range srv.reach.cache {
		saved[k] = e
	}
	srv.reach.cacheMu.Unlock()
	if len(saved) != 1 {
		t.Fatalf("cache entries after warm-up = %d, want 1", len(saved))
	}
	clearReachCache(t, srv)

	if err := srv.reach.cold.acquire(context.Background()); err != nil {
		t.Fatalf("test hold: %v", err)
	}
	defer srv.reach.cold.release()
	srv.reach.beforeFlight = func() {
		srv.reach.cacheMu.Lock()
		for k, e := range saved {
			srv.reach.cache[k] = e
		}
		srv.reach.cacheMu.Unlock()
	}
	rr := serveReach(srv, path)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 from the in-flight recheck (body=%s)", rr.Code, rr.Body.String())
	}
	if got := srv.reach.cold.acquiredCount(); got != 2 {
		t.Fatalf("acquiredCount = %d, want 2 (warm-up + the test's hold only)", got)
	}
}

// Same as above, but the starter goes away mid-scan (after it took the slot):
// the scan fails with the context error, and the coalesced waiter that is
// still connected gets 429, not 500.
func TestNodeReach_CoalescedWaiterGets429WhenStarterCancelsMidScan(t *testing.T) {
	startCtx, cancelStarter := context.WithCancel(context.Background())
	inScan := make(chan struct{})
	proceed := make(chan struct{})
	hold := func() { close(inScan); <-proceed }
	srv, _, n := newColdLimitServer(t, 1, 10*time.Second, hold)
	path := "/api/nodes/" + n + "/reach?days=30"

	starter := make(chan *httptest.ResponseRecorder, 1)
	go func() { starter <- serveReachCtx(srv, startCtx, path) }()
	select { // the starter holds the slot and is about to scan
	case <-inScan:
	case <-time.After(5 * time.Second):
		t.Fatal("starter never took the slot")
	}

	joiner := make(chan *httptest.ResponseRecorder, 1)
	go func() { joiner <- serveReach(srv, path) }()
	time.Sleep(100 * time.Millisecond) // joiner waits on the starter's flight

	cancelStarter()
	close(proceed)
	select {
	case rr := <-joiner:
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("coalesced waiter after mid-scan cancel: status=%d want 429 (body=%s)", rr.Code, rr.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced waiter did not return after the starter was cancelled")
	}
	<-starter
	if got := srv.reach.cold.acquiredCount(); got != 1 {
		t.Fatalf("acquiredCount = %d, want 1 (the joiner shared the starter's flight)", got)
	}
	if got := srv.reach.cold.inflight.Load(); got != 0 {
		t.Fatalf("inflight = %d, want 0", got)
	}
}
