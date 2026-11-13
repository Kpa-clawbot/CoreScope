package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

// A transmission or observation the ingestor commits after the startup load's
// last chunk has read its snapshot, but before the poller initialises its
// cursor, must still reach the in-memory store. The poller seeds its cursor
// from a fresh MAX(id) taken when it starts (websocket.go Poller.Start), so a
// row committed in that window is above everything loaded yet not above the
// cursor: neither the load nor the poll ever reads it.
//
// The window is opened deterministically with OnChunkLoaded, which fires
// after a chunk's rows have been read and closed. With one chunk that is the
// last snapshot the load takes.

func TestStartupLoadPollerGap_SQLite(t *testing.T) {
	db, rw := openGapSQLite(t)
	runStartupLoadPollerGap(t, db, rw)
}

func TestStartupLoadPollerGap_Postgres(t *testing.T) {
	db, rw := newPostgresTestDB(t) // skips without $CORESCOPE_TEST_PG_DSN
	runStartupLoadPollerGap(t, db, rw)
}

func runStartupLoadPollerGap(t *testing.T, db *DB, rw *sql.DB) {
	t.Helper()
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	iso := func(d time.Duration) string { return base.Add(d).Format("2006-01-02T15:04:05.000Z") }
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := rw.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	addTx := func(id int) {
		mustExec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
			VALUES (?, ?, ?, ?, 1, 5, 1, '{"type":"GRP_TXT"}', ?)`,
			id, fmt.Sprintf("11%02dAABBCCDD", id), gapHash(id), iso(time.Duration(id)*time.Second), base.Add(time.Duration(id)*time.Second).Unix())
	}
	addObs := func(id, tx, observer int) {
		mustExec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
			VALUES (?, ?, ?, 'rx', 5.5, -90, 1, ?, ?)`,
			id, tx, observer, fmt.Sprintf(`["%02X"]`, id), base.Add(time.Duration(id)*time.Second).Unix())
	}
	for idx := 1; idx <= 3; idx++ {
		mustExec(`INSERT INTO observers (idx, id, name, iata, last_seen, first_seen) VALUES (?, ?, ?, 'SJC', ?, ?)`,
			idx, fmt.Sprintf("%064X", idx), fmt.Sprintf("Obs-%d", idx), iso(0), iso(-time.Hour))
	}
	// Committed before the load.
	addTx(1)
	addObs(1, 1, 1)
	addTx(2)
	addObs(2, 2, 1)

	store := NewPacketStore(db, &PacketStoreConfig{})
	fired := false
	store.OnChunkLoaded(func(int, int) {
		if fired {
			return
		}
		fired = true
		// Committed after the (only) chunk read its snapshot, before the
		// poller starts: a new transmission with one observation, and a
		// second observation of an already loaded transmission.
		addTx(3)
		addObs(3, 3, 1)
		addObs(4, 1, 2)
	})
	if err := store.RunStartupLoad(100); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if !fired {
		t.Fatal("OnChunkLoaded never fired")
	}

	loaded := loadedObsPaths(store)
	pr := startGapPoller(t, db, store)

	time.Sleep(80 * time.Millisecond)
	// Committed after the poller started: a later observation of tx 3 (the
	// 12 observations of the lost transmission in the field), and a control
	// transmission proving the poller itself works.
	addObs(5, 3, 3)
	addTx(4)
	addObs(6, 4, 1)

	has := func() (tx3, tx4 bool, obs3, obs4, obs5 bool) {
		store.mu.RLock()
		defer store.mu.RUnlock()
		_, tx3 = store.byHash[gapHash(3)]
		_, tx4 = store.byHash[gapHash(4)]
		_, obs3 = store.byObsID[3]
		_, obs4 = store.byObsID[4]
		_, obs5 = store.byObsID[5]
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, tx4, _, _, _ := has(); tx4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // a few more ticks for stragglers
	tx3, tx4, obs3, obs4, obs5 := has()
	if !tx4 {
		t.Fatal("control: tx 4, committed after the poller started, never reached the store; the poller is not running")
	}
	if !tx3 {
		t.Errorf("tx 3, committed between the load's snapshot and the poller's cursor init, is missing from the store (lost forever)")
	}
	if !obs3 || !obs5 {
		t.Errorf("observations of tx 3 missing: obs3=%v obs5=%v", obs3, obs5)
	}
	if !obs4 {
		t.Errorf("obs 4 (new observation of already-loaded tx 1, committed in the same window) is missing from the store")
	}
	pr.stop()
	assertStoreMatchesDB(t, store, rw)
	assertNoDuplicateBroadcasts(t, pr.drain(), loaded)
	assertReplayIdempotent(t, store, rw)
}

