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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestServiceShutdown_ViolentShutdownImmediatelyClosesServer(t *testing.T) {
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close listener: %v", errClose)
	}

	cfg := &config.Config{
		Host: "127.0.0.1",
		Port: port,
	}

	server := api.NewServer(cfg, nil, nil, "")
	go func() {
		_ = server.Start()
	}()

	// Wait briefly for server to bind.
	var conn net.Conn
	var errDial error
	for i := 0; i < 20; i++ {
		conn, errDial = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if errDial == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if errDial != nil {
		t.Fatalf("server failed to start listening: %v", errDial)
	}
	defer func() {
		if conn != nil {
			if errCloseConn := conn.Close(); errCloseConn != nil {
				t.Logf("connection close error: %v", errCloseConn)
			}
		}
	}()

	service := &Service{
		server: server,
		cfg:    cfg,
	}

	stopStart := time.Now()
	errShutdown := service.Shutdown(context.Background())
	stopDuration := time.Since(stopStart)

	if stopDuration >= time.Second {
		t.Fatalf("service.Shutdown took %v, want immediate violent shutdown (<1s)", stopDuration)
	}
	if errShutdown != nil {
		t.Fatalf("service.Shutdown error = %v, want nil", errShutdown)
	}

	// Verify server port is closed.
	conn2, errDial2 := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if errDial2 == nil {
		_ = conn2.Close()
		t.Fatal("expected server to be closed, but dial succeeded")
	}
}

func TestServiceRun_ShutdownContextFreshAtExit(t *testing.T) {
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close listener: %v", errClose)
	}

	authDir := t.TempDir()
	cfg := &config.Config{
		Host:    "127.0.0.1",
		Port:    port,
		AuthDir: authDir,
	}

	cfgPath := filepath.Join(authDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("{}"), 0o644); errWrite != nil {
		t.Fatalf("failed to write dummy config: %v", errWrite)
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		Build()
	if errBuild != nil {
		t.Fatalf("failed to build service: %v", errBuild)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	runDone := make(chan error, 1)
	go func() {
		runDone <- service.Run(runCtx)
	}()

	// Wait for server to accept connections.
	var conn net.Conn
	var errDial error
	for i := 0; i < 50; i++ {
		conn, errDial = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if errDial == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if errDial != nil {
		cancel()
		t.Fatalf("failed to dial service server: %v", errDial)
	}
	if errCloseConn := conn.Close(); errCloseConn != nil {
		t.Logf("connection close error: %v", errCloseConn)
	}

	// Trigger stop.
	stopStart := time.Now()
	cancel()

	select {
	case errRun := <-runDone:
		stopDuration := time.Since(stopStart)
		if stopDuration >= 5*time.Second {
			t.Fatalf("service.Run stop took %v, want fast shutdown", stopDuration)
		}
		if errRun != nil && !errors.Is(errRun, context.Canceled) {
			t.Fatalf("unexpected service.Run error: %v", errRun)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for service.Run to exit")
	}

	// Verify server port is closed.
	connClosed, errDialClosed := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if errDialClosed == nil {
		_ = connClosed.Close()
		t.Fatal("expected server to be closed after Run exit, but dial succeeded")
	}
}
