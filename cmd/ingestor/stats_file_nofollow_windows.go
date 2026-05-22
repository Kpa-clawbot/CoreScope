//go:build windows

package main

// oNoFollow is 0 on Windows: O_NOFOLLOW is not defined in the Windows syscall
// package. The ingestor is only deployed on Linux where the flag is enforced;
// on Windows the flag is a no-op so the binary compiles and tests run.
const oNoFollow = 0
