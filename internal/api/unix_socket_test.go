package api

import (
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
)

// shortSocketDir returns a short temporary directory, because socket paths
// are limited to about 104 bytes on macOS and t.TempDir can be longer.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, errDir := os.MkdirTemp("", "cpa-sock")
	if errDir != nil {
		t.Fatalf("MkdirTemp error = %v", errDir)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func unixSocketHTTPClient(socketPath string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}}
}

func doUnixRequest(t *testing.T, client *http.Client, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, errReq := http.NewRequest(http.MethodGet, "http://cliproxy"+path, nil)
	if errReq != nil {
		t.Fatalf("NewRequest error = %v", errReq)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("GET %s error = %v", path, errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("read body error = %v", errRead)
	}
	return resp.StatusCode, string(body)
}

func TestServerUnixSocketEndToEnd(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "api.sock")

	// Leave a stale socket file behind, like a crashed earlier run.
	stale, errStale := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if errStale != nil {
		t.Fatalf("stale listen error = %v", errStale)
	}
	stale.SetUnlinkOnClose(false)
	if errClose := stale.Close(); errClose != nil {
		t.Fatalf("stale close error = %v", errClose)
	}

	hash, errHash := bcrypt.GenerateFromPassword([]byte("mgmt-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg := &proxyconfig.Config{
		SDKConfig:      sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
		UnixSocket:     socketPath,
		UnixSocketMode: "0660",
	}
	cfg.RemoteManagement.SecretKey = string(hash)
	server := newTestServerWithConfig(t, cfg)
	server.engine.GET("/test-client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})

	startErr := make(chan error, 1)
	go func() { startErr <- server.Start() }()

	client := unixSocketHTTPClient(socketPath)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, errGet := client.Get("http://cliproxy/healthz")
		if errGet == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case errStart := <-startErr:
			t.Fatalf("Start() returned early: %v", errStart)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not serve on %s: %v", socketPath, errGet)
		}
		time.Sleep(10 * time.Millisecond)
	}

	info, errStat := os.Lstat(socketPath)
	if errStat != nil {
		t.Fatalf("Lstat error = %v", errStat)
	}
	if info.Mode().Type() != fs.ModeSocket {
		t.Fatalf("socket type = %v, want socket", info.Mode().Type())
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode = %#o, want 0660", perm)
	}

	// The same API auth applies on the socket.
	if code, _ := doUnixRequest(t, client, "/v1/models", nil); code != http.StatusUnauthorized {
		t.Fatalf("/v1/models without key status = %d, want 401", code)
	}
	if code, body := doUnixRequest(t, client, "/v1/models", map[string]string{"Authorization": "Bearer test-key"}); code != http.StatusOK {
		t.Fatalf("/v1/models with key status = %d, want 200: %s", code, body)
	}

	// A socket peer has no IP address, even with a spoofed forwarded header.
	if code, body := doUnixRequest(t, client, "/test-client-ip", map[string]string{"X-Forwarded-For": "127.0.0.1"}); code != http.StatusOK || body != "" {
		t.Fatalf("client IP = %q (status %d), want empty", body, code)
	}

	// A socket peer is not localhost: management needs allow-remote.
	code, body := doUnixRequest(t, client, "/v8/management/config", map[string]string{
		"Authorization":   "Bearer mgmt-secret",
		"X-Forwarded-For": "127.0.0.1",
	})
	if code != http.StatusForbidden || !strings.Contains(body, "remote management disabled") {
		t.Fatalf("management status = %d body = %s, want 403 remote management disabled", code, body)
	}

	if errStop := server.Stop(context.Background()); errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}
	select {
	case errStart := <-startErr:
		if errStart != nil {
			t.Fatalf("Start() error = %v", errStart)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after Stop()")
	}
	if _, errStat = os.Lstat(socketPath); !os.IsNotExist(errStat) {
		t.Fatalf("socket file still exists after Stop(): %v", errStat)
	}
}

func TestServerUnixSocketManagementWithAllowRemote(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "api.sock")
	hash, errHash := bcrypt.GenerateFromPassword([]byte("mgmt-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg := &proxyconfig.Config{UnixSocket: socketPath}
	cfg.RemoteManagement.SecretKey = string(hash)
	cfg.RemoteManagement.AllowRemote = true
	server := newTestServerWithConfig(t, cfg)
	if errWrite := os.WriteFile(server.configFilePath, []byte("server:\n  port: 8317\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	listener, errListen := listenUnixSocket(socketPath, "")
	if errListen != nil {
		t.Fatalf("listenUnixSocket error = %v", errListen)
	}
	go func() { _ = server.server.Serve(listener) }()
	t.Cleanup(func() { _ = server.server.Close() })

	info, errStat := os.Lstat(socketPath)
	if errStat != nil {
		t.Fatalf("Lstat error = %v", errStat)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("default socket mode = %#o, want 0600", perm)
	}

	client := unixSocketHTTPClient(socketPath)
	if code, body := doUnixRequest(t, client, "/v8/management/config", map[string]string{"Authorization": "Bearer mgmt-secret"}); code != http.StatusOK {
		t.Fatalf("management status = %d, want 200: %s", code, body)
	}
}

func TestListenUnixSocketRefusesNonSocketFile(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "api.sock")
	if errWrite := os.WriteFile(path, []byte("keep me"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	server := newTestServerWithConfig(t, &proxyconfig.Config{UnixSocket: path})
	errStart := server.Start()
	if errStart == nil || !strings.Contains(errStart.Error(), "not a socket") {
		t.Fatalf("Start() error = %v, want not a socket", errStart)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || string(data) != "keep me" {
		t.Fatalf("regular file changed: %q, %v", data, errRead)
	}
}

func TestListenUnixSocketRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "api.sock")
	live, errLive := net.Listen("unix", path)
	if errLive != nil {
		t.Fatal(errLive)
	}
	defer func() { _ = live.Close() }()

	if _, errListen := listenUnixSocket(path, ""); errListen == nil || !strings.Contains(errListen.Error(), "already in use") {
		t.Fatalf("listenUnixSocket error = %v, want already in use", errListen)
	}
	if _, errStat := os.Lstat(path); errStat != nil {
		t.Fatalf("live socket was removed: %v", errStat)
	}
}

func TestResolveRemoteIPUnixPeerIsNotLocal(t *testing.T) {
	for _, name := range []string{"", "@"} {
		if _, local := resolveRemoteIP(&net.UnixAddr{Name: name, Net: "unix"}); local {
			t.Fatalf("resolveRemoteIP(unix %q) treated as local", name)
		}
	}
}

func TestParseUnixSocketMode(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    fs.FileMode
		wantErr bool
	}{
		{"", 0o600, false},
		{"0660", 0o660, false},
		{"660", 0o660, false},
		{"0o666", 0o666, false},
		{" 0600 ", 0o600, false},
		{"0999", 0, true},
		{"01777", 0, true},
		{"rw-rw----", 0, true},
	} {
		got, err := parseUnixSocketMode(tc.raw)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Fatalf("parseUnixSocketMode(%q) = %#o, %v; want %#o, err=%t", tc.raw, got, err, tc.want, tc.wantErr)
		}
	}
}
