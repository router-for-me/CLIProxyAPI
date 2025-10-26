package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

// ensureClaudePythonBridge ensures a local claude agent sdk python bridge is ready.
// If CLAUDE_AGENT_SDK_URL is set, it will be used directly. Otherwise, it attempts
// to spawn the bundled python app with ANTHROPIC envs derived from config/auth.
// Returns bridge base URL (e.g., http://127.0.0.1:35331) or error.
func ensureClaudePythonBridge(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_AGENT_SDK_URL")); v != "" {
		// Validate env URL for scheme/host safety
		if err := validateLocalBridgeURL(v); err != nil {
			return "", err
		}
		return v, nil
	}

	// Check if auto-start is disabled
	if cfg == nil || !cfg.PythonAgent.Enabled {
		return "", errors.New("python agent bridge disabled in config")
	}

	// If already started (check context), return URL
	if existingURL := ctx.Value("python_bridge_url"); existingURL != nil {
		if url, ok := existingURL.(string); ok && url != "" {
			return url, nil
		}
	}

	// Derive target URL from config
	targetURL := strings.TrimSpace(cfg.PythonAgent.BaseURL)
	if targetURL == "" {
		targetURL = "http://127.0.0.1:35331"
	}

	if err := validateLocalBridgeURL(targetURL); err != nil {
		return "", err
	}

	// Check if bridge is already running (health check)
	if checkBridgeHealth(ctx, targetURL) {
		log.Infof("Python bridge already running at %s", targetURL)
		return targetURL, nil
	}

	// Prepare to spawn python process
	log.Infof("Starting Python Agent Bridge at %s...", targetURL)

	// Build environment variables from config
	envVars := os.Environ()
	if cfg.PythonAgent.Env != nil {
		for k, v := range cfg.PythonAgent.Env {
			envVars = append(envVars, k+"="+v)
		}
	}

	// Extract port from URL
	port := "35331" // default
	if u, err := url.Parse(targetURL); err == nil && u.Port() != "" {
		port = u.Port()
	}
	envVars = append(envVars, "PORT="+port)

	// Note: Actual process start is handled by service layer
	// This function validates config and returns target URL
	return targetURL, nil
}

// validateLocalBridgeURL ensures the bridge URL is http(s) and host is local-only by default.
// Allowed hosts: 127.0.0.1, localhost, ::1. To relax this, explicitly set
// CLAUDE_AGENT_SDK_ALLOW_REMOTE=true (primarily for controlled environments).
func validateLocalBridgeURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid CLAUDE Agent SDK URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme for CLAUDE Agent SDK URL: %s (use http/https)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	allowRemote := strings.EqualFold(strings.TrimSpace(os.Getenv("CLAUDE_AGENT_SDK_ALLOW_REMOTE")), "true")
	if allowRemote {
		return nil
	}
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	return fmt.Errorf("python agent bridge URL must be local-only unless CLAUDE_AGENT_SDK_ALLOW_REMOTE=true; got host=%s. To fix: set claude-agent-sdk-for-python.baseURL to http://127.0.0.1:35331 or export CLAUDE_AGENT_SDK_URL=http://127.0.0.1:35331", host)
}

// checkBridgeHealth performs a quick health check on the bridge URL
func checkBridgeHealth(ctx context.Context, baseURL string) bool {
	healthURL := strings.TrimSuffix(baseURL, "/") + "/healthz"
	req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	if proxyURL == "" {
		return nil
	}

	parsedURL, errParse := url.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}

	var transport *http.Transport

	// Handle different proxy schemes
	if parsedURL.Scheme == "socks5" {
		// Configure SOCKS5 proxy with optional authentication
		var proxyAuth *proxy.Auth
		if parsedURL.User != nil {
			username := parsedURL.User.Username()
			password, _ := parsedURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", parsedURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
		}
		// Set up a custom transport using the SOCKS5 dialer
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	} else if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		// Configure HTTP or HTTPS proxy
		transport = &http.Transport{Proxy: http.ProxyURL(parsedURL)}
	} else {
		log.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
		return nil
	}

	return transport
}

// StartPythonBridge starts the Python Agent SDK bridge process with config from cfg.
// Returns the running command and target URL, or error if startup fails.
func StartPythonBridge(ctx context.Context, cfg *config.Config) (*exec.Cmd, string, error) {
	if cfg == nil || !cfg.PythonAgent.Enabled {
		return nil, "", errors.New("python agent bridge disabled in config")
	}

	// Derive target URL
	targetURL := strings.TrimSpace(cfg.PythonAgent.BaseURL)
	if targetURL == "" {
		targetURL = "http://127.0.0.1:35331"
	}

	// Check if already running
	if checkBridgeHealth(ctx, targetURL) {
		log.Infof("Python bridge already running at %s", targetURL)
		return nil, targetURL, nil
	}

	// Find Python interpreter
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		pythonBin, err = exec.LookPath("python")
		if err != nil {
			return nil, "", fmt.Errorf("python interpreter not found in PATH: %w", err)
		}
	}

	// Locate app.py - try multiple paths
	appPaths := []string{
		"python/claude_agent_sdk_python/app.py",
		"./python/claude_agent_sdk_python/app.py",
		filepath.Join("python", "claude_agent_sdk_python", "app.py"),
	}

	var appPath string
	for _, p := range appPaths {
		if _, err := os.Stat(p); err == nil {
			appPath = p
			break
		}
	}

	if appPath == "" {
		return nil, "", errors.New("app.py not found in expected locations")
	}

	// Build environment variables
	envVars := os.Environ()
	if cfg.PythonAgent.Env != nil {
		for k, v := range cfg.PythonAgent.Env {
			envVars = append(envVars, k+"="+v)
		}
	}

	// Extract and set PORT
	port := "35331"
	if u, err := url.Parse(targetURL); err == nil && u.Port() != "" {
		port = u.Port()
	}
	envVars = append(envVars, "PORT="+port)

	// Create command with context
	cmd := exec.CommandContext(ctx, pythonBin, appPath)
	cmd.Env = envVars
	cmd.Dir = "." // Set working directory

	// Start the process
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("failed to start python bridge: %w", err)
	}

	log.Infof("Python bridge started (PID %d), waiting for health check...", cmd.Process.Pid)

	// Wait for bridge to become healthy (max 15 seconds)
	healthCtx, healthCancel := context.WithTimeout(ctx, 15*time.Second)
	defer healthCancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-healthCtx.Done():
			cmd.Process.Kill()
			return nil, "", fmt.Errorf("python bridge failed to start within 15s")
		case <-ticker.C:
			if checkBridgeHealth(healthCtx, targetURL) {
				log.Infof("Python bridge ready at %s (PID %d)", targetURL, cmd.Process.Pid)
				return cmd, targetURL, nil
			}
		}
	}
}

// StopPythonBridge gracefully stops the Python bridge process
func StopPythonBridge(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	log.Infof("Stopping Python bridge (PID %d)...", pid)

	// Try graceful shutdown first (SIGTERM)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		log.Warnf("Failed to send interrupt to Python bridge: %v", err)
	}

	// Wait up to 5 seconds for graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-time.After(5 * time.Second):
		log.Warnf("Python bridge did not stop gracefully, forcing kill...")
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill python bridge: %w", err)
		}
		<-done // Wait for Wait() to complete
	case err := <-done:
		if err != nil && err.Error() != "signal: interrupt" {
			log.Debugf("Python bridge exited with: %v", err)
		}
	}

	log.Infof("Python bridge stopped (PID %d)", pid)
	return nil
}
