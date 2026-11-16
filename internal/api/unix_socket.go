package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// defaultUnixSocketMode is applied when server.unix-socket-mode is empty.
const defaultUnixSocketMode fs.FileMode = 0o600

// unixSocketPath returns the configured Unix socket path, or "" for TCP.
func unixSocketPath(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.UnixSocket)
}

// parseUnixSocketMode parses an octal file mode such as "0660".
func parseUnixSocketMode(raw string) (fs.FileMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultUnixSocketMode, nil
	}
	value, errParse := strconv.ParseUint(strings.TrimPrefix(raw, "0o"), 8, 32)
	if errParse != nil || value > 0o777 {
		return 0, fmt.Errorf("invalid unix-socket-mode %q: want an octal mode such as 0660", raw)
	}
	return fs.FileMode(value), nil
}

// removeStaleUnixSocket removes a socket file left behind by an earlier run.
// It never removes a file that is not a socket, and it refuses to remove a
// socket that still accepts connections.
func removeStaleUnixSocket(path string) error {
	info, errStat := os.Lstat(path)
	if errors.Is(errStat, fs.ErrNotExist) {
		return nil
	}
	if errStat != nil {
		return fmt.Errorf("failed to inspect unix socket path %s: %w", path, errStat)
	}
	if info.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("unix socket path %s exists and is not a socket; refusing to remove it", path)
	}
	if conn, errDial := net.Dial("unix", path); errDial == nil {
		if errClose := conn.Close(); errClose != nil {
			log.Debugf("failed to close probe connection to %s: %v", path, errClose)
		}
		return fmt.Errorf("unix socket %s is already in use", path)
	}
	if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove stale unix socket %s: %w", path, errRemove)
	}
	log.Infof("removed stale unix socket %s", path)
	return nil
}

// listenUnixSocket opens the API listener on a Unix domain socket.
// The socket is created while the process umask allows only the owner
// (see listenUnixOwnerOnly), then chmod sets the configured mode. So the
// socket is never reachable with wider permissions than configured.
// The returned listener removes the socket file when it is closed.
func listenUnixSocket(path, rawMode string) (net.Listener, error) {
	mode, errMode := parseUnixSocketMode(rawMode)
	if errMode != nil {
		return nil, errMode
	}
	if errStale := removeStaleUnixSocket(path); errStale != nil {
		return nil, errStale
	}
	listener, errListen := listenUnixOwnerOnly(path)
	if errListen != nil {
		return nil, errListen
	}
	if errChmod := os.Chmod(path, mode); errChmod != nil {
		if errClose := listener.Close(); errClose != nil {
			log.Errorf("failed to close unix socket listener after chmod failure: %v", errClose)
		}
		return nil, fmt.Errorf("failed to set mode %#o on unix socket %s: %w", mode, path, errChmod)
	}
	return listener, nil
}
