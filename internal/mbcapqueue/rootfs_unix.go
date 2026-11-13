//go:build unix

package mbcapqueue

import (
	"os"
	"syscall"
)

const openReadFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
