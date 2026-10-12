package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestPostgresStartupUnlimitedCursorUsesPrimaryKeySeek(t *testing.T) {
	postgresOnly(t)
	db, writer := openPostgresValueFixture(t)
	stamp := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	copyTestRows(t, writer, "transmissions", []string{"id", "raw_hex", "hash", "first_seen", "last_seen"}, 20000, func(n int) []any {
		// A large tie group proves the ID half of the cursor also seeks.
		return []any{n, "AA", fmt.Sprintf("seek-%d", n), stamp.Format(time.RFC3339), stamp.Unix()}
	})
	if _, err := writer.Exec("ANALYZE transmissions"); err != nil {
		t.Fatal(err)
	}
	s := NewPacketStore(db, &PacketStoreConfig{})
	query, args := s.packetLoadQuery(stamp.Add(-time.Hour), time.Time{}, packetLoadCursor{id: 10000, recency: strconv.FormatInt(stamp.Unix(), 10)}, 100)
	var raw []byte
	if err := db.conn.QueryRow("EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		Type     string     `json:"Node Type"`
		Relation string     `json:"Relation Name"`
		Index    string     `json:"Index Name"`
		Rows     float64    `json:"Actual Rows"`
		Filtered float64    `json:"Rows Removed by Filter"`
		Plans    []planNode `json:"Plans"`
	}
	var plans []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("invalid plan: %v", err)
	}
	pending := []planNode{plans[0].Plan}
	indexed := false
	for len(pending) > 0 {
		node := pending[0]
		pending = append(pending[1:], node.Plans...)
		if node.Relation != "transmissions" {
			continue
		}
		if node.Index == "transmissions_pkey" {
			indexed = true
		}
		if node.Rows+node.Filtered > 100 {
			t.Fatalf("100-row page rescanned history: %s (%s), rows=%g filtered=%g", node.Type, node.Index, node.Rows, node.Filtered)
		}
	}
	if !indexed {
		t.Fatal("unlimited startup cursor did not use the primary-key index")
	}
}
