package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Regression for musl thread stacks. musl sizes every non-main thread's stack
// from the ELF PT_GNU_STACK size (floor 128 KiB), and cgo calls into SQLite
// run on the calling thread's stack. SQLite's multiSelect recurses ~850 B per
// UNION ALL term, so the wide chunked queries below overflow 128 KiB off the
// main thread and SIGSEGV the process.
//
// Which links are exposed: a native musl link with gcc and GNU ld (`go test`
// or `make build` on Alpine) writes a zero PT_GNU_STACK and crashes here
// without -Wl,-z,stack-size=8388608. zig's linker writes 16 MiB by default, so
// the zig-linked release binaries pass with or without the flag, and so does
// any glibc link. That makes this a test for Alpine hosts (`make test` passes
// the flag); the shipped artifacts are guarded by the readelf checks in the
// Dockerfile and the release job instead.
//
// Linux only: it needs the thread id to stay off the main thread.

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

	// 249 keys nothing matches and one that resolves, in the second chunk.
	keys := make([]string, 0, 250)
	for i := 0; i < 249; i++ {
		keys = append(keys, fmt.Sprintf("%08x", 0x30000000+i))
	}
	keys = append(keys, "c0cd")
	// 160 targets nothing shares a prefix with, and one whose 8-byte prefix is
	// also the seeded node cnnAdvertPK's.
	targets := make([]string, 0, 161)
	for i := 0; i < 160; i++ {
		targets = append(targets, fmt.Sprintf("%016x", 0x4000000000000000+uint64(i))+strings.Repeat("0", 48))
	}
	sharedTarget := cnnAdvertPK[:16] + strings.Repeat("f", 48)
	targets = append(targets, sharedTarget)

	var hits map[string]prefixHit
	var shared map[string]bool
	offMainThread(func() {
		hits = srv.lookupPrefixes(keys)
		shared = srv.sharedEightBytePrefixes(targets)
	})

	// A failed lookupPrefixes chunk drops its keys, and a failed
	// sharedEightBytePrefixes chunk marks every prefix in it shared. Both
	// results are checked exactly, so neither failure passes as "no match".
	if len(hits) != len(keys) {
		t.Fatalf("lookupPrefixes returned %d keys, want %d (a chunk failed)", len(hits), len(keys))
	}
	if h := hits["c0cd"]; h.pubkey != cnnOnlyPK || h.name != "Self Named" {
		t.Fatalf("c0cd = %+v, want %s / Self Named", h, cnnOnlyPK)
	}
	for _, k := range keys[:249] {
		if h := hits[k]; h.matches != 0 {
			t.Fatalf("key %s: unexpected match %+v", k, h)
		}
	}
	if len(shared) != 1 || !shared[sharedTarget[:16]] {
		t.Fatalf("sharedEightBytePrefixes = %v, want only %s (a chunk failed?)", shared, sharedTarget[:16])
	}
}