func gapHash(id int) string { return fmt.Sprintf("feed%012x", id) }

// A chunk-1 transmission can gain an observation after chunk 1 has read its
// snapshot whose id is BELOW an observation a later chunk loads (that one
// belongs to a chunk-2 transmission and was committed just after it). Using
// the store's highest loaded observation id as the poller's floor would then
// skip it. Three chunks of two: {1,2} {3,4} {5}; the last is short, so its
// callback runs after the load's final snapshot.

func TestStartupLoadPollerGapMultiChunk_SQLite(t *testing.T) {
	db, rw := openGapSQLite(t)
	runStartupLoadPollerGapMultiChunk(t, db, rw)
}

func TestStartupLoadPollerGapMultiChunk_Postgres(t *testing.T) {
	db, rw := newPostgresTestDB(t) // skips without $CORESCOPE_TEST_PG_DSN
	runStartupLoadPollerGapMultiChunk(t, db, rw)
}

func openGapSQLite(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gap.db")
	rw, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rw.Close() })
	rw.SetMaxOpenConns(1)
	if _, err := rw.Exec(dbschema.SQLiteEndState); err != nil {
		t.Fatalf("apply SQLite end-state schema: %v", err)
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.conn.Close() })
	return db, rw
}

func runStartupLoadPollerGapMultiChunk(t *testing.T, db *DB, rw *sql.DB) {
	t.Helper()
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	iso := func(d time.Duration) string { return base.Add(d).Format("2006-01-02T15:04:05.000Z") }
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := rw.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	addTx := func(id int) {
		mustExec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
			VALUES (?, ?, ?, ?, 1, 5, 1, '{"type":"GRP_TXT"}', ?)`,
			id, fmt.Sprintf("11%02dAABBCCDD", id), gapHash(id), iso(time.Duration(id)*time.Second), base.Add(time.Duration(id)*time.Second).Unix())
	}
	// path_json is unique per observation, so none merges with another
	// (dedup is by observer+path) and DB and store counts must agree.
	addObs := func(id, tx, observer int) {
		mustExec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
			VALUES (?, ?, ?, 'rx', 5.5, -90, 1, ?, ?)`,
			id, tx, observer, fmt.Sprintf(`["%02X"]`, id), base.Add(time.Duration(id)*time.Second).Unix())
	}
	for idx := 1; idx <= 3; idx++ {
		mustExec(`INSERT INTO observers (idx, id, name, iata, last_seen, first_seen) VALUES (?, ?, ?, 'SJC', ?, ?)`,
			idx, fmt.Sprintf("%064X", idx), fmt.Sprintf("Obs-%d", idx), iso(0), iso(-time.Hour))
	}
	for id := 1; id <= 5; id++ {
		addTx(id)
		addObs(id, id, 1)
	}

	store := NewPacketStore(db, &PacketStoreConfig{})
	chunk := 0
	store.OnChunkLoaded(func(int, int) {
		chunk++
		switch chunk {
		case 1:
			// Between chunks 1 and 2: obs 10 of chunk-1 tx 1, then obs 11
			// of tx 3, which chunk 2 loads. The store's max obs id becomes
			// 11 although 10 was never loaded.
			addObs(10, 1, 2)
			addObs(11, 3, 2)
		case 3:
			// After the final snapshot: a new transmission with one
			// observation, and a new observation of loaded tx 2.
			addTx(6)
			addObs(12, 6, 1)
			addObs(13, 2, 3)
		}
	})
	if err := store.RunStartupLoad(2); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if chunk != 3 {
		t.Fatalf("expected 3 chunks, got %d", chunk)
	}
	loaded := loadedObsPaths(store)
	if _, ok := loaded[11]; !ok {
		t.Fatal("setup: obs 11 should have been loaded by chunk 2")
	}
	if _, ok := loaded[10]; ok {
		t.Fatal("setup: obs 10 should not have been loaded (committed after chunk 1)")
	}

	pr := startGapPoller(t, db, store)
	time.Sleep(80 * time.Millisecond)
	// After the poller started: a later observation of tx 6 and a control.
	addObs(14, 6, 2)
	addTx(7)
	addObs(15, 7, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.RLock()
		_, ok := store.byObsID[15]
		store.mu.RUnlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // a few more ticks for stragglers
	pr.stop()

	store.mu.RLock()
	_, tx6 := store.byHash[gapHash(6)]
	_, tx7 := store.byHash[gapHash(7)]
	var missing []int
	for _, id := range []int{10, 12, 13, 14, 15} {
		if _, ok := store.byObsID[id]; !ok {
			missing = append(missing, id)
		}
	}
	store.mu.RUnlock()
	if !tx7 {
		t.Fatal("control: tx 7, committed after the poller started, never reached the store; the poller is not running")
	}
	if !tx6 {
		t.Errorf("tx 6, committed after the load's final snapshot, is missing from the store")
	}
	if len(missing) > 0 {
		t.Errorf("observations missing from the store: %v (10 is below the max loaded obs id 11)", missing)
	}
	assertStoreMatchesDB(t, store, rw)
	assertNoDuplicateBroadcasts(t, pr.drain(), loaded)
	assertReplayIdempotent(t, store, rw)
}

