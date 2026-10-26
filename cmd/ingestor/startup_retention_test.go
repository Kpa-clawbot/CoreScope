package main

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Main must use the behavior-tested orchestration. The existing hop-index
// ordering guard separately pins subscription and required configuration.
func TestMainUsesStartupRetentionOrchestration(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var mainFn *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "main" {
			mainFn = fn
		}
	}
	if mainFn == nil {
		t.Fatal("main not found")
	}
	var startup *ast.CallExpr
	var prunes []*ast.CallExpr
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		if _, background := n.(*ast.GoStmt); background {
			return false // the daily retention worker is independent
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "finishIngestStartup" {
			if startup != nil {
				t.Error("startup orchestration called more than once")
			}
			startup = call
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "PruneOldPackets" {
			prunes = append(prunes, call)
		}
		return true
	})
	if startup == nil {
		t.Fatal("main must use finishIngestStartup to open ingestion before packet retention")
	}
	if len(startup.Args) != 3 {
		t.Fatal("startup orchestration must receive the store, readiness callback and packet retention callback")
	}
	if store, ok := startup.Args[0].(*ast.Ident); !ok || store.Name != "store" {
		t.Error("startup orchestration is not waiting on the active store")
	}
	readyCalls := 0
	ast.Inspect(startup.Args[1], func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Ready" {
				if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "ingestBuffer" {
					readyCalls++
				}
			}
		}
		return true
	})
	if readyCalls != 1 {
		t.Fatalf("readiness callback has %d ingestBuffer.Ready calls, want 1", readyCalls)
	}
	if len(prunes) != 1 || prunes[0].Pos() < startup.Args[2].Pos() || prunes[0].End() > startup.Args[2].End() {
		t.Fatal("all startup packet retention must run through the orchestration callback")
	}
}

