//go:build !windows

package main

import "syscall"

// oNoFollow is syscall.O_NOFOLLOW on platforms that define it (all non-Windows targets).
// On Windows this constant does not exist; see stats_file_nofollow_windows.go.
const oNoFollow = syscall.O_NOFOLLOW
