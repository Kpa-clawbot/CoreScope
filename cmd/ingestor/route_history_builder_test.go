package main

import (
	"path/filepath"
	"testing"
)

func TestRouteHistoryBuilderPersistsResolvedAdjacentEdges(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "route-history.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?), (?, ?)`,
		"aa11111111111111", "a",
		"bb22222222222222", "b",
		"cc33333333333333", "c",
	); err != nil {
		t.Fatal(err)
	}
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES (?, ?, ?)`,
		"", "hash-route", "2026-01-01T00:30:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, path_json, resolved_path, timestamp)
		 VALUES (?, ?, ?, ?)`,
		txID, `["aa","bb","cc"]`,
		`["aa11111111111111","bb22222222222222","cc33333333333333"]`,
		int64(1735691400),
	); err != nil {
		t.Fatal(err)
	}

	inserted, err := store.buildAndPersistRouteHistoryEdges(0)
	if err != nil {
		t.Fatalf("buildAndPersistRouteHistoryEdges: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2 adjacent edges", inserted)
	}
	inserted, err = store.buildAndPersistRouteHistoryEdges(0)
	if err != nil {
		t.Fatalf("second buildAndPersistRouteHistoryEdges: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("second build should be idempotent, inserted = %d", inserted)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM route_history_edges WHERE packet_hash = ?`, "hash-route").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("route_history_edges rows = %d, want 2", count)
	}
	var hourlyCount int
	if err := store.db.QueryRow(`SELECT COALESCE(SUM(count), 0) FROM route_history_edge_hourly`).Scan(&hourlyCount); err != nil {
		t.Fatal(err)
	}
	if hourlyCount != 2 {
		t.Fatalf("hourly route-history count = %d, want 2", hourlyCount)
	}
}

func TestRouteHistoryBuilderResolvesRawPathPrefixes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "route-history-prefix.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aa11111111111111", "a",
		"bb22222222222222", "b",
	); err != nil {
		t.Fatal(err)
	}
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES (?, ?, ?)`,
		"", "hash-prefix", "2026-01-01T00:30:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp)
		 VALUES (?, ?, ?)`,
		txID, `["aa","bb"]`, int64(1735691400),
	); err != nil {
		t.Fatal(err)
	}

	inserted, err := store.buildAndPersistRouteHistoryEdges(0)
	if err != nil {
		t.Fatalf("buildAndPersistRouteHistoryEdges: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1 resolved prefix edge", inserted)
	}
	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM route_history_edges WHERE node_a = ? AND node_b = ?`,
		"aa11111111111111", "bb22222222222222").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("resolved route-history edge rows = %d, want 1", got)
	}
}
