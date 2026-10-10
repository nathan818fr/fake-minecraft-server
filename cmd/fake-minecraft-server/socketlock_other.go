//go:build !unix

package main

import "io"

// lockSocket does nothing where flock does not exist (Windows): servers
// sharing a Unix socket there can replace each other's.
func lockSocket(string) (io.Closer, error) {
	return noLock{}, nil
}

type noLock struct{}

func (noLock) Close() error { return nil }
