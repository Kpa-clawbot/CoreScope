//go:build windows

package dbconfig

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var selectionKernel = syscall.NewLazyDLL("kernel32.dll")
var selectionLock = selectionKernel.NewProc("LockFileEx")
var selectionUnlock = selectionKernel.NewProc("UnlockFileEx")
var selectionMove = selectionKernel.NewProc("MoveFileExW")

func openSelectionRead(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(selectionLongPath(path))
	if err != nil {
		return nil, err
	}
	// Inspection must not prevent another process replacing the complete
	// metadata file. The open handle still reads the old complete generation.
	var handle syscall.Handle
	for attempt := 0; attempt < 32; attempt++ {
		handle, err = syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if err != syscall.Errno(5) && err != syscall.Errno(32) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func lockSelectionFile(f *os.File, exclusive bool) error {
	flags := uintptr(1) // LOCKFILE_FAIL_IMMEDIATELY
	if exclusive {
		flags |= 2
	}
	var overlap syscall.Overlapped
	ok, _, err := selectionLock.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlap)))
	if ok != 0 {
		return nil
	}
	if err == syscall.Errno(33) || err == syscall.Errno(997) {
		return ErrSelectionBusy
	}
	return err
}

func unlockSelectionFile(f *os.File) error {
	var overlap syscall.Overlapped
	ok, _, err := selectionUnlock.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlap)))
	if ok != 0 {
		return nil
	}
	return err
}

func selectionLongPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return `\\?\` + path
}

func replaceSelectionFile(source, target string) error {
	from, err := syscall.UTF16PtrFromString(selectionLongPath(source))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(selectionLongPath(target))
	if err != nil {
		return err
	}
	// Same-directory rename only: replacement + write-through, never copy/delete.
	// A just-deleted journal name can remain delete-pending until an inspection
	// handle closes. Retry only Windows' transient sharing/access responses;
	// never replace via a copy/delete fallback or wait on a long-lived reader.
	var moveErr error
	for attempt := 0; attempt < 32; attempt++ {
		ok, _, err := selectionMove.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 9)
		if ok != 0 {
			return nil
		}
		moveErr = err
		if err != syscall.Errno(5) && err != syscall.Errno(32) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	return moveErr
}

func syncSelectionDirectory(string) error { return nil } // MoveFileExW uses WRITE_THROUGH.
