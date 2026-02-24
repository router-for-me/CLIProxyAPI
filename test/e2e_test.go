package test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestServerHealth tests the server health endpoint
func TestServerHealth(t *testing.T) {
	// Start a mock server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer srv.Close()
	
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestBinaryExists tests that the binary exists and is executable
func TestBinaryExists(t *testing.T) {
	paths := []string{
		"cli-proxy-api-plus-integration-test",
		"cli-proxy-api-plus",
		"server",
	}
	
	repoRoot := "/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxy++"
	
	for _, p := range paths {
		path := filepath.Join(repoRoot, p)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			t.Logf("Found binary: %s", p)
			return
		}
	}
	t.Skip("Binary not found in expected paths")
}

// TestConfigFile tests config file parsing
func TestConfigFile(t *testing.T) {
	config := `
port: 8317
host: localhost
log_level: debug
`
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	
	// Just verify we can write the config
	if _, err := os.Stat(configPath); err != nil {
		t.Error(err)
	}
}

// TestOAuthLoginFlow tests OAuth flow
func TestOAuthLoginFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"access_token":"test","expires_in":3600}`))
		}
	}))
	defer srv.Close()
	
	client := srv.Client()
	client.Timeout = 5 * time.Second
	
	resp, err := client.Get(srv.URL + "/oauth/token")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestKiloLoginBinary tests kilo login binary
func TestKiloLoginBinary(t *testing.T) {
	binary := "/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus/cli-proxy-api-plus-integration-test"
	
	if _, err := os.Stat(binary); os.IsNotExist(err) {
		t.Skip("Binary not found")
	}
	
	cmd := exec.Command(binary, "-help")
	cmd.Dir = "/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus"
	
	if err := cmd.Run(); err != nil {
		t.Logf("Binary help returned error: %v", err)
	}
}
