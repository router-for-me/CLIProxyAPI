package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func reserveLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close listener: %v", errClose)
	}
	return port
}

// TestServiceRun_OnBeforeStartConfigReachesLiveServer is the CPA-005
// regression: api.NewServer clones its config, so OnBeforeStart must run before
// construction for its documented configuration changes to take effect.
func TestServiceRun_OnBeforeStartConfigReachesLiveServer(t *testing.T) {
	// Hold the configured port so a server built from the pre-hook config
	// cannot start, making the test fail deterministically without the fix.
	occupied, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	defer func() { _ = occupied.Close() }()
	originalPort := occupied.Addr().(*net.TCPAddr).Port
	hookPort := reserveLoopbackPort(t)

	authDir := t.TempDir()
	cfgPath := filepath.Join(authDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("{}"), 0o644); errWrite != nil {
		t.Fatalf("failed to write config: %v", errWrite)
	}
	cfg := &config.Config{Host: "127.0.0.1", Port: originalPort, AuthDir: authDir}

	hookCalled := make(chan struct{}, 1)
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		WithHooks(Hooks{OnBeforeStart: func(c *config.Config) {
			c.Port = hookPort
			hookCalled <- struct{}{}
		}}).
		Build()
	if errBuild != nil {
		t.Fatalf("failed to build service: %v", errBuild)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- service.Run(runCtx) }()

	var conn net.Conn
	var errDial error
	for i := 0; i < 100; i++ {
		select {
		case errRun := <-runDone:
			t.Fatalf("service.Run exited before serving hook port %d: %v", hookPort, errRun)
		default:
		}
		conn, errDial = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", hookPort))
		if errDial == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if errDial != nil {
		t.Fatalf("live server did not adopt OnBeforeStart port %d: %v", hookPort, errDial)
	}
	_ = conn.Close()

	select {
	case <-hookCalled:
	default:
		t.Fatal("OnBeforeStart was not called")
	}
	if got := service.currentConfig().Port; got != hookPort {
		t.Fatalf("service config port = %d, want hook port %d", got, hookPort)
	}

	cancel()
	select {
	case errRun := <-runDone:
		if errRun != nil && !errors.Is(errRun, context.Canceled) {
			t.Fatalf("unexpected service.Run error: %v", errRun)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for service.Run to exit")
	}
}