type gapPoller struct {
	client *Client
	stop   func()
}

// startGapPoller runs a poller over store with one capturing hub client.
// stop waits for Start to return, so no tick races the assertions after it.
func startGapPoller(t *testing.T, db *DB, store *PacketStore) *gapPoller {
	t.Helper()
	hub := NewHub()
	c := &Client{send: make(chan []byte, 1024), release: func() {}}
	hub.mu.Lock()
	hub.clients[c] = true
	hub.mu.Unlock()
	poller := NewPoller(db, hub, 20*time.Millisecond)
	poller.store = store
	done := make(chan struct{})
	go func() {
		poller.Start()
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			poller.Stop()
			<-done
		})
	}
	t.Cleanup(stop)
	return &gapPoller{client: c, stop: stop}
}

// drain returns every message broadcast so far.
func (p *gapPoller) drain() [][]byte {
	var out [][]byte
	for {
		select {
		case m := <-p.client.send:
			out = append(out, m)
		default:
			return out
		}
	}
}

// loadedObsPaths maps each observation id the store holds to its path_json.
func loadedObsPaths(store *PacketStore) map[int]string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	out := make(map[int]string, len(store.byObsID))
	for id, o := range store.byObsID {
		out[id] = o.PathJSON
	}
	return out
}

// assertStoreMatchesDB checks the store holds exactly the DB's transmissions
// and observations: nothing lost, nothing counted twice.
// notLoaded names transmissions outside the loaded window; they and their
// observations are left out of the DB side.
func assertStoreMatchesDB(t *testing.T, store *PacketStore, rw *sql.DB, notLoaded ...int) {
	t.Helper()
	txWhere, obsWhere := "", ""
	if len(notLoaded) > 0 {
		ids := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(notLoaded)), ","), "[]")
		txWhere = " WHERE id NOT IN (" + ids + ")"
		obsWhere = " WHERE transmission_id NOT IN (" + ids + ")"
	}
	var dbTx, dbObs int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM transmissions` + txWhere).Scan(&dbTx); err != nil {
		t.Fatal(err)
	}
	if err := rw.QueryRow(`SELECT COUNT(*) FROM observations` + obsWhere).Scan(&dbObs); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	sumObs, sumCount := 0, 0
	for _, tx := range store.packets {
		sumObs += len(tx.Observations)
		sumCount += tx.ObservationCount
	}
	if len(store.packets) != dbTx || len(store.byHash) != dbTx {
		t.Errorf("store transmissions: packets=%d byHash=%d, DB has %d", len(store.packets), len(store.byHash), dbTx)
	}
	if store.totalObs != dbObs || len(store.byObsID) != dbObs || sumObs != dbObs || sumCount != dbObs {
		t.Errorf("store observations: totalObs=%d byObsID=%d attached=%d ObservationCount=%d, DB has %d",
			store.totalObs, len(store.byObsID), sumObs, sumCount, dbObs)
	}
}

// assertNoDuplicateBroadcasts checks the poller's catch-up over rows it
// re-read did not re-announce one the load already held, nor any row twice.
func assertNoDuplicateBroadcasts(t *testing.T, msgs [][]byte, loaded map[int]string) {
	t.Helper()
	loadedPaths := make(map[string]int, len(loaded))
	for id, p := range loaded {
		loadedPaths[p] = id
	}
	seen := make(map[string]bool)
	for _, raw := range msgs {
		var m struct {
			Type string `json:"type"`
			Data struct {
				Hash     string `json:"hash"`
				PathJSON string `json:"path_json"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("broadcast: %v", err)
		}
		key := m.Data.Hash + " " + m.Data.PathJSON
		if seen[key] {
			t.Errorf("broadcast twice: %s", key)
		}
		seen[key] = true
		if id, ok := loadedPaths[m.Data.PathJSON]; ok {
			t.Errorf("broadcast obs %d, which the startup load already held", id)
		}
	}
	if len(msgs) == 0 {
		t.Error("no broadcasts captured; the capturing client is not wired")
	}
}

