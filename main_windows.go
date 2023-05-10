//go:build windows

package main

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func flock(f *os.File, timeout time.Duration) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	handle := windows.Handle(f.Fd())

	for {
		select {
		case <-ticker.C:
			err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, nil)
			if err == nil {
				return nil
			} else if err == windows.ERROR_LOCK_VIOLATION {
				// File is locked, retry
			} else {
				return err
			}
		case <-timer.C:
			return errTimeout
		}
	}
}
