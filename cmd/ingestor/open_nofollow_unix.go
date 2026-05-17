//go:build !windows

package main

import "syscall"

const openNoFollow = syscall.O_NOFOLLOW
