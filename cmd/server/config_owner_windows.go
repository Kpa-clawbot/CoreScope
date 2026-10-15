//go:build windows

package main

import "os"

// copyOwner is a no-op on Windows: there is no uid/gid to copy, and the new
// file takes the ACL of its directory.
func copyOwner(path string, fi os.FileInfo) {}
