package main

import (
	"os"
	"testing"
)

func TestApplyMemoryLimit_FromEnv(t *testing.T) {
	os.Setenv("GOMEMLIMIT", "512MiB")
	defer os.Unsetenv("GOMEMLIMIT")

	_, src := applyMemoryLimit(&Config{})
	if src != "env" {
		t.Fatalf("expected source env, got %s", src)
	}
}

func TestApplyMemoryLimit_Derived(t *testing.T) {
	os.Unsetenv("GOMEMLIMIT")

	cfg := &Config{PacketStore: &PacketStoreConfig{MaxMemoryMB: 200}}
	limit, src := applyMemoryLimit(cfg)
	if src != "derived" {
		t.Fatalf("expected source derived, got %s", src)
	}
	// 200 * 1.5 * 1MiB = 314572800
	if limit != 314572800 {
		t.Fatalf("expected 314572800, got %d", limit)
	}
}

func TestApplyMemoryLimit_None(t *testing.T) {
	os.Unsetenv("GOMEMLIMIT")

	_, src := applyMemoryLimit(&Config{})
	if src != "none" {
		t.Fatalf("expected source none, got %s", src)
	}
}
