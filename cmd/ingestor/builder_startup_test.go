package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestBuildersReturnBeforeInitialWarmup(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "startup-builders.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	hold, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin hold tx: %v", err)
	}

	start := time.Now()
	stopNeighbor := store.StartNeighborEdgesBuilder(time.Hour)
	stopRouteHistory := store.StartRouteHistoryBuilder(time.Hour)
	stopDerived := store.StartDerivedEdgesBuilder(time.Hour, RouteHistoryBackfillSettings{Enabled: false})
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("builders blocked startup for %s", elapsed)
	}

	if err := hold.Rollback(); err != nil {
		t.Fatalf("rollback hold tx: %v", err)
	}
	stopNeighbor()
	stopRouteHistory()
	stopDerived()
}