// Retention is paused after a real committed batch, with expired rows still
// present. Ingest must use that writer gap, not wait for the whole backlog.
// Deadlines only catch a stuck goroutine; no machine-speed threshold is the
// correctness criterion.
func TestStartupRetentionAdmitsBufferedMQTTBetweenBatches(t *testing.T) {
	s := newTestStore(t)
	const aged = 2*pruneBatchTransmissions + 7
	seedRetentionFanout(t, s, aged, aged*2)
	b := NewIngestBuffer(3)
	b.Start()
	t.Cleanup(func() { b.Stop(); <-b.Done() })
	initRelease, initialized := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.RunAsyncMigration(ctx, "startup_test_required", func(ctx context.Context, _ *sql.DB) error {
		select {
		case <-initRelease:
			close(initialized)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	processed := make(chan int, 4)
	submit := func(sequence int) {
		b.Submit(func() {
			select {
			case <-initialized:
			default:
				t.Error("buffered work ran before required initialization finished")
			}
			msg := &mockMessage{
				topic:   fmt.Sprintf("meshcore/TEST/startup-%d/packets", sequence),
				payload: []byte(`{"raw":"0A00D69FD7A5A7475DB07337749AE61FA53A4788E976","SNR":5.5,"RSSI":-100}`),
			}
			handleMessage(s, "startup-test", MQTTSource{Name: "startup-test"}, msg, nil, nil, &Config{})
			processed <- sequence
		})
	}
	for i := 0; i < 4; i++ {
		submit(i)
	}
	if b.Pending() != 3 || b.Dropped() != 1 {
		t.Fatalf("before readiness: pending=%d dropped=%d, want 3 and 1", b.Pending(), b.Dropped())
	}

	paused, releaseRetention, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var removed int64
	var pruneErr error
	readyCalls, pruneCalls, batches := 0, 0, 0
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(releaseRetention) })
		<-done
	})
	go func() {
		defer close(done)
		removed, pruneErr = finishIngestStartup(s, func() {
			readyCalls++
			if status, err := s.AsyncMigrationStatus("startup_test_required"); err != nil || status != "done" {
				t.Errorf("readiness preceded required migration completion: status=%q err=%v", status, err)
			}
			b.Ready()
		}, func() (int64, error) {
			pruneCalls++
			return s.pruneOldPackets(5, func() {
				batches++
				if batches == 1 {
					close(paused)
					<-releaseRetention
				}
			})
		})
	}()
	select {
	case <-b.ready:
		t.Error("ingestion opened while the required migration was still blocked")
	case <-time.After(30 * time.Millisecond):
	}
	close(initRelease)
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("retention did not reach the committed batch boundary")
	}
	var remaining int
	if err := s.db.QueryRow(`SELECT count(*) FROM transmissions WHERE first_seen < '2021-01-01'`).Scan(&remaining); err != nil || remaining != aged-pruneBatchTransmissions {
		t.Fatalf("paused retention: remaining=%d err=%v", remaining, err)
	}
	for _, want := range []int{0, 1, 2, 4} {
		if want == 4 {
			submit(want) // live arrivals also run while the backlog is paused
		}
		select {
		case got := <-processed:
			if got != want {
				t.Fatalf("buffer order: got %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("buffered MQTT work could not write between retention batches; startup gate is still closed")
		}
	}
	if b.Pending() != 0 || b.Dropped() != 1 {
		t.Fatalf("during retention: pending=%d dropped=%d, want 0 and 1", b.Pending(), b.Dropped())
	}
	releaseOnce.Do(func() { close(releaseRetention) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retention did not finish after release")
	}
	if removed != aged || pruneErr != nil || readyCalls != 1 || pruneCalls != 1 || batches != 2 {
		t.Fatalf("removed=%d err=%v ready=%d prune=%d batch gaps=%d", removed, pruneErr, readyCalls, pruneCalls, batches)
	}
	if countRows(t, s, "transmissions") != 1 || countRows(t, s, "observations") != 4 || s.Stats.WriteErrors.Load() != 0 {
		t.Fatal("startup retention lost live MQTT writes or retained expired rows")
	}
}

func TestStartupRetentionReturnsPruneFailureAndAllowsRetry(t *testing.T) {
	s := newTestStore(t)
	seedRetentionFanout(t, s, 500, 1000)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_startup_prune BEFORE DELETE ON observations WHEN OLD.transmission_id=251 BEGIN SELECT RAISE(ABORT,'startup retention test failure'); END`); err != nil {
		t.Fatal(err)
	}
	b := NewIngestBuffer(1)
	b.Start()
	t.Cleanup(func() { b.Stop(); <-b.Done() })
	written := make(chan error, 1)
	b.Submit(func() {
		_, err := s.InsertTransmission(&PacketData{Hash: "startup-live", RawHex: "00", Timestamp: time.Now().UTC().Format(time.RFC3339), PathJSON: "[]", DecodedJSON: "{}"})
		written <- err
	})
	readyCalls, pruneCalls := 0, 0
	removed, err := finishIngestStartup(s, func() { readyCalls++; b.Ready() }, func() (int64, error) {
		pruneCalls++
		return s.PruneOldPackets(5)
	})
	if removed != 250 || err == nil || !strings.Contains(err.Error(), "startup retention test failure") || readyCalls != 1 || pruneCalls != 1 {
		t.Fatalf("removed=%d err=%v ready=%d prune=%d", removed, err, readyCalls, pruneCalls)
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prune failure prevented buffered work from writing")
	}
	if countRows(t, s, "transmissions") != 251 || countRows(t, s, "observations") != 501 {
		t.Fatal("failed startup batch was not rolled back or live write was lost")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_startup_prune`); err != nil {
		t.Fatal(err)
	}
	if removed, err := s.PruneOldPackets(5); removed != 250 || err != nil {
		t.Fatalf("retry removed=%d err=%v", removed, err)
	}
	if countRows(t, s, "transmissions") != 1 || countRows(t, s, "observations") != 1 {
		t.Fatal("retry lost live rows or left expired rows")
	}
}

func TestStartupRetentionStoppedBufferDoesNotDrainAfterInitialization(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.RunAsyncMigration(ctx, "startup_test_cancelled", func(ctx context.Context, _ *sql.DB) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	b := NewIngestBuffer(1)
	b.Start()
	var writes atomic.Int32
	b.Submit(func() { writes.Add(1) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		finishIngestStartup(s, b.Ready, func() (int64, error) { return s.PruneOldPackets(0) })
	}()
	b.Stop()
	<-b.Done() // shutdown has stopped the consumer before initialization ends
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled initialization did not finish")
	}
	if writes.Load() != 0 || b.Pending() != 1 || b.Dropped() != 0 {
		t.Fatalf("stopped buffer drained: writes=%d pending=%d dropped=%d", writes.Load(), b.Pending(), b.Dropped())
	}
	if status, err := s.AsyncMigrationStatus("startup_test_cancelled"); err != nil || status != "cancelled" {
		t.Fatalf("cancelled initialization: status=%q err=%v", status, err)
	}
}
