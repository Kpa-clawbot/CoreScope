package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestSourceLivenessState_ReceiptVsWriteSeparate asserts that the receipt-
// time and post-write liveness clocks are independent (PR #1609 review
// MAJOR M1): stamping at receipt must NOT advance the post-write clock so
// the watchdog/healthz can distinguish "broker alive, write path stuck"
// from "everything fine". Without separation, /healthz reports "fresh"
// while the writer is stalled and the ingest buffer is filling.
func TestSourceLivenessState_ReceiptVsWriteSeparate(t *testing.T) {
	s := &SourceLivenessState{Tag: "t"}
	now := time.Now()

	// Receipt at T0; post-write never happens (writer stalled).
	s.MarkReceipt(now)

	gotReceipt := atomic.LoadInt64(&s.LastReceiptUnix)
	gotWrite := atomic.LoadInt64(&s.LastMessageUnix)
	if gotReceipt != now.Unix() {
		t.Fatalf("LastReceiptUnix: want %d, got %d", now.Unix(), gotReceipt)
	}
	if gotWrite != 0 {
		t.Fatalf("LastMessageUnix MUST stay 0 while writer stalled (only MarkReceipt called); got %d — receipt is double-stamping the write clock and /healthz will lie about ingestion freshness", gotWrite)
	}

	// Write completes later: only MarkMessage advances LastMessageUnix.
	later := now.Add(5 * time.Second)
	s.MarkMessage(later)

	gotReceipt2 := atomic.LoadInt64(&s.LastReceiptUnix)
	gotWrite2 := atomic.LoadInt64(&s.LastMessageUnix)
	if gotReceipt2 != now.Unix() {
		t.Fatalf("MarkMessage must not move LastReceiptUnix backwards or forwards; want %d, got %d", now.Unix(), gotReceipt2)
	}
	if gotWrite2 != later.Unix() {
		t.Fatalf("LastMessageUnix after MarkMessage: want %d, got %d", later.Unix(), gotWrite2)
	}
}
