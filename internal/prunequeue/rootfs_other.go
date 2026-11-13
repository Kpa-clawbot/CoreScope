//go:build !unix

package prunequeue

import "os"

const openReadFlags = os.O_RDONLY
