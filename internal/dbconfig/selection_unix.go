//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package dbconfig

import (
	"errors"
	"os"
	"syscall"
)

func openSelectionRead(path string) (*os.File, error) { return os.Open(path) }

func lockSelectionFile(f *os.File, exclusive bool) error {
	flag := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		flag = syscall.LOCK_EX | syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), flag)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrSelectionBusy
	}
	return err
}
func unlockSelectionFile(f *os.File) error             { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func replaceSelectionFile(source, target string) error { return os.Rename(source, target) }
func syncSelectionDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
