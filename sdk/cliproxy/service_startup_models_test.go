package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// When a client lists models the moment the proxy accepts connections, it sees
// the models of the credentials stored in the auth directory. Before the fix,
// the server listened before those models were registered, so early requests
// got "unknown provider for model".
func TestRunServesStoredCredentialModelsFromTheFirstConnection(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	// The prefix keeps this credential's model ids unique in the global registry.
	credential := `{"type":"claude","email":"startup@example.com","access_token":"fake","expired":"2099-01-01T00:00:00Z","prefix":"startuptest"}`
	if errWrite := os.WriteFile(filepath.Join(authDir, "claude-startup@example.com.json"), []byte(credential), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	port := freeStartupTestPort(t)
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`config-version: 8
server:
  host: "127.0.0.1"
  port: %d
access:
  api-keys:
    - "startup-test-key"
requests:
  proxy-url: "http://127.0.0.1:9"
oauth:
  auth-dir: %q
`, port, authDir)
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("load config: %v", errLoad)
	}
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	})

	body := firstModelsListing(t, port, done)

	if !strings.Contains(body, `"id":"startuptest/claude-opus-5"`) {
		t.Fatalf("first /v1/models listing lacks the stored credential's model startuptest/claude-opus-5:\n%s", body)
	}
}

// firstModelsListing sends GET /v1/models as soon as the port accepts a
// connection, the way a client already waiting on the proxy would.
func firstModelsListing(t *testing.T, port int, done <-chan error) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/models", port)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case errRun := <-done:
			t.Fatalf("service stopped before serving: %v", errRun)
		default:
		}
		req, errRequest := http.NewRequest(http.MethodGet, url, nil)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Authorization", "Bearer startup-test-key")
		resp, errDo := client.Do(req)
		if errDo != nil {
			var errOp *net.OpError
			if errors.As(errDo, &errOp) {
				continue
			}
			t.Fatalf("GET /v1/models: %v", errDo)
		}
		raw, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("close body: %v", errClose)
		}
		if errRead != nil {
			t.Fatalf("read /v1/models: %v", errRead)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/models status %d: %s", resp.StatusCode, raw)
		}
		return string(raw)
	}
	t.Fatal("proxy never accepted a connection")
	return ""
}

func freeStartupTestPort(t *testing.T) int {
	t.Helper()
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return port
}
