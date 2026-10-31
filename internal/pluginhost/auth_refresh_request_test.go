package pluginhost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAuthRefreshExpiredRequestRefreshesBeforeInference(t *testing.T) {
	var refreshes, inferences atomic.Int32
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id: "auth-refresh-request",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{
			identifier: "plugin-provider",
			refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				refreshes.Add(1)
				if req.Metadata["refresh_token"] != "refresh-old" {
					t.Errorf("refresh received wrong credential")
				}
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{
					"access_token": "access-new", "refresh_token": "refresh-new", "expired": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				}}}, nil
			},
		}}},
	})
	host := newHostWithRecords(record)
	dir := t.TempDir()
	host.runtimeConfig = &config.Config{AuthDir: dir}
	exec := &fakeExecutor{identifier: "plugin-provider", execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		inferences.Add(1)
		if req.AuthMetadata["access_token"] != "access-new" || req.AuthMetadata["refresh_token"] != "refresh-new" {
			t.Errorf("inference received stale credential")
		}
		var stored map[string]any
		if err := json.Unmarshal(req.StorageJSON, &stored); err != nil || stored["access_token"] != "access-new" || stored["refresh_token"] != "refresh-new" {
			t.Errorf("inference storage and metadata disagree")
		}
		return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)}, nil
	}}
	adapter := newExecutorAdapterForRecordForTest(host, record, exec, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	manager.RegisterExecutor(adapter)
	host.SetAuthManager(manager)
	var data pluginapi.AuthData
	if err := json.Unmarshal([]byte(`{"Provider":"plugin-provider","ID":"auth-refresh.json","RefreshPolicy":"on-demand"}`), &data); err != nil {
		t.Fatal(err)
	}
	data.Metadata = map[string]any{"access_token": "access-old", "refresh_token": "refresh-old", "expired": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	auth := host.AuthDataToCoreAuth(data, filepath.Join(dir, data.ID), data.ID)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	model := "auth-refresh-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	_, err := manager.Execute(context.Background(), []string{auth.Provider}, coreexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("expired opted-in request must refresh before selection rejection: %v", err)
	}
	if refreshes.Load() != 1 || inferences.Load() != 1 {
		t.Fatalf("refreshes=%d inferences=%d, want one each", refreshes.Load(), inferences.Load())
	}
}
