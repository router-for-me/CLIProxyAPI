// Package helps provides utility functions for proxy configuration and Tor IP rotation.
package helps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// TorRotator manages Tor exit node IP rotation via the Tor control port.
type TorRotator struct {
	mu              sync.Mutex
	controlAddr     string
	proxyAddr       string
	controlPassword string
	retryAttempts   int   // 0 = unlimited
	retryCount      int   // current retry count
	retryOnCodes    []int // HTTP status codes that trigger rotation
}

// DefaultTorControlAddr is the default Tor control port address.
const DefaultTorControlAddr = "127.0.0.1:9051"

// DefaultTorProxyAddr is the default Tor SOCKS5 proxy address.
const DefaultTorProxyAddr = "127.0.0.1:9050"

// ErrTorControlNotConnected is returned when the Tor control connection fails.
var ErrTorControlNotConnected = errors.New("tor control connection failed")

// NewTorRotator creates a new TorRotator from the config.
func NewTorRotator(cfg *config.Config) *TorRotator {
	if cfg == nil {
		return nil
	}

	controlAddr := strings.TrimSpace(cfg.TorControlAddr)
	if controlAddr == "" {
		controlAddr = DefaultTorControlAddr
	}

	proxyAddr := strings.TrimSpace(cfg.TorProxyAddr)
	if proxyAddr == "" {
		proxyAddr = DefaultTorProxyAddr
	}

	codes := cfg.TorRetryOnCodes
	if codes == nil {
		codes = []int{429, 403, 500, 502, 503}
	}

	return &TorRotator{
		controlAddr:     controlAddr,
		proxyAddr:       proxyAddr,
		controlPassword: strings.TrimSpace(cfg.TorControlPassword),
		retryAttempts:   cfg.TorRetryAttempts,
		retryOnCodes:    codes,
	}
}

// ResetRetryCount resets the retry counter (call after a successful request).
func (r *TorRotator) ResetRetryCount() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryCount = 0
}

// ShouldRetryWithNewTorIP checks whether the given status code should trigger a
// Tor IP rotation + retry, and whether we still have retry attempts left.
func (r *TorRotator) ShouldRetryWithNewTorIP(statusCode int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if this status code should trigger rotation
	if !r.isRetryableCode(statusCode) {
		return false
	}

	// Check if we have retry attempts left
	if r.retryAttempts > 0 && r.retryCount >= r.retryAttempts {
		return false
	}

	return true
}

// RotateIP sends a NEWNYM signal to the Tor control port to request a new exit node IP.
// It waits for the new circuit to be established before returning.
func (r *TorRotator) RotateIP() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	conn, err := net.DialTimeout("tcp", r.controlAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTorControlNotConnected, err)
	}
	defer func() {
		_, _ = conn.Write([]byte("QUIT\r\n"))
		_ = conn.Close()
	}()

	// Set a deadline for the operation
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}
	// Note: Some Tor versions/configs don't send an initial PROTOCOLINFO greeting.
	// We skip reading it and go straight to AUTHENTICATE.
	buf := make([]byte, 512)
	var n int
	var resp string
	// Authenticate with the control port.
	// If a password is configured, send it quoted (Tor control protocol expects
	// AUTHENTICATE "password" for plaintext passwords).
	var authCmd string
	if r.controlPassword != "" {
		authCmd = fmt.Sprintf("AUTHENTICATE %q\r\n", r.controlPassword)
	} else {
		authCmd = "AUTHENTICATE\r\n"
	}
	if _, err := conn.Write([]byte(authCmd)); err != nil {
		return fmt.Errorf("send AUTHENTICATE: %w", err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read AUTHENTICATE response: %w", err)
	}
	resp = string(buf[:n])
	if !strings.HasPrefix(resp, "250") {
		return fmt.Errorf("AUTHENTICATE failed: %s", resp)
	}
	// Send NEWNYM signal to request a new identity
	if _, err := conn.Write([]byte("SIGNAL NEWNYM\r\n")); err != nil {
		return fmt.Errorf("send NEWNYM: %w", err)
	}
	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read NEWNYM response: %w", err)
	}
	resp = string(buf[:n])
	if !strings.HasPrefix(resp, "250") {
		return fmt.Errorf("NEWNYM failed: %s", resp)
	}

	r.retryCount++
	return nil
}

// NewTorHTTPTransport creates an HTTP transport that routes through the Tor SOCKS5 proxy.
func NewTorHTTPTransport(proxyAddr string) *http.Transport {
	addr := strings.TrimSpace(proxyAddr)
	if addr == "" {
		addr = DefaultTorProxyAddr
	}

	dialer, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
	if err != nil {
		return nil
	}

	transport := proxyutil.NewDirectTransport()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(network, addr)
	}

	return transport
}

// BuildTorDialer creates a SOCKS5 dialer for Tor connections.
func BuildTorDialer(proxyAddr string) (proxy.Dialer, error) {
	addr := strings.TrimSpace(proxyAddr)
	if addr == "" {
		addr = DefaultTorProxyAddr
	}
	return proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
}

// IsTorMode returns true if the config specifies Tor proxy mode.
func IsTorMode(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.ProxyMode), "tor")
}

// isRetryableCode checks if the given HTTP status code is in the retry list.
func (r *TorRotator) isRetryableCode(statusCode int) bool {
	for _, code := range r.retryOnCodes {
		if code == statusCode {
			return true
		}
	}
	return false
}
