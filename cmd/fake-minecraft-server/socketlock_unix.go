//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// lockSocket locks a file next to the Unix socket at path (path.lock), so
// that two servers never share a socket: the second one fails to start,
// rather than replacing the socket of the first, or removing it once it
// stops. The lock lasts while the returned file is open, and ends with the
// process even if it crashes. The file is left in place: removing it would let
// two servers each lock a different file of the same name.
func lockSocket(path string) (io.Closer, error) {
	lockPath := path + ".lock"
	// A symlink there would make the server open or create another file.
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another server uses it (%s is locked)", lockPath)
		}
		return nil, fmt.Errorf("locking %s: %w", lockPath, err)
	}
	return f, nil
}
