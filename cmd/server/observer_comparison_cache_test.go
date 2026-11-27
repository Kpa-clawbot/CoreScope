package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mattn/go-sqlite3"
)

// The scalar subquery in the view runs once per real aggregate. A test can
// hold that scan open while other requests share the production-sized pool.
func comparisonCacheFixture(t *testing.T, scan func() int) *Server {
	t.Helper()
	conn, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "comparison.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	for range 4 {
		c, err := conn.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := c.Raw(func(driver any) error {
			return driver.(*sqlite3.SQLiteConn).RegisterFunc("comparison_scan", scan, false)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{db: &DB{conn: conn, isV3: true}, cfg: &Config{}}
}

func seedComparisonCache(t *testing.T, s *Server) {
	t.Helper()
	mustExec(t, s.db, `CREATE TABLE observers(idx INTEGER PRIMARY KEY, id TEXT UNIQUE, name TEXT, iata TEXT, inactive INTEGER);
		CREATE TABLE receptions(transmission_id INTEGER, observer_idx INTEGER, timestamp INTEGER, direction TEXT, snr REAL);
		CREATE VIEW observations AS SELECT * FROM receptions WHERE (SELECT comparison_scan())=1;
		INSERT INTO observers VALUES (1,'pin','Reference','PIN',0),(2,'east','East','  aaa ',0),
			(3,'west','West','BBB',0),(4,'unknown','Unknown',NULL,0);
		INSERT INTO receptions VALUES (1,1,?,'rx',3),(1,2,?,'rx',7)`, time.Now().Unix(), time.Now().Unix())
}

func getComparison(ctx context.Context, s *Server, ref, region string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/analytics/observer-comparison?ref="+url.QueryEscape(ref)+"&region="+url.QueryEscape(region), nil)
	s.handleObserverComparison(w, r.WithContext(ctx))
	return w
}

func checkCachedComparison(t *testing.T, w *httptest.ResponseRecorder, total int, peers ...string) {
	t.Helper()
	var got observerComparisonResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("comparison response: %d %s", w.Code, w.Body)
	}
	ids := make([]string, 0, len(got.Peers))
	for _, peer := range got.Peers {
		ids = append(ids, peer.ID)
	}
	if got.ReferenceTotal != total || got.Peers == nil || !slices.Equal(ids, peers) {
		t.Errorf("want reference total %d, peers %v; got %s", total, peers, w.Body)
	}
}

func TestObserverComparisonCacheRegionExpiryAndCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var scans atomic.Int32
		s := comparisonCacheFixture(t, func() int { scans.Add(1); return 1 })
		seedComparisonCache(t, s)
		get := func(ref, region string) *httptest.ResponseRecorder {
			return getComparison(context.Background(), s, ref, region)
		}
		checkCachedComparison(t, get("pin", " aaA "), 1, "east")
		mustExec(t, s.db, "INSERT INTO receptions VALUES (2,1,?,'rx',2)", time.Now().Unix())
		checkCachedComparison(t, get("pin", "All"), 1, "east", "unknown", "west")
		checkCachedComparison(t, get("pin", "bbb, aaA"), 1, "east", "west")
		checkCachedComparison(t, get("pin", "missing"), 1)
		checkCachedComparison(t, get("pin", ""), 1, "east", "unknown", "west")
		if n := scans.Load(); n != 1 {
			t.Errorf("region toggles executed %d aggregates, want 1", n)
		}
		s.cfg = &Config{ObserverBlacklist: []string{"EAST"}}
		checkCachedComparison(t, get("pin", ""), 1, "unknown", "west")
		s.cfg = &Config{ObserverBlacklist: []string{"PIN"}}
		if w := get("pin", ""); w.Code != http.StatusNotFound {
			t.Fatalf("blacklisted cached reference: %d %s", w.Code, w.Body)
		}
		s.cfg = &Config{}
		mustExec(t, s.db, "UPDATE observers SET inactive=1 WHERE id='pin'")
		if w := get("pin", ""); w.Code != http.StatusNotFound {
			t.Fatalf("inactive cached reference: %d %s", w.Code, w.Body)
		}
		mustExec(t, s.db, "UPDATE observers SET inactive=0 WHERE id='pin'")
		time.Sleep(time.Minute)
		mustExec(t, s.db, "DROP VIEW observations")
		if w := get("pin", "aaa"); w.Code != http.StatusInternalServerError {
			t.Fatalf("expired aggregate failure: %d %s", w.Code, w.Body)
		}
		mustExec(t, s.db, "CREATE VIEW observations AS SELECT * FROM receptions WHERE (SELECT comparison_scan())=1")
		checkCachedComparison(t, get("pin", "aaa"), 2, "east")
		for i := range 32 {
			ref := fmt.Sprintf("receiver%02d", i)
			mustExec(t, s.db, "INSERT INTO observers(id) VALUES (?)", ref)
			if w := get(ref, ""); w.Code != http.StatusOK {
				t.Fatalf("fill %s: %d %s", ref, w.Code, w.Body)
			}
			time.Sleep(time.Millisecond)
		}
		before := scans.Load()
		get("receiver31", "")
		if scans.Load() != before {
			t.Error("inserting references evicted the newest result")
		}
		before = scans.Load()
		get("pin", "")
		if scans.Load() != before+1 {
			t.Error("old reference survived 32 newer fills")
		}
	})
}

