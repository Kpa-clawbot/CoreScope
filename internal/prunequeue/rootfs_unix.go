//go:build unix

package prunequeue

import (
	"os"
	"syscall"
)

const openReadFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
