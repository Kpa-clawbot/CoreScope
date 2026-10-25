package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Keep the real reach methods and database/sql iteration. Only the driver's
// query results are controlled, so errors can arrive after a valid first row.
type reachFailureDB struct {
	degreeErr                          error
	degreeBadScan                      bool
	scopeErr                           error
	scopeMissing, scopeNull            bool
	cancel                             context.CancelFunc
	scans, degreeQueries, scopeQueries int
}

func (c *reachFailureDB) Connect(context.Context) (driver.Conn, error) {
	return &reachFailureConn{c}, nil
}
func (*reachFailureDB) Driver() driver.Driver { return reachFailureDriver{} }

type reachFailureDriver struct{}

func (reachFailureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the test connector")
}

type reachFailureConn struct{ fixture *reachFailureDB }

func (*reachFailureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*reachFailureConn) Close() error { return nil }
func (*reachFailureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *reachFailureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	f := c.fixture
	switch {
	case strings.Contains(query, "FROM neighbor_edges"):
		f.degreeQueries++
		r := &reachFailureRows{columns: []string{"pk", "neigh"}, values: [][]driver.Value{
			{pk64("01fa"), int64(4)}, {pk64("aabb"), int64(2)},
		}}
		if f.degreeErr != nil {
			r.values = r.values[:1]
			r.err, r.done = f.degreeErr, f.cancel
		}
		if f.degreeBadScan {
			r.values[1][1] = "not an integer"
		}
		return r, nil
	case strings.Contains(query, "FROM nodes WHERE public_key"):
		f.scopeQueries++
		if f.scopeErr != nil {
			if f.cancel != nil {
				f.cancel()
			}
			return nil, f.scopeErr
		}
		r := &reachFailureRows{}
		var values []driver.Value
		for _, col := range []struct{ name, value string }{
			{"default_scope", "test-inferred"},
			{"configured_scope", "test-configured"},
			{"configured_scope_at", "2026-01-01T00:00:00Z"},
		} {
			if strings.Contains(query, col.name) {
				r.columns = append(r.columns, col.name)
				if f.scopeNull {
					values = append(values, nil)
				} else {
					values = append(values, col.value)
				}
			}
		}
		if !f.scopeMissing {
			r.values = [][]driver.Value{values}
		}
		return r, nil
	case strings.Contains(query, "FROM observations o"):
		return &reachFailureRows{
			columns: []string{"observer", "originator", "type", "path", "snr"},
			values:  [][]driver.Value{{"", "", int64(5), `["01FA"]`, float64(-7)}},
			done:    func() { f.scans++ },
		}, nil
	case strings.HasPrefix(query, "SELECT since FROM "):
		return &reachFailureRows{columns: []string{"since"}}, nil // no hop-index coverage
	case query == "SELECT id, name FROM observers":
		return &reachFailureRows{columns: []string{"id", "name"}}, nil
	default:
		return nil, fmt.Errorf("unexpected test query: %s", query)
	}
}

type reachFailureRows struct {
	columns []string
	values  [][]driver.Value
	pos     int
	err     error
	done    func()
}

func (r *reachFailureRows) Columns() []string { return r.columns }
func (*reachFailureRows) Close() error        { return nil }
func (r *reachFailureRows) Next(dest []driver.Value) error {
	if r.pos == len(r.values) {
		if r.done != nil {
			r.done()
		}
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	copy(dest, r.values[r.pos])
	r.pos++
	return nil
}

func newReachFailureServer(t *testing.T) (*Server, *reachFailureDB, string) {
	t.Helper()
	db, pk := newReachIntegrationDB(t, `["01FA"]`)
	t.Cleanup(func() { db.conn.Close() })
	cfg := &Config{}
	f := &reachFailureDB{}
	conn := sql.OpenDB(f)
	t.Cleanup(func() { conn.Close() })
	return &Server{
		store: newTestStoreWithDB(t, db, cfg), cfg: cfg, perfStats: NewPerfStats(),
		db: &DB{conn: conn, isV3: true, hasDefaultScope: true, hasConfiguredScope: true},
	}, f, pk
}

func completeReachDegreeSnapshot() *degreeSnapshot {
	return &degreeSnapshot{at: time.Now().Add(-2 * reachDegreeTTL), total: 2,
		deg: map[string]int{pk64("01fa"): 10, pk64("aabb"): 8}, sortedDesc: []int{10, 8}}
}

func TestReachDegreeRefreshFailurePreservesSnapshot(t *testing.T) {
	for _, failure := range []string{"cancelled iteration", "failed iteration", "failed scan"} {
		for _, warm := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale=%t", failure, warm), func(t *testing.T) {
				s, f, _ := newReachFailureServer(t)
				var stale *degreeSnapshot
				if warm {
					stale = completeReachDegreeSnapshot()
					s.reach.degreeSnap = stale
				}
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				switch failure {
				case "cancelled iteration":
					f.degreeErr, f.cancel = context.Canceled, cancel
				case "failed iteration":
					f.degreeErr = errors.New("synthetic iteration failure")
				case "failed scan":
					f.degreeBadScan = true
				}
				if got := s.getDegreeSnapshot(ctx); got != stale || s.reach.degreeSnap != stale {
					t.Errorf("failed refresh published a partial snapshot: got=%+v, want original=%+v", got, stale)
				}
				if failure == "cancelled iteration" && !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatal("fixture did not cancel during iteration")
				}
				f.degreeErr, f.cancel, f.degreeBadScan = nil, nil, false
				got := s.getDegreeSnapshot(context.Background())
				if got == stale || got.total != 2 || got.deg[pk64("01fa")] != 4 || got.deg[pk64("aabb")] != 2 {
					t.Fatalf("successful retry did not publish the complete snapshot: %+v", got)
				}
			})
		}
	}
}

