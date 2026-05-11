package main

import (
	"runtime/debug"
	"testing"
)

func TestApplyMemoryLimit_FromEnv(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "850MiB")
	// reset to a known state after test
	defer debug.SetMemoryLimit(-1)

	limit, source := applyMemoryLimit(512, true /* envSet */)
	if source != "env" {
		t.Fatalf("expected source=env, got %q", source)
	}
	// When env is set, our function must NOT override it; reported limit is 0.
	if limit != 0 {
		t.Fatalf("expected limit=0 (not set by us), got %d", limit)
	}
}

func TestApplyMemoryLimit_DerivedFromMaxMemoryMB(t *testing.T) {
	defer debug.SetMemoryLimit(-1)

	// maxMemoryMB=512 → 512 * 1.5 = 768 MiB = 768 * 1024 * 1024 bytes
	limit, source := applyMemoryLimit(512, false /* envSet */)
	if source != "derived" {
		t.Fatalf("expected source=derived, got %q", source)
	}
	want := int64(768) * 1024 * 1024
	if limit != want {
		t.Fatalf("expected limit=%d, got %d", want, limit)
	}
	// Verify it was actually set on the runtime
	cur := debug.SetMemoryLimit(-1)
	if cur != want {
		t.Fatalf("runtime memory limit not set: want=%d got=%d", want, cur)
	}
}

func TestApplyMemoryLimit_None(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	// Reset to "no limit" (math.MaxInt64) before test
	debug.SetMemoryLimit(int64(1<<63 - 1))

	limit, source := applyMemoryLimit(0, false)
	if source != "none" {
		t.Fatalf("expected source=none, got %q", source)
	}
	if limit != 0 {
		t.Fatalf("expected limit=0, got %d", limit)
	}
}
