//go:build !windows

package main

import (
	"os"
	"syscall"
)

// copyOwner gives path the owner and group recorded in fi, best effort: a
// process that may not chown (not root, or not in the group) leaves path as
// it was created. On Windows there is no uid/gid; see config_owner_windows.go.
func copyOwner(path string, fi os.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(st.Uid), int(st.Gid))
	}
}
