//go:build !windows

package main

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func flock(f *os.File, timeout time.Duration) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ticker.C:
			err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if err == nil {
				return nil
			} else if err == unix.EWOULDBLOCK {
				// File is locked, retry
			} else {
				return err
			}
		case <-timer.C:
			return errTimeout
		}
	}
}