// assertReplayIdempotent re-reads every row from id 0. A store that already
// holds them all must ingest nothing and announce nothing. notLoaded is as
// for assertStoreMatchesDB.
func assertReplayIdempotent(t *testing.T, store *PacketStore, rw *sql.DB, notLoaded ...int) {
	t.Helper()
	// IngestNewFromDB applies no retention window, so with transmissions
	// outside it a replay would rightly load them; replay observations only.
	if len(notLoaded) == 0 {
		if txs, _ := store.IngestNewFromDB(0, 1000); len(txs) != 0 {
			t.Errorf("replaying transmissions from 0 broadcast %d already-held packets", len(txs))
		}
	}
	if obs := store.IngestNewObservations(0, 1000); len(obs) != 0 {
		t.Errorf("replaying observations from 0 broadcast %d already-held observations", len(obs))
	}
	assertStoreMatchesDB(t, store, rw, notLoaded...)
}

// gapSeeder writes rows the way the ingestor does, with explicit ids (one
// writer: ids commit in order).
type gapSeeder struct {
	t    *testing.T
	rw   *sql.DB
	base time.Time
}

func newGapSeeder(t *testing.T, rw *sql.DB) *gapSeeder {
	g := &gapSeeder{t: t, rw: rw, base: time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)}
	for idx := 1; idx <= 3; idx++ {
		g.exec(`INSERT INTO observers (idx, id, name, iata, last_seen, first_seen) VALUES (?, ?, ?, 'SJC', ?, ?)`,
			idx, fmt.Sprintf("%064X", idx), fmt.Sprintf("Obs-%d", idx), g.iso(g.base), g.iso(g.base.Add(-time.Hour)))
	}
	return g
}

func (g *gapSeeder) iso(ts time.Time) string { return ts.Format("2006-01-02T15:04:05.000Z") }

