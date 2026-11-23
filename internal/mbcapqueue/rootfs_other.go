//go:build !unix

package mbcapqueue

import "os"

const openReadFlags = os.O_RDONLY