func TestObserverComparisonCacheCoalescesOverlappingRegions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		entered := make(chan struct{})
		var scans atomic.Int32
		s := comparisonCacheFixture(t, func() int {
			if scans.Add(1) == 1 {
				close(entered)
			}
			<-release
			return 1
		})
		seedComparisonCache(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		leader := make(chan struct{})
		go func() { getComparison(ctx, s, "pin", "AAA"); close(leader) }()
		<-entered
		regions := []string{"BBB", "AAA", "", "BBB", "AAA", "", "BBB"}
		responses := make([]*httptest.ResponseRecorder, len(regions))
		var followers sync.WaitGroup
		for i, region := range regions {
			followers.Go(func() { responses[i] = getComparison(context.Background(), s, "pin", region) })
		}
		// Every follower is now waiting while the initiating scan is blocked.
		synctest.Wait()
		cancel()
		close(release)
		followers.Wait()
		<-leader
		for i, region := range regions {
			switch region {
			case "AAA":
				checkCachedComparison(t, responses[i], 1, "east")
			case "BBB":
				checkCachedComparison(t, responses[i], 1, "west")
			default:
				checkCachedComparison(t, responses[i], 1, "east", "unknown", "west")
			}
		}
		if n := scans.Load(); n != 1 {
			t.Errorf("8 overlapping regional requests executed %d aggregates, want 1", n)
		}
	})
}

func TestObserverComparisonCacheReservesPoolForOtherReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var scans atomic.Int32
		s := comparisonCacheFixture(t, func() int { scans.Add(1); <-release; return 1 })
		seedComparisonCache(t, s)
		var group sync.WaitGroup
		responses := make([]*httptest.ResponseRecorder, 8)
		for i := range responses {
			ref := fmt.Sprintf("receiver%d", i)
			mustExec(t, s.db, "INSERT INTO observers(id) VALUES (?)", ref)
		}
		for i := range responses {
			group.Go(func() { responses[i] = getComparison(context.Background(), s, fmt.Sprintf("receiver%d", i), "") })
		}
		synctest.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var n int
		if inUse := s.db.conn.Stats().InUse; inUse >= 4 {
			t.Errorf("all %d readers held by comparison scans; unrelated read has no connection", inUse)
		} else if err := s.db.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM observers").Scan(&n); err != nil || n != 12 {
			t.Errorf("unrelated read during 8 distinct cold requests: count=%d, err=%v", n, err)
		}
		close(release)
		group.Wait()
		successes := 0
		for _, w := range responses {
			switch w.Code {
			case http.StatusOK:
				successes++
			case http.StatusTooManyRequests:
				if w.Header().Get("Retry-After") == "" {
					t.Error("busy comparison omitted Retry-After")
				}
			default:
				t.Errorf("cold comparison: %d %s", w.Code, w.Body)
			}
		}
		if successes == 0 || successes >= 4 || int(scans.Load()) != successes {
			t.Errorf("distinct references completed %d scans, %d successful responses; want room on the four-connection pool", scans.Load(), successes)
		}
	})
}
