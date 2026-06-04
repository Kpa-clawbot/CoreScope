package main

// Stub for #1561 — replaced by real implementation in the GREEN commit.
// Lives in routes.go-adjacent file so tests compile and execute to
// the assertion stage (red commit must fail on assertions, not build).

import (
	"net/http"
	"sync"
)

var cdnWarnOnce sync.Once

// cdnDetectionMiddleware: stub returns the next handler unchanged.
// Real implementation lands in the GREEN commit.
func cdnDetectionMiddleware(next http.Handler) http.Handler {
	return next
}
