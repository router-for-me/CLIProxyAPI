package cmd

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestStartServiceBackgroundWithPluginHostAppliesHostOptions(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen for test port: %v", errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("close test listener: %v", errClose)
	}

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{APIKeys: []string{"your-api-key-1"}},
		Host:      "127.0.0.1",
		Port:      port,
		AuthDir:   filepath.Join(t.TempDir(), "auths"),
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("marshal config: %v", errMarshal)
	}
	if errWrite := os.WriteFile(configPath, configYAML, 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cancel, done := StartServiceBackgroundWithPluginHost(cfg, configPath, "", nil, HostOptions{
		RuntimeContractVersion: "1",
		EphemeralAPIKey:        "runtime-secret",
	})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("background service did not stop")
		}
	})

	client := &http.Client{Timeout: time.Second}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	waitForBackgroundServer(t, client, baseURL+"/v1/models", "runtime-secret")

	for _, tt := range []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "runtime key", key: "runtime-secret", wantStatus: http.StatusOK},
		{name: "example config key", key: "your-api-key-1", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", key: "wrong-secret", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/v1/models", nil)
			if errRequest != nil {
				t.Fatalf("create request: %v", errRequest)
			}
			request.Header.Set("Authorization", "Bearer "+tt.key)
			response, errDo := client.Do(request)
			if errDo != nil {
				t.Fatalf("request background service: %v", errDo)
			}
			defer response.Body.Close()
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.wantStatus)
			}
		})
	}
}

func waitForBackgroundServer(t *testing.T, client *http.Client, url, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if errRequest != nil {
			t.Fatalf("create readiness request: %v", errRequest)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		response, errDo := client.Do(request)
		if errDo == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background service did not become ready")
}
