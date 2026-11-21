//go:build !linux

package main

// Portable protocol-only tests compile here; measured runs are Linux-only.
func benchMono() int64 { panic("measurement requires Linux CLOCK_MONOTONIC") }
