//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

// CLOCK_MONOTONIC is shared across processes on this one Linux host.
func benchMono() int64 {
	var ts syscall.Timespec
	_, _, e := syscall.RawSyscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	if e != 0 {
		panic(e)
	}
	return ts.Sec*1e9 + ts.Nsec
}
