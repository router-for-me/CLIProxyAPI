package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	management "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/tui"
	"gopkg.in/yaml.v3"
)

func TestStartServiceBackground_LocalManagementSurvivesConfigReload(t *testing.T) {
	port := reserveTCPPort(t)
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &config.Config{
		Port:                   port,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		RequestRetry:           1,
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	writeConfigForTest(t, configPath, cfg)

	password := "standalone-test-password"
	cancel, done := StartServiceBackground(cfg, configPath, password)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for background service shutdown")
		}
	})

	client := tui.NewClient(port, password)
	waitForCondition(t, 10*time.Second, func() error {
		_, err := client.GetConfig()
		return err
	})

	reloadedCfg := *cfg
	reloadedCfg.RequestRetry = 3
	writeConfigForTest(t, configPath, &reloadedCfg)

	waitForCondition(t, 10*time.Second, func() error {
		currentCfg, err := client.GetConfig()
		if err != nil {
			return err
		}
		if got := int(getNumber(currentCfg, "request-retry")); got != reloadedCfg.RequestRetry {
			return fmt.Errorf("request-retry = %d, want %d", got, reloadedCfg.RequestRetry)
		}
		return nil
	})

	for i := 0; i < 3; i++ {
		if _, err := client.GetConfig(); err != nil {
			t.Fatalf("management config request %d failed after reload: %v", i+1, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve tcp port: %v", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener addr type: %T", listener.Addr())
	}
	return addr.Port
}

func writeConfigForTest(t *testing.T, path string, cfg *config.Config) {
	t.Helper()

	payload, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := management.WriteConfig(path, payload); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %s: %v", timeout, lastErr)
}

func getNumber(m map[string]any, key string) float64 {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	value, ok := raw.(float64)
	if !ok {
		return 0
	}
	return value
}
