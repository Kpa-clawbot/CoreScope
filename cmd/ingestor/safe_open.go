package main

import (
	"fmt"
	"io"
	"os"
)

// maxChannelKeysFileBytes caps the channel keys file. The file is a flat JSON
// object of name -> 32 hex characters (about 60 bytes an entry; the shipped
// one is ~16 KB), so 4 MiB holds some 70,000 channels, hundreds of times the
// realistic size, while still stopping a planted or runaway file from being
// read whole into memory.
const maxChannelKeysFileBytes = 4 << 20

// readRegularFile reads a configuration-named file without trusting what is
// at the path: it opens read-only, refusing a symlink (O_NOFOLLOW) and never
// waiting on a FIFO's writer (O_NONBLOCK), then fstats the open descriptor
// and requires a regular file of at most maxBytes. A missing file returns an
// error satisfying os.IsNotExist, so callers can treat it as optional.
func readRegularFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow|oNonBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > maxBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit", path, st.Size(), maxBytes)
	}
	// Bound the read too: the file can grow after the fstat.
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s is over the %d byte limit", path, maxBytes)
	}
	return data, nil
}
