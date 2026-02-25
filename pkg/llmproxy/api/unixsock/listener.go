// Package unixsock provides Unix domain socket support for cliproxy++.
//
// Benefits over TCP:
//   - 10-50µs latency vs 100-500µs for TCP localhost
//   - No network stack overhead
//   - OS-level access control via file permissions
//   - Better for local microservices and agents
package unixsock

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	DefaultSocketPath = "/tmp/cliproxy.sock"
	DefaultPerm       = 0660 // rw-rw----
)

// Config holds Unix socket configuration
type Config struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Path      string `yaml:"path" json:"path"`
	Perm      int    `yaml:"perm" json:"perm"`
	RemoveOnStop bool `yaml:"remove_on_stop" json:"remove_on_stop"`
}

// DefaultConfig returns default Unix socket configuration
func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		Path:         DefaultSocketPath,
		Perm:         DefaultPerm,
		RemoveOnStop: true,
	}
}

// Listener wraps a Unix domain socket listener
type Listener struct {
	config  Config
	mu      sync.Mutex
	ln      net.Listener
	server  *http.Server
	running bool
}

// New creates a new Unix socket listener
func New(cfg Config) *Listener {
	if cfg.Path == "" {
		cfg.Path = DefaultSocketPath
	}
	if cfg.Perm == 0 {
		cfg.Perm = DefaultPerm
	}
	return &Listener{config: cfg}
}

// Serve starts the Unix socket HTTP server
func (l *Listener) Serve(handler http.Handler) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.config.Enabled {
		log.Info("Unix socket disabled, skipping")
		return nil
	}

	// Remove existing socket if stale
	if err := l.cleanup(); err != nil && !os.IsNotExist(err) {
		log.WithError(err).Warn("Failed to cleanup existing socket")
	}

	// Ensure directory exists
	dir := filepath.Dir(l.config.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Create Unix socket listener
	l.ln = &unixListener{
		addr: &net.UnixAddr{Name: l.config.Path, Net: "unix"},
	}

	// Actually create the socket
	syscall.Umask(0) // Use exact permissions
	ln, err := net.Listen("unix", l.config.Path)
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket: %w", err)
	}

	// Set permissions
	if err := os.Chmod(l.config.Path, os.FileMode(l.config.Perm)); err != nil {
		ln.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	l.ln = ln

	// Create HTTP server with optimized settings
	l.server = &http.Server{
		Handler:           handler,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	l.running = true
	log.WithField("path", l.config.Path).Info("Unix socket listener started")

	// Serve in background
	go func() {
		if err := l.server.Serve(l.ln); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("Unix socket server error")
		}
	}()

	return nil
}

// Stop gracefully stops the Unix socket listener
func (l *Listener) Stop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.running {
		return nil
	}

	var errs []error

	if l.server != nil {
		if err := l.server.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown error: %w", err))
		}
	}

	if l.ln != nil {
		if err := l.ln.Close(); err != nil {
			errs = append(errs, fmt.Errorf("listener close error: %w", err))
		}
	}

	if l.config.RemoveOnStop {
		if err := l.cleanup(); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("cleanup error: %w", err))
		}
	}

	l.running = false
	log.Info("Unix socket listener stopped")

	if len(errs) > 0 {
		return fmt.Errorf("errors during stop: %v", errs)
	}
	return nil
}

// cleanup removes the socket file
func (l *Listener) cleanup() error {
	return os.Remove(l.config.Path)
}

// Addr returns the socket address
func (l *Listener) Addr() net.Addr {
	if l.ln != nil {
		return l.ln.Addr()
	}
	return &net.UnixAddr{Name: l.config.Path, Net: "unix"}
}

// Path returns the socket file path
func (l *Listener) Path() string {
	return l.config.Path
}

// IsRunning returns whether the listener is active
func (l *Listener) IsRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// unixListener is a wrapper to satisfy net.Listener interface
type unixListener struct {
	net.Listener
	addr *net.UnixAddr
}

func (l *unixListener) Addr() net.Addr {
	return l.addr
}

// CheckSocket tests if a Unix socket is available
func CheckSocket(path string) bool {
	conn, err := net.DialTimeout("unix", path, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// GetOptimalTransport returns the best transport for local communication
func GetOptimalTransport() string {
	if CheckSocket(DefaultSocketPath) {
		return "unix"
	}
	return "http"
}