// exec may run on a load or poller goroutine, so it reports with Errorf.
func (g *gapSeeder) exec(q string, args ...any) {
	if _, err := g.rw.Exec(q, args...); err != nil {
		g.t.Errorf("seed: %v\n%s", err, q)
	}
}

// tx inserts a transmission seen at base+id seconds, or two hours ago when
// old, which puts it outside a one-hour retention window.
func (g *gapSeeder) tx(id int, old bool) {
	seen := g.base.Add(time.Duration(id) * time.Second)
	if old {
		seen = g.base.Add(-2 * time.Hour)
	}
	g.exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
		VALUES (?, ?, ?, ?, 1, 5, 1, '{"type":"GRP_TXT"}', ?)`,
		id, fmt.Sprintf("11%02dAABBCCDD", id), gapHash(id), g.iso(seen), seen.Unix())
}

// obs inserts an observation with a unique path, so none dedupes.
func (g *gapSeeder) obs(id, tx, observer int) {
	g.exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
		VALUES (?, ?, ?, 'rx', 5.5, -90, 1, ?, ?)`,
		id, tx, observer, fmt.Sprintf(`["%02X"]`, id), g.base.Add(time.Duration(id)*time.Second).Unix())
}

func gapWait(t *testing.T, ch <-chan struct{}, what string) bool {
	select {
	case <-ch:
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("timed out waiting for %s", what)
		return false
	}
}

func storeHasObs(store *PacketStore, id int) bool {
	store.mu.RLock()
	defer store.mu.RUnlock()
	_, ok := store.byObsID[id]
	return ok
}

func waitStoreObs(store *PacketStore, id int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if storeHasObs(store, id) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return storeHasObs(store, id)
}

// The poller runs from the first chunk on, while later chunks still load.
// Obs 10 of chunk-2 tx 3 is committed after chunk 2's query and polled before
// chunk 2 merges, when tx 3 is not in the store yet. It must end up in the
// store exactly once. This pins an ordering, not a mechanism of its own: the
// poller reads its observation cursor bound before ingestNewObservations polls
// the advert feed, which waits on advertEvidenceMu that each chunk holds from
// its query to its merge. So obs 10 lies above the bound of the poll that
// skips it, and is read again once tx 3 is merged (or the chunk already saw
// it). Obs 11 belongs to a transmission outside retention; it is skipped and
// must not hold back the live observation committed after the load.

func TestStartupObsDuringChunkMerge_SQLite(t *testing.T) {
	db, rw := openGapSQLite(t)
	runStartupObsDuringChunkMerge(t, db, rw)
}

func TestStartupObsDuringChunkMerge_Postgres(t *testing.T) {
	db, rw := newPostgresTestDB(t) // skips without $CORESCOPE_TEST_PG_DSN
	runStartupObsDuringChunkMerge(t, db, rw)
}

