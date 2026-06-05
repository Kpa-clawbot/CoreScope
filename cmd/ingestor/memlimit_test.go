package main

import (
	"runtime/debug"
	"testing"
)

// TestApplyMemoryLimit_FromEnv: when GOMEMLIMIT env var is set, the runtime
// already parsed it. Our function MUST NOT override and MUST report env source.
func TestApplyMemoryLimit_FromEnv(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "850MiB")
	defer debug.SetMemoryLimit(-1)

	limit, source := applyMemoryLimit(512, true /* envSet */)
	if source != "env" {
		t.Fatalf("expected source=env, got %q", source)
	}
	if limit != 0 {
		t.Fatalf("expected limit=0 (not set by us), got %d", limit)
	}
}

// TestApplyMemoryLimit_FromConfig: when env is unset and runtime.maxMemoryMB
// is set, derive a limit of exactly runtimeMaxMB * 1 MiB (no headroom — the
// ingestor's working set is bounded by MQTT batch decode, not packet store).
func TestApplyMemoryLimit_FromConfig(t *testing.T) {
	defer debug.SetMemoryLimit(-1)

	limit, source := applyMemoryLimit(512, false /* envSet */)
	if source != "config" {
		t.Fatalf("expected source=config, got %q", source)
	}
	want := int64(512) * 1024 * 1024
	if limit != want {
		t.Fatalf("expected limit=%d, got %d", want, limit)
	}
	cur := debug.SetMemoryLimit(-1)
	if cur != want {
		t.Fatalf("runtime memory limit not set: want=%d got=%d", want, cur)
	}
}

// TestApplyMemoryLimit_None: neither env nor config — no limit applied,
// default behavior preserved.
func TestApplyMemoryLimit_None(t *testing.T) {
	defer debug.SetMemoryLimit(-1)
	debug.SetMemoryLimit(int64(1<<63 - 1)) // math.MaxInt64 = "no limit"

	limit, source := applyMemoryLimit(0, false)
	if source != "none" {
		t.Fatalf("expected source=none, got %q", source)
	}
	if limit != 0 {
		t.Fatalf("expected limit=0, got %d", limit)
	}
}

// TestApplyMemoryLimit_EnvWinsOverConfig: env set AND config set → env wins,
// our function does not override. Locks the precedence triage specified.
func TestApplyMemoryLimit_EnvWinsOverConfig(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "1GiB")
	defer debug.SetMemoryLimit(-1)

	limit, source := applyMemoryLimit(512, true /* envSet */)
	if source != "env" {
		t.Fatalf("expected source=env when both set, got %q", source)
	}
	if limit != 0 {
		t.Fatalf("expected limit=0 when env wins, got %d", limit)
	}
}
