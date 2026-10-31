//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeAuthRefreshWatcherExactPendingCollision(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("native-auth-refresh-watcher.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	file := sdkauth.NewFileTokenStore()
	file.SetBaseDir(native.cfg.AuthDir)
	failure := errors.New("exact native pending save failure")
	store := &nativeFailingSave{FileTokenStore: file, err: failure}
	store.fail.Store(true)
	manager.SetStore(store)
	current, _ := manager.GetByID(auth.ID)
	if _, err := manager.EnsureRequestAuth(context.Background(), current, "native-auth-refresh-chat"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	credential["access_token"] = "synthetic-access-rotated"
	credential["refresh_token"] = "synthetic-refresh-rotated"
	credential["expired"] = nativeAuthRefreshFuture
	credential[coreauth.PluginRefreshPolicyStorageKey] = string(pluginapi.RefreshOnDemand)
	path := filepath.Join(native.cfg.AuthDir, auth.FileName)
	nativeAuthRefreshWriteJSON(t, path, credential)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, handled, err := native.host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: nativeAuthRefreshProvider, Path: path, FileName: auth.FileName, RawJSON: raw})
	if err != nil || !handled || observed == nil {
		t.Fatalf("native watcher parse=%v handled=%v", err, handled)
	}
	observed.BindFileSnapshot(path, raw)
	if !observed.HasFileSnapshot() || !observed.FileSnapshotMatchesDisk() {
		t.Fatal("native watcher was not bound to current exact pending file bytes")
	}
	if _, err := manager.Update(coreauth.WithSkipPersist(context.Background()), observed); err != nil {
		t.Fatal(err)
	}
	current, _ = manager.GetByID(auth.ID)
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"pending watcher"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	counters := nativeAuthRefreshNativeCounters(t, native.client)
	if resp.StatusCode == 200 || !strings.Contains(string(body), failure.Error()) || current.Metadata["access_token"] != "synthetic-access-expired" || counters.Refresh != 1 || len(upstream.snapshot()) != 1 {
		t.Fatalf("native watcher treated visible rotated bytes as a save receipt: status=%d body=%s current=%v counters=%+v requests=%+v", resp.StatusCode, body, current.Metadata, counters, upstream.snapshot())
	}
	store.fail.Store(false)
	saved, err := manager.ForceRefreshAuth(context.Background(), auth.ID)
	if err != nil || saved.Metadata["refresh_token"] != "synthetic-refresh-rotated" {
		t.Fatalf("native save-only recovery=%v", err)
	}
	counters = nativeAuthRefreshNativeCounters(t, native.client)
	if counters.Refresh != 1 || store.saves.Load() != 2 {
		t.Fatalf("native watcher retry rotated again: refresh=%d save=%d", counters.Refresh, store.saves.Load())
	}
	raw, err = os.ReadFile(path)
	var stored map[string]any
	if err != nil || json.Unmarshal(raw, &stored) != nil || stored["refresh_token"] != "synthetic-refresh-rotated" {
		t.Fatalf("native pending pair not physically saved: %v", err)
	}
	resp, err = server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"after save"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), "synthetic-native-ok") || len(upstream.snapshot()) != 2 {
		t.Fatalf("native saved positive control=%d %s error=%v", resp.StatusCode, body, err)
	}
	t.Log("native exact pending watcher bytes stayed non-executable; Save-only retry performed one successful physical write; one total native rotation")
}
