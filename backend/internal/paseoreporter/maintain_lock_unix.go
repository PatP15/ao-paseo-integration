//go:build unix

package paseoreporter

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockMaintenanceFile(path string) (func(), error) {
	lock, err := os.OpenFile(path+".ao-lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	}, nil
}
