//go:build cgo && (linux || darwin || freebsd)

package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type issue6653PublicPendingStore struct {
	coreauth.Store
	fail atomic.Bool
}

func (s *issue6653PublicPendingStore) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
	if s.fail.Load() {
		return "", fmt.Errorf("qa disposable store unavailable")
	}
	return s.Store.Save(ctx, a)
}

func TestIssue6653PendingNativePublicScheduledSnapshot(t *testing.T) {
	pluginDir := t.TempDir()
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", filepath.Join(pluginDir, "qa-native-6653"+ext), ".")
	cmd.Dir = filepath.Join("..", "internal", "pluginhost", "testdata", "issue6653_native")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native build: %v\n%s", err, out)
	}
	for _, during := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("during=%v/stream=%v", during, stream), func(t *testing.T) {
				dir := t.TempDir()
				file := filepath.Join(dir, "qa-public-pending.json")
				var exchanges, dispatches atomic.Int32
				entered, release := make(chan struct{}), make(chan struct{})
				closeRelease := sync.OnceFunc(func() { close(release) })
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/refresh" {
						exchanges.Add(1)
						close(entered)
						if during {
							<-release
						}
						_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
						return
					}
					dispatches.Add(1)
					disk, err := os.ReadFile(file)
					if err != nil || ojson.GetBytes(disk, "refresh_token").String() != "qa-native-rotated-refresh" || r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
						t.Error("public dispatch preceded rotated save or used old token")
						w.WriteHeader(500)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"qa\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"qa\"}}]}\n\n")
					} else {
						_, _ = io.WriteString(w, `{"id":"qa","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"qa"}}]}`)
					}
				}))
				defer upstream.Close()
				defer closeRelease()
				h := pluginhost.New()
				cfg := &config.Config{}
				cfg.Plugins.Enabled = true
				cfg.Plugins.Dir = pluginDir
				enabled := true
				cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{"qa-native-6653": {Enabled: &enabled}}
				h.ApplyConfig(context.Background(), cfg)
				defer h.ShutdownAll()
				physical := sdkauth.NewFileTokenStore()
				physical.SetBaseDir(dir)
				store := &issue6653PublicPendingStore{Store: physical}
				m := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
				h.SetAuthManager(m)
				reg := registry.GetGlobalRegistry()
				h.RegisterExecutors(m, reg)
				d := ojson.NewDoc([]byte(`{}`))
				for k, v := range map[string]any{"id": "qa-public-pending", "type": "qa-native-6653", "qa_upstream": upstream.URL, "access_token": "qa-old-access", "refresh_token": "qa-old-refresh", "expires_at": time.Now().Add(30 * time.Second).Format(time.RFC3339Nano), "qa_policy": map[string]any{"mode": "lazy", "expiry_margin_seconds": 60}} {
					_ = d.Set(k, v)
				}
				a, handled, err := h.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qa-native-6653", FileName: "qa-public-pending.json", RawJSON: d.Bytes()})
				if err != nil || !handled {
					t.Fatalf("native parse %v", err)
				}
				a, err = m.Register(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
				reg.RegisterClient(a.ID, a.Provider, []*registry.ModelInfo{{ID: "qa-model-6653", Type: "openai"}})
				defer reg.UnregisterClient(a.ID)
				switchPolicy := func() {
					c, _ := m.GetByID(a.ID)
					c.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
					if _, err := m.Update(context.Background(), c); err != nil {
						t.Fatal(err)
					}
				}
				if during {
					done := make(chan error, 1)
					go func() { _, err := m.EnsureAuthFresh(context.Background(), a); done <- err }()
					<-entered
					switchPolicy()
					store.fail.Store(true)
					closeRelease()
					if err := <-done; err == nil {
						t.Fatal("initial save accepted")
					}
				} else {
					store.fail.Store(true)
					if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
						t.Fatal("initial save accepted")
					}
					store.fail.Store(false)
					switchPolicy()
					store.fail.Store(true)
				}
				base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, m)
				api := openaihandlers.NewOpenAIAPIHandler(base)
				router := gin.New()
				router.POST("/v1/chat/completions", api.ChatCompletions)
				server := httptest.NewServer(router)
				defer server.Close()
				body := fmt.Sprintf(`{"model":"qa-model-6653","stream":%v,"messages":[{"role":"user","content":"qa"}]}`, stream)
				for attempt := 0; attempt < 3; attempt++ {
					if attempt == 2 {
						store.fail.Store(false)
					}
					resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					payload, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := 503
					if attempt == 2 {
						want = 200
					}
					if resp.StatusCode != want {
						t.Fatalf("scheduled snapshot public status=%d want=%d payload=%s", resp.StatusCode, want, payload)
					}
					if attempt < 2 && dispatches.Load() != 0 {
						t.Fatal("unsaved pending pair reached public inference")
					}
				}
				if dispatches.Load() != 1 || exchanges.Load() != 1 {
					t.Fatal("public pending recovery reused grant or wrong dispatch count")
				}
			})
		}
	}
}
