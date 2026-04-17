package watcher

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestReloadLoggingConfigIfChanged(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}

	var callbacks int32
	w, err := NewWatcher(configPath, authDir, nil, func() {
		atomic.AddInt32(&callbacks, 1)
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer func() {
		_ = w.Stop()
	}()

	loggingPath := filepath.Join(tmpDir, "logging.ini")
	if err := os.WriteFile(loggingPath, []byte("[runtime]\nrotate_daily = true\n"), 0o644); err != nil {
		t.Fatalf("write logging.ini: %v", err)
	}

	w.reloadLoggingConfigIfChanged()
	if got := atomic.LoadInt32(&callbacks); got != 1 {
		t.Fatalf("expected first logging reload callback once, got %d", got)
	}

	w.reloadLoggingConfigIfChanged()
	if got := atomic.LoadInt32(&callbacks); got != 1 {
		t.Fatalf("expected unchanged logging config to be skipped, got %d", got)
	}

	if err := os.WriteFile(loggingPath, []byte("[runtime]\nrotate_daily = false\n"), 0o644); err != nil {
		t.Fatalf("rewrite logging.ini: %v", err)
	}
	w.reloadLoggingConfigIfChanged()
	if got := atomic.LoadInt32(&callbacks); got != 2 {
		t.Fatalf("expected changed logging config callback, got %d", got)
	}

	if err := os.Remove(loggingPath); err != nil {
		t.Fatalf("remove logging.ini: %v", err)
	}
	w.reloadLoggingConfigIfChanged()
	if got := atomic.LoadInt32(&callbacks); got != 3 {
		t.Fatalf("expected missing logging config callback, got %d", got)
	}
}