func TestNodeReachFollowupFailureDoesNotCache(t *testing.T) {
	for _, failure := range []string{"scope cancellation", "scope error", "degree cancellation without scope columns"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired-cache=%t", failure, cached), func(t *testing.T) {
				s, f, pk := newReachFailureServer(t)
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				stale := completeReachDegreeSnapshot()
				s.reach.degreeSnap = stale
				switch failure {
				case "scope cancellation":
					stale.at = time.Now() // a complete, fresh degree cache already exists
					f.scopeErr, f.cancel = context.Canceled, cancel
				case "scope error":
					stale.at = time.Now()
					f.scopeErr = errors.New("synthetic scope failure")
				default:
					s.db.hasDefaultScope, s.db.hasConfiguredScope = false, false
					f.degreeErr, f.cancel = context.Canceled, cancel
				}
				key := pk + "|7|g0"
				old := reachCacheEntry{at: time.Now().Add(-2 * reachCacheTTL), raw: []byte("previous complete response")}
				if cached {
					s.reach.cache = map[string]reachCacheEntry{key: old}
				}
				r := httptest.NewRequest(http.MethodGet, "/api/nodes/"+pk+"/reach", nil).WithContext(ctx)
				r = mux.SetURLVars(r, map[string]string{"pubkey": pk})
				w := httptest.NewRecorder()
				s.handleNodeReach(w, r)
				if f.scans != 1 {
					t.Fatalf("failure must follow a completed main scan, got %d scans", f.scans)
				}
				if w.Code != http.StatusInternalServerError {
					t.Errorf("failed follow-up returned %d, want 500: %s", w.Code, w.Body.String())
				}
				entry, exists := s.reach.cache[key]
				if exists != cached || (cached && (!entry.at.Equal(old.at) || string(entry.raw) != string(old.raw))) {
					t.Error("failed build inserted or replaced the reach response cache")
				}
				if s.reach.degreeSnap != stale {
					t.Error("failed build replaced the complete degree snapshot")
				}
				f.scopeErr, f.degreeErr, f.cancel = nil, nil, nil
				s.db.hasDefaultScope, s.db.hasConfiguredScope = true, true
				w = serveReach(s, "/api/nodes/"+pk+"/reach")
				var response NodeReachResponse
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
					t.Fatalf("retry failed: %d %s", w.Code, w.Body.String())
				}
				if f.scans != 2 || response.Node.ConfiguredScope != "test-configured" || response.Importance.RelayObservations != 1 {
					t.Errorf("retry reused a failed answer: scans=%d response=%+v", f.scans, response)
				}
				if _, ok := s.reachCacheGet(key); !ok {
					t.Error("successful retry was not cached")
				}
			})
		}
	}
}

func TestNodeReachOptionalScopes(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		defaults, configured, missing, nulls bool
		wantDefault, wantConfigured          string
	}{
		{"both", true, true, false, false, "test-inferred", "test-configured"},
		{"default only", true, false, false, false, "test-inferred", ""},
		{"configured only", false, true, false, false, "", "test-configured"},
		{"old schema", false, false, false, false, "", ""},
		{"missing row", true, true, true, false, "", ""},
		{"null scopes", true, true, false, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, pk := newReachFailureServer(t)
			s.db.hasDefaultScope, s.db.hasConfiguredScope = tc.defaults, tc.configured
			f.scopeMissing, f.scopeNull = tc.missing, tc.nulls
			w := serveReach(s, "/api/nodes/"+pk+"/reach")
			var response NodeReachResponse
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatalf("valid optional scopes returned %d: %s", w.Code, w.Body.String())
			}
			if response.Node.DefaultScope != tc.wantDefault || response.Node.ConfiguredScope != tc.wantConfigured {
				t.Errorf("scopes = %+v", response.Node)
			}
			if !tc.defaults && !tc.configured && f.scopeQueries != 0 {
				t.Error("old schema must not query absent scope columns")
			}
		})
	}
}
