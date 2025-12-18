package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

// newTestServerWithUnixSocket creates a test server configured with a Unix socket
func newTestServerWithUnixSocket(t *testing.T, socketPath string) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0, // Socket-only mode
		UnixSocket:             socketPath,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestServer_UnixSocket_CleanupStaleSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	// Create a stale socket file (just a regular file, not a real socket)
	if err := os.WriteFile(socketPath, []byte("stale"), 0644); err != nil {
		t.Fatalf("failed to create stale socket file: %v", err)
	}

	server := newTestServerWithUnixSocket(t, socketPath)

	// cleanupStaleSocket should remove the stale file
	err := server.cleanupStaleSocket(socketPath)
	if err != nil {
		t.Fatalf("cleanupStaleSocket failed: %v", err)
	}

	// Verify the file was removed
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("stale socket file was not removed")
	}
}

func TestServer_UnixSocket_CleanupStaleSocket_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	server := newTestServerWithUnixSocket(t, socketPath)

	// cleanupStaleSocket should succeed for non-existent file
	err := server.cleanupStaleSocket(socketPath)
	if err != nil {
		t.Fatalf("cleanupStaleSocket failed for non-existent file: %v", err)
	}
}

func TestServer_UnixSocket_CleanupStaleSocket_ActiveSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "active.sock")

	// Create an actual listening socket
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create test socket: %v", err)
	}
	defer listener.Close()

	server := newTestServerWithUnixSocket(t, socketPath)

	// cleanupStaleSocket should fail because socket is active
	err = server.cleanupStaleSocket(socketPath)
	if err == nil {
		t.Error("expected error for active socket, got nil")
	}
	if !strings.Contains(err.Error(), "socket already in use") {
		t.Errorf("expected 'socket already in use' error, got: %v", err)
	}
}

func TestServer_UnixSocket_StartAndStop(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "server.sock")

	server := newTestServerWithUnixSocket(t, socketPath)

	// Start the Unix socket listener
	err := server.startUnixSocket()
	if err != nil {
		t.Fatalf("startUnixSocket failed: %v", err)
	}

	// Verify socket file was created
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Error("socket file was not created")
	}

	// Verify we can connect to the socket
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("failed to connect to Unix socket: %v", err)
	}
	conn.Close()

	// Stop the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = server.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify socket file was cleaned up
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file was not cleaned up after Stop")
	}
}

func TestServer_UnixSocket_AutoCreateParentDirs(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a path with nested directories that don't exist
	socketPath := filepath.Join(tmpDir, "nested", "dirs", "server.sock")

	server := newTestServerWithUnixSocket(t, socketPath)

	// Start the Unix socket listener - should auto-create parent dirs
	err := server.startUnixSocket()
	if err != nil {
		t.Fatalf("startUnixSocket failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(ctx)
	}()

	// Verify socket file was created
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Error("socket file was not created")
	}

	// Verify parent directories were created
	parentDir := filepath.Dir(socketPath)
	if info, err := os.Stat(parentDir); err != nil || !info.IsDir() {
		t.Error("parent directories were not created")
	}
}

func TestServer_UnixSocket_HTTPRequest(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "http.sock")

	server := newTestServerWithUnixSocket(t, socketPath)

	// Start the Unix socket listener
	err := server.startUnixSocket()
	if err != nil {
		t.Fatalf("startUnixSocket failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Stop(ctx)
	}()

	// Give the server a moment to start serving
	time.Sleep(100 * time.Millisecond)

	// Create an HTTP client that connects via Unix socket
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	// Make a request to the models endpoint
	req, err := http.NewRequest(http.MethodGet, "http://localhost/v1/models", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request via Unix socket failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status code: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
