//go:build unix

package config_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
)

// configPipe creates a named pipe where the config file would be. A read or a
// write of it parks inside the file I/O until the test opens the other end.
func configPipe(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return path
}

// assertReadHoldsLock runs read, which must read the config at path, and checks
// that it holds the config file lock while it reads, so no write can interleave.
func assertReadHoldsLock(t *testing.T, path string, content []byte, read func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		read()
	}()
	// Opening the write end returns once the reader has opened the pipe, and the
	// reader then waits for data inside its read.
	pipe, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open pipe for writing: %v", err)
	}
	if config.ConfigFileMu.TryLock() {
		config.ConfigFileMu.Unlock()
		t.Error("the config is read without the config file lock")
	}
	if _, err = pipe.Write(content); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err = pipe.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	<-done
}

func managementContext(method, target string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(method, target, nil)
	return c
}

func TestConfigFileCallSitesHoldTheLock(t *testing.T) {
	t.Run("watcher reload", func(t *testing.T) {
		path := configPipe(t)
		w, err := watcher.NewWatcher(path, t.TempDir(), nil)
		if err != nil {
			t.Fatalf("NewWatcher: %v", err)
		}
		defer func() {
			if errStop := w.Stop(); errStop != nil {
				t.Errorf("stop watcher: %v", errStop)
			}
		}()
		// Empty bytes end the reload right after its read.
		assertReadHoldsLock(t, path, nil, w.ReloadConfigIfChanged)
	})
	t.Run("management GET config.yaml", func(t *testing.T) {
		path := configPipe(t)
		h := management.NewHandler(&config.Config{}, path, nil)
		assertReadHoldsLock(t, path, []byte("port: 8317\n"), func() {
			h.GetConfigYAML(managementContext(http.MethodGet, "/v0/management/config.yaml"))
		})
	})
	t.Run("management v8 config", func(t *testing.T) {
		path := configPipe(t)
		h := management.NewHandler(&config.Config{}, path, nil)
		assertReadHoldsLock(t, path, []byte("port: 8317\n"), func() {
			h.ConfigV8(managementContext(http.MethodGet, "/v8/management/config"))
		})
	})
	t.Run("management stale-snapshot check", func(t *testing.T) {
		path := configPipe(t)
		h := management.NewHandler(&config.Config{}, path, nil)
		assertReadHoldsLock(t, path, []byte("port: 8317\n"), func() {
			h.SetConfig(&config.Config{SourceHash: "applied"})
		})
	})
	t.Run("management write", func(t *testing.T) {
		path := configPipe(t)
		// More than a pipe buffer holds, so the writer stays inside its write until
		// the test drains the pipe.
		data := append(bytes.Repeat([]byte("# padding\n"), 1<<17), []byte("port: 8317\n")...)
		written := make(chan error, 1)
		go func() { written <- management.WriteConfig(path, data) }()
		pipe, err := os.Open(path)
		if err != nil {
			t.Fatalf("open pipe for reading: %v", err)
		}
		if config.ConfigFileMu.TryRLock() {
			config.ConfigFileMu.RUnlock()
			t.Error("the config is written without the config file lock")
		}
		if _, err = io.Copy(io.Discard, pipe); err != nil {
			t.Fatalf("drain pipe: %v", err)
		}
		if err = pipe.Close(); err != nil {
			t.Fatalf("close pipe: %v", err)
		}
		// A pipe cannot be synced, so the write reports an error after writing.
		<-written
	})
}
