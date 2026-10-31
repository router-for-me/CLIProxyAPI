//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type nativeFailingSave struct {
	*sdkauth.FileTokenStore
	fail  atomic.Bool
	saves atomic.Int32
	err   error
}

func (s *nativeFailingSave) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	s.saves.Add(1)
	if s.fail.Load() {
		return "", s.err
	}
	return s.FileTokenStore.Save(ctx, auth)
}

func TestNativeAuthRefreshSaveFailureRetainsPair(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("save.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	file := sdkauth.NewFileTokenStore()
	file.SetBaseDir(native.cfg.AuthDir)
	failure := errors.New("synthetic native save failure")
	store := &nativeFailingSave{FileTokenStore: file, err: failure}
	store.fail.Store(true)
	manager.SetStore(store)
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"save failure"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	seen := upstream.snapshot()
	current, _ := manager.GetByID(auth.ID)
	if resp.StatusCode == 200 || !strings.Contains(string(body), failure.Error()) || len(seen) != 1 || current.Metadata["access_token"] != "synthetic-access-expired" {
		t.Fatalf("unsaved native pair executed: %d %s %+v", resp.StatusCode, body, seen)
	}
	store.fail.Store(false)
	saved, err := manager.ForceRefreshAuth(context.Background(), auth.ID)
	if err != nil || saved.Metadata["refresh_token"] != "synthetic-refresh-rotated" {
		t.Fatalf("native save-only retry=%v", err)
	}
	if counters := nativeAuthRefreshNativeCounters(t, native.client); counters.Refresh != 1 || store.saves.Load() != 2 {
		t.Fatalf("old native refresh token consumed twice: refresh=%d saves=%d", counters.Refresh, store.saves.Load())
	}
	raw, err := os.ReadFile(filepath.Join(native.cfg.AuthDir, auth.FileName))
	if err != nil || !strings.Contains(string(raw), "synthetic-refresh-rotated") {
		t.Fatal("pending native pair not saved")
	}
	resp, err = server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"saved"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "synthetic-native-ok") || len(upstream.snapshot()) != 2 {
		t.Fatal("saved native pair not executable")
	}
}
