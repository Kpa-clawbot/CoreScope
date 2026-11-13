package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Regression: on static musl builds every non-main thread gets a 128 KiB stack
// (glibc: 8 MiB), and cgo calls into SQLite run on the calling thread's stack.
// SQLite's multiSelect recurses ~850 B per UNION ALL term, so the wide chunked
// queries below overflow 128 KiB off the main thread and SIGSEGV the process.
// The fix is the linker flag -Wl,-z,stack-size=8388608 (set via -extldflags in
// the Dockerfile and Makefile; `make test` passes it too). Without it this test
// crashes the whole test binary on musl; on glibc it passes either way.

// offMainThread runs fn on an OS thread that is not the process's main thread.
// The main thread has the kernel-sized stack, so it would hide the bug. A
// goroutine locked to the main thread cannot be moved off it, so it runs fn in
// a fresh goroutine instead; a fresh goroutine is never on the main thread
// while the locking goroutine still holds it.
func offMainThread(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if syscall.Gettid() != os.Getpid() {
		fn()
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		fn()
	}()
	<-done
}

func TestMuslThreadStackWideUnionQueries(t *testing.T) {
	srv := seedClientNames(t)

	keys := make([]string, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("%08x", 0x30000000+i)
	}
	targets := make([]string, 160)
	for i := range targets {
		targets[i] = fmt.Sprintf("%016x", 0x4000000000000000+uint64(i)) + strings.Repeat("0", 48)
	}

	var hits map[string]prefixHit
	var shared map[string]bool
	offMainThread(func() {
		hits = srv.lookupPrefixes(keys)
		shared = srv.sharedEightBytePrefixes(targets)
	})

	// Nothing seeded matches, so every key resolves to no candidate. A chunk
	// failure would drop keys or flag every prefix as shared.
	for _, k := range keys {
		if h, ok := hits[k]; ok && h.matches != 0 {
			t.Fatalf("key %s: unexpected match %+v", k, h)
		}
	}
	if len(shared) != 0 {
		t.Fatalf("sharedEightBytePrefixes flagged %d prefixes, want 0 (query failed?)", len(shared))
	}
}
