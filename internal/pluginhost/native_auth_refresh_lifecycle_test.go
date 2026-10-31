//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeAuthRefreshRestartProcess(t *testing.T) {
	root := os.Getenv("NATIVE_AUTH_REFRESH_RESTART_ROOT")
	if root == "" {
		t.Skip("restart subprocess only")
	}
	enabled := true
	cfg := &config.Config{AuthDir: filepath.Join(root, "auths"), Plugins: config.PluginsConfig{Enabled: true, Dir: filepath.Join(root, "plugins"), Configs: map[string]config.PluginInstanceConfig{nativeAuthRefreshProvider: {Enabled: &enabled}}}}
	host := New()
	host.ApplyConfig(context.Background(), cfg)
	defer host.ShutdownAll()
	sdkauth.RegisterPluginAuthParser(host)
	defer sdkauth.RegisterPluginAuthParser(nil)
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	manager := coreauth.NewManager(store, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(NewPluginRefreshCompatExecutor(runtimeexecutor.NewOpenAICompatExecutor(nativeAuthRefreshProvider, cfg), host, cfg))
	host.SetAuthManager(manager)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	auths := manager.List()
	if len(auths) != 1 || auths[0].RefreshPolicy != pluginapi.RefreshOnDemand || auths[0].Metadata["access_token"] != "synthetic-access-rotated" || auths[0].Metadata["refresh_token"] != "synthetic-refresh-rotated" {
		t.Fatal("fresh process did not recover managed pair")
	}
	manager = nativeAuthRefreshManager(t, cfg, host, auths[0])
	server := nativeAuthRefreshHandlerServer(t, cfg, manager, host)
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"restart"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "synthetic-native-ok") {
		t.Fatalf("restart inference=%d %s", resp.StatusCode, body)
	}
	host.mu.Lock()
	client := host.loaded[nativeAuthRefreshProvider].client
	host.mu.Unlock()
	if counters := nativeAuthRefreshNativeCounters(t, client); counters.Refresh != 0 || counters.ConsistencyErrors != 0 {
		t.Fatalf("restart unnecessarily rotated: %+v", counters)
	}
	t.Log("fresh process loaded rotated pair and served without refresh")
}

func TestNativeAuthRefreshFileBackedRestart(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("restart.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"before restart"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first demand=%d %s", resp.StatusCode, body)
	}
	server.Close()
	native.host.ShutdownAll()
	authPath := filepath.Join(native.cfg.AuthDir, auth.FileName)
	storedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(storedRaw, &stored); err != nil {
		t.Fatal(err)
	}
	delete(stored, "test_refresh_policy")
	nativeAuthRefreshWriteJSON(t, authPath, stored)
	before := len(upstream.snapshot())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestNativeAuthRefreshRestartProcess$", "-test.v")
	child.Env = append(os.Environ(), "NATIVE_AUTH_REFRESH_RESTART_ROOT="+native.root)
	output, err := child.CombinedOutput()
	t.Logf("fresh subprocess args=%v output=%s error=%v", child.Args, output, err)
	if err != nil {
		t.Fatal(err)
	}
	seen := upstream.snapshot()
	if len(seen) != before+1 || seen[len(seen)-1].Authorization != "Bearer synthetic-access-rotated" || seen[len(seen)-1].Path != "/v1/chat/completions" {
		t.Fatal("fresh subprocess did not serve exactly one rotated inference")
	}
	nativeAuthRefreshWriteJSON(t, filepath.Join(native.root, "restart-receipt.json"), map[string]any{"subprocess_args": child.Args, "subprocess_output": string(output), "upstream_requests": seen, "fresh_process": true, "refresh_count": 1})
}

func TestNativeAuthRefreshConcurrentDemandAndTransient(t *testing.T) {
	for _, mode := range []string{"success", "transient"} {
		t.Run(mode, func(t *testing.T) {
			native := nativeAuthRefreshBuildNativeHost(t)
			upstream := &nativeAuthRefreshLoopback{}
			provider := httptest.NewServer(upstream)
			defer provider.Close()
			credential := nativeAuthRefreshCredential("concurrent.json", "expired", provider.URL)
			credential["test_refresh_policy"] = "on-demand"
			if mode == "transient" {
				credential["refresh_mode"] = "transient"
			}
			auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
			manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
			server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
			const n = 12
			errors := make(chan string, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"concurrent"}]}`))
					if err != nil {
						errors <- err.Error()
						return
					}
					body, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					if mode == "success" {
						if resp.StatusCode != 200 {
							errors <- string(body)
						}
					} else if resp.StatusCode != 503 || !strings.Contains(string(body), "synthetic refresh unavailable") {
						errors <- string(body)
					}
				}()
			}
			wg.Wait()
			close(errors)
			for message := range errors {
				t.Error(message)
			}
			counters := nativeAuthRefreshNativeCounters(t, native.client)
			seen := upstream.snapshot()
			if counters.Refresh != 1 || counters.ConsistencyErrors != 0 {
				t.Fatalf("native concurrency=%+v", counters)
			}
			want := n + 1
			if mode == "transient" {
				want = 1
			}
			if len(seen) != want {
				t.Fatalf("upstream calls=%d want %d", len(seen), want)
			}
			t.Logf("mode=%s native_refresh=%d upstream_calls=%d", mode, counters.Refresh, len(seen))
		})
	}
}

func TestNativeAuthRefreshExpiredConsumers(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	for _, tc := range []struct{ name, path, body, marker string }{
		{"stream_chat", "/v1/chat/completions", `{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"stream"}],"stream":true}`, "synthetic-native-ok"},
		{"image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":false}`, "c3ludGhldGljLW5hdGl2ZS1pbWFnZQ=="},
		{"stream_image", "/v1/images/generations", `{"model":"native-auth-refresh-image","prompt":"synthetic","stream":true}`, "image_generation.completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &nativeAuthRefreshLoopback{}
			provider := httptest.NewServer(upstream)
			defer provider.Close()
			credential := nativeAuthRefreshCredential(tc.name+".json", "expired", provider.URL)
			credential["test_refresh_policy"] = "on-demand"
			auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
			manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
			server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
			before := nativeAuthRefreshNativeCounters(t, native.client)
			resp, err := server.Client().Post(server.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			after := nativeAuthRefreshNativeCounters(t, native.client)
			seen := upstream.snapshot()
			if resp.StatusCode != 200 || !strings.Contains(string(body), tc.marker) || after.Refresh-before.Refresh != 1 || len(seen) != 2 || seen[0].Path != "/oauth/token" || seen[1].Path != tc.path || seen[1].Authorization != "Bearer synthetic-access-rotated" {
				t.Fatalf("expired consumer=%d refresh=%d upstream=%+v body=%s", resp.StatusCode, after.Refresh-before.Refresh, seen, body)
			}
		})
	}
}
