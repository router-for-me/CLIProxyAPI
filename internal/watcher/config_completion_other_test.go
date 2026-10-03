//go:build !linux

package watcher

import "testing"

func TestNewConfigCompletionWatcherUsesFsnotifyFallback(t *testing.T) {
	completion, err := newConfigCompletionWatcher("config.yaml")
	if err != nil {
		t.Fatalf("create completion watcher: %v", err)
	}
	if completion != nil {
		t.Fatal("non-Linux completion watcher must be a nil interface")
	}
}
