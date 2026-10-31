//go:build cgo && (linux || darwin || freebsd)

package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAPICallNativeAuthRefresh(t *testing.T) {
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pluginDir := filepath.Join(root, "plugins", runtime.GOOS, runtime.GOARCH)
			authDir := filepath.Join(root, "auths")
			for _, dir := range []string{pluginDir, authDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			library := filepath.Join(pluginDir, "native-auth-refresh"+pluginhost.PluginExtension(runtime.GOOS))
			build := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
			build.Dir = filepath.Join("..", "..", "..", "pluginhost", "testdata", "native_auth_refresh")
			build.Env = append(os.Environ(), "CGO_ENABLED=1", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("native billing build=%v %s", err, output)
			}
			var rotations, inferences atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					rotations.Add(1)
					if mode == "failure" {
						w.WriteHeader(503)
						_, _ = io.WriteString(w, "synthetic native billing refresh unavailable")
						return
					}
					_, _ = io.WriteString(w, `{"access_token":"synthetic-access-rotated","refresh_token":"synthetic-refresh-rotated","expired":"2099-01-01T00:00:00Z"}`)
					return
				}
				inferences.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-access-rotated" {
					t.Error("native billing used old bearer")
				}
				_, _ = io.WriteString(w, `{"balance":1}`)
			}))
			defer upstream.Close()
			enabled := true
			cfg := &config.Config{AuthDir: authDir, Plugins: config.PluginsConfig{Enabled: true, Dir: filepath.Join(root, "plugins"), Configs: map[string]config.PluginInstanceConfig{"native-auth-refresh": {Enabled: &enabled}}}}
			host := pluginhost.New()
			host.ApplyConfig(context.Background(), cfg)
			defer host.ShutdownAll()
			if !host.HasAuthProvider("native-auth-refresh") {
				t.Fatal("native billing provider not loaded")
			}
			credential := map[string]any{"type": "native-auth-refresh", "id": "billing.json", "access_token": "synthetic-access-expired", "refresh_token": "synthetic-refresh-expired", "expired": "2000-01-01T00:00:00Z", "base_url": upstream.URL + "/v1", "refresh_url": upstream.URL + "/oauth/token", "test_refresh_policy": "on-demand"}
			raw, _ := json.Marshal(credential)
			authPath := filepath.Join(authDir, "billing.json")
			if err := os.WriteFile(authPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			auth, handled, err := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "native-auth-refresh", Path: authPath, FileName: "billing.json", RawJSON: raw})
			if err != nil || !handled || auth.RefreshPolicy != pluginapi.RefreshOnDemand {
				t.Fatalf("native parse=%v", err)
			}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(authDir)
			manager := coreauth.NewManager(store, nil, nil)
			manager.RegisterExecutor(pluginhost.NewPluginRefreshCompatExecutor(executor.NewOpenAICompatExecutor(auth.Provider, cfg), host, cfg))
			auth, _ = manager.Register(context.Background(), auth)
			host.SetAuthManager(manager)
			handler := NewHandler(cfg, "", manager)
			router := gin.New()
			router.POST("/billing", handler.APICall)
			payload, _ := json.Marshal(map[string]any{"auth_index": auth.Index, "method": "GET", "url": upstream.URL + "/billing", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", "/billing", strings.NewReader(string(payload))))
			if mode == "success" {
				if response.Code != 200 || inferences.Load() != 1 {
					t.Fatalf("native billing status=%d calls=%d body=%s", response.Code, inferences.Load(), response.Body.String())
				}
			} else if inferences.Load() != 0 || !strings.Contains(response.Body.String(), "synthetic native billing refresh unavailable") {
				t.Fatalf("native billing failure dispatched or masked: %s", response.Body.String())
			}
			if rotations.Load() != 1 {
				t.Fatalf("native billing rotations=%d", rotations.Load())
			}
		})
	}
}
