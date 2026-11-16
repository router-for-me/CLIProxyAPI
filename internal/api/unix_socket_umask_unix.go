//go:build unix

package api

import (
	"net"
	"sync"
	"syscall"
)

var unixSocketUmaskMu sync.Mutex

// listenUnixOwnerOnly creates the socket with umask 0077, so the new socket
// file is accessible by the owner only until the caller applies the
// configured mode. The umask is process-wide, so files created by other
// goroutines in this short window also get owner-only permissions; the
// owner keeps full access, so this is safe.
func listenUnixOwnerOnly(path string) (net.Listener, error) {
	unixSocketUmaskMu.Lock()
	defer unixSocketUmaskMu.Unlock()
	oldMask := syscall.Umask(0o077)
	defer syscall.Umask(oldMask)
	return net.Listen("unix", path)
}
