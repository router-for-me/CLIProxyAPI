package proxypool

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// defaultProbe dials the health-check target through the configured proxy node.
// A successful TCP connection through the proxy is treated as a healthy node.
func defaultProbe(ctx context.Context, nodeURL, testURL string, timeout time.Duration) error {
	target, errTarget := probeTarget(testURL)
	if errTarget != nil {
		return errTarget
	}
	dialer, mode, errBuild := proxyutil.BuildDialer(nodeURL)
	if errBuild != nil {
		return fmt.Errorf("build proxy dialer: %w", errBuild)
	}
	if mode != proxyutil.ModeProxy || dialer == nil {
		return fmt.Errorf("proxy node is not a concrete proxy")
	}
	if timeout <= 0 {
		timeout = DefaultHealthCheckTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		conn, errDial := contextDialer.DialContext(dialCtx, "tcp", target)
		if errDial != nil {
			return fmt.Errorf("dial %s through proxy: %w", target, errDial)
		}
		return conn.Close()
	}

	result := make(chan error, 1)
	go func() {
		conn, errDial := dialer.Dial("tcp", target)
		if errDial != nil {
			result <- errDial
			return
		}
		result <- conn.Close()
	}()
	select {
	case <-dialCtx.Done():
		return dialCtx.Err()
	case errDial := <-result:
		if errDial != nil {
			return fmt.Errorf("dial %s through proxy: %w", target, errDial)
		}
		return nil
	}
}

func probeTarget(rawTestURL string) (string, error) {
	trimmed := strings.TrimSpace(rawTestURL)
	if trimmed == "" {
		trimmed = DefaultHealthCheckTestURL
	}
	parsed, errURL := url.Parse(trimmed)
	if errURL != nil {
		return "", fmt.Errorf("parse health-check test URL: %w", errURL)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("health-check test URL %q has no host", rawTestURL)
	}
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return "", fmt.Errorf("health-check test URL %q has no supported scheme", rawTestURL)
		}
	}
	return net.JoinHostPort(host, port), nil
}
