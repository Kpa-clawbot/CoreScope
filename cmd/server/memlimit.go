package main

import (
	"os"
	"runtime/debug"
)

// applyMemoryLimit configures the Go runtime soft memory limit.
// Priority: GOMEMLIMIT env → derived from maxMemoryMB×1.5 → none.
// Returns the applied limit in bytes and source ("env", "derived", or "none").
func applyMemoryLimit(cfg *Config) (int64, string) {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		// Already honored by the runtime; just report it.
		return debug.SetMemoryLimit(-1), "env"
	}

	if cfg.PacketStore != nil && cfg.PacketStore.MaxMemoryMB > 0 {
		limit := int64(cfg.PacketStore.MaxMemoryMB) * 3 / 2 * (1 << 20)
		debug.SetMemoryLimit(limit)
		return limit, "derived"
	}

	return 0, "none"
}
