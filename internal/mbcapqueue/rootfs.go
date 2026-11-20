package mbcapqueue

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxFileBytes caps a snapshot document, on write and on read. It equals the
// payload CHECK on the Postgres mbcap_snapshots table (4 MiB), so both stores
// refuse the same documents; a hostile peer cannot make the reader allocate
// more.
const maxFileBytes = 4 << 20

// checkSize refuses an encoded snapshot over the cap before it is stored.
func checkSize(n int) error {
	if n > maxFileBytes {
		return fmt.Errorf("snapshot is %d bytes, exceeds %d", n, maxFileBytes)
	}
	return nil
}

// openQueueRoot opens dir as an os.Root so every later operation is confined
// to it: no name resolved through the Root can leave dir, even through a
// symlink planted inside. The final component of dir is Lstat'd through its
// parent and must be a real directory, so a queue directory swapped for a
// symlink is refused instead of followed. With create, a missing dir is made
// (0755). The caller must Close the Root.
func openQueueRoot(dir string, create bool) (*os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(dir)
	fi, err := parent.Lstat(name)
	if os.IsNotExist(err) && create {
		if err = parent.Mkdir(name, 0o755); err != nil && !os.IsExist(err) {
			return nil, err
		}
		fi, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: queue path is not a directory", name)
	}
	return parent.OpenRoot(name)
}

// writeFileAtomic writes b as name inside root via a randomly named O_EXCL
// temp file in the same root (a symlink pre-planted at any guessable name is
// never followed), fsyncs, then renames over name. Rename replaces a symlink
// at name itself rather than writing through it. The result is mode 0644 so
// the other process (a different user) can read it.
func writeFileAtomic(root *os.Root, name string, b []byte) (err error) {
	var f *os.File
	var tmp string
	for i := 0; ; i++ {
		var r [8]byte
		if _, err = rand.Read(r[:]); err != nil {
			return err
		}
		tmp = name + "." + hex.EncodeToString(r[:]) + ".tmp"
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) || i >= 8 {
			return err
		}
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

// readRegularFile reads name from root only if it is a regular file (not a
// symlink, directory, device or FIFO) no larger than maxFileBytes. The open
// uses O_NOFOLLOW|O_NONBLOCK (see openReadFlags) so a FIFO or device planted
// at name cannot block the reader; the type is then checked on the opened
// handle. A missing file yields an error satisfying os.IsNotExist.
func readRegularFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, openReadFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	if fi.Size() > maxFileBytes {
		return nil, fmt.Errorf("%s: exceeds %d bytes", name, maxFileBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("%s: exceeds %d bytes", name, maxFileBytes)
	}
	return b, nil
}
