//go:build windows

package main

import "os"

// openWithoutBlocking opens path for reading. Windows has no FIFOs in the file system.
func openWithoutBlocking(path string) (*os.File, error) {
	return os.Open(path)
}