func runStartupObsDuringChunkMerge(t *testing.T, db *DB, rw *sql.DB) {
	g := newGapSeeder(t, rw)
	for id := 1; id <= 4; id++ {
		g.tx(id, false)
		g.obs(id, id, 1)
	}
	g.tx(5, true) // outside retention: never loaded
	g.obs(5, 5, 1)

	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 1})
	pollerAtFeeds := make(chan struct{})
	releasePoller := make(chan struct{})
	pollerMerged := make(chan struct{})
	var stage atomic.Int32 // 0 idle, 1 poller paused, 2 merge done
	store.obsPollHook = func(st obsPollStage) {
		switch {
		case st == obsPollAfterFeeds && stage.CompareAndSwap(0, 1):
			close(pollerAtFeeds)
			gapWait(t, releasePoller, "chunk 2's query")
		case st == obsPollDone && stage.CompareAndSwap(1, 2):
			close(pollerMerged)
		}
	}
	store.OnChunkLoaded(func(int, int) {
		if store.loadProgressRows.Load() == 2 { // after chunk 1
			gapWait(t, pollerAtFeeds, "the poller's first tick")
		}
	})
	store.chunkQueriedHook = func(chunk int) {
		if chunk != 2 {
			return
		}
		g.obs(10, 3, 2) // of tx 3, which this chunk is about to merge
		g.obs(11, 5, 2) // orphan: tx 5 is outside retention
		close(releasePoller)
		gapWait(t, pollerMerged, "the poller's merge")
	}

	loadErr := make(chan error, 1)
	go func() { loadErr <- store.RunStartupLoad(2) }()
	<-store.FirstChunkReady()
	pr := startGapPoller(t, db, store)
	if err := <-loadErr; err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if stage.Load() != 2 {
		t.Fatal("setup: the poller never polled between chunk 2's query and its merge")
	}

	// After the load: a live observation must not wait behind orphan 11.
	g.obs(12, 1, 3)
	if !waitStoreObs(store, 12, 3*time.Second) {
		t.Fatal("live obs 12, committed after the load completed, never reached the store: the feed is blocked")
	}
	pr.stop()
	if !storeHasObs(store, 10) {
		t.Error("obs 10 (chunk-2 tx, committed after chunk 2's query, polled before its merge) is missing from the store")
	}
	if storeHasObs(store, 11) {
		t.Error("obs 11 of a transmission outside retention is in the store")
	}
	assertStoreMatchesDB(t, store, rw, 5)
	assertNoDuplicateBroadcasts(t, pr.drain(), nil)
	assertReplayIdempotent(t, store, rw, 5)
}

// A failed transmission or observation read must leave the poller's cursor
// where it was, so the same rows are read once the database recovers. The
// observation cursor's bound comes from its own query, which used to advance
// the cursor past rows the failed read never saw. Renaming observers fails
// both ingest reads (they join it) while the bound query still succeeds.

func TestPollerRetriesFailedRead_SQLite(t *testing.T) {
	db, rw := openGapSQLite(t)
	runPollerRetriesFailedRead(t, db, rw)
}

func TestPollerRetriesFailedRead_Postgres(t *testing.T) {
	db, rw := newPostgresTestDB(t) // skips without $CORESCOPE_TEST_PG_DSN
	runPollerRetriesFailedRead(t, db, rw)
}

func runPollerRetriesFailedRead(t *testing.T, db *DB, rw *sql.DB) {
	g := newGapSeeder(t, rw)
	g.tx(1, false)
	g.obs(1, 1, 1)
	store := NewPacketStore(db, &PacketStoreConfig{})
	if err := store.RunStartupLoad(100); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	loaded := loadedObsPaths(store)
	pr := startGapPoller(t, db, store)

	g.exec(`ALTER TABLE observers RENAME TO observers_away`)
	renamed := true
	defer func() {
		if renamed {
			g.exec(`ALTER TABLE observers_away RENAME TO observers`)
		}
	}()
	g.tx(2, false)
	g.obs(2, 2, 1) // arrives with tx 2
	g.obs(3, 1, 2) // a new observation of loaded tx 1

	time.Sleep(150 * time.Millisecond) // several failing ticks
	if storeHasObs(store, 2) || storeHasObs(store, 3) {
		t.Fatal("setup: the ingest reads did not fail while observers was renamed")
	}
	g.exec(`ALTER TABLE observers_away RENAME TO observers`)
	renamed = false

	if !waitStoreObs(store, 3, 3*time.Second) {
		t.Error("obs 3, committed while the observation read failed, never reached the store: the cursor passed it")
	}
	if !waitStoreObs(store, 2, time.Second) {
		t.Error("tx 2 and its obs 2, committed while the transmission read failed, never reached the store")
	}
	pr.stop()
	assertStoreMatchesDB(t, store, rw)
	assertNoDuplicateBroadcasts(t, pr.drain(), loaded)
	assertReplayIdempotent(t, store, rw)
}
