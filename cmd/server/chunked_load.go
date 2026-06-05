package main

// Stubs for issue #1009 chunked-load contract.
//
// RED commit: these intentionally do nothing useful so the
// chunked_load_test.go assertions fail in a behavior-asserting way
// rather than a compile error. The GREEN commit replaces these stubs
// with the real implementation.

import (
	"net/http"

	"github.com/meshcore-analyzer/dbconfig"
)

// dbLoadConfig is the server-package alias for dbconfig.LoadConfig (#1009).
type dbLoadConfig = dbconfig.LoadConfig

// DBLoadChunkSize returns the configured chunk size for chunked startup
// load, or 10000 default (#1009).
func (c *Config) DBLoadChunkSize() int {
	return c.DB.GetLoadChunkSize()
}

// LoadChunked is the chunked-load entry point that backs the early
// HTTP readiness contract from #1009. STUB.
func (s *PacketStore) LoadChunked(chunkSize int) error {
	// Stub: do not signal FirstChunkReady, do not fire chunk callbacks,
	// do not flip LoadComplete. Tests will fail on assertion.
	return nil
}

// FirstChunkReady returns a channel that is closed once the first
// chunk has been merged into the store. STUB returns a never-closing
// channel so the readiness assertion fires.
func (s *PacketStore) FirstChunkReady() <-chan struct{} {
	return make(chan struct{})
}

// LoadComplete reports whether LoadChunked has finished. STUB.
func (s *PacketStore) LoadComplete() bool {
	return false
}

// OnChunkLoaded registers a callback invoked once per loaded chunk.
// STUB: never stores or invokes the callback.
func (s *PacketStore) OnChunkLoaded(fn func(rowsThisChunk, totalRows int)) {
	_ = fn
}

// loadStatusMiddleware stamps X-CoreScope-Load-Status on responses
// based on store.LoadComplete(). STUB always reports "unknown" so the
// transition assertion fires.
func loadStatusMiddleware(s *PacketStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CoreScope-Load-Status", "unknown")
		next.ServeHTTP(w, r)
	})
}
