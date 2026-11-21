//go:build !unix

package api

import "net"

// listenUnixOwnerOnly creates the socket. Platforms without a umask (for
// example Windows) rely on the chmod that the caller applies afterwards.
func listenUnixOwnerOnly(path string) (net.Listener, error) {
	return net.Listen("unix", path)
}
