//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAuthRefreshCanceledWaiterRetainsLatePair(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{refreshStarted: make(chan struct{}), refreshRelease: make(chan struct{})}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("cancel.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	current, _ := manager.GetByID(auth.ID)
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := manager.EnsureRequestAuth(ctx, current, "")
		waiter <- err
	}()
	<-upstream.refreshStarted
	cancel()
	if err := <-waiter; err != context.Canceled {
		t.Fatalf("native waiter=%v", err)
	}
	other := make(chan error, 1)
	go func() {
		_, err := manager.EnsureRequestAuth(context.Background(), current, "")
		other <- err
	}()
	close(upstream.refreshRelease)
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	after, _ := manager.GetByID(auth.ID)
	raw, err := os.ReadFile(filepath.Join(native.cfg.AuthDir, auth.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if after.Metadata["refresh_token"] != "synthetic-refresh-rotated" || !strings.Contains(string(raw), "synthetic-refresh-rotated") {
		t.Fatal("late native pair was discarded")
	}
	if counters := nativeAuthRefreshNativeCounters(t, native.client); counters.Refresh != 1 || counters.ConsistencyErrors != 0 {
		t.Fatalf("native cancellation double rotation=%+v", counters)
	}
}
