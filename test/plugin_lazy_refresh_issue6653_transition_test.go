//go:build cgo && (linux || darwin || freebsd)

package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6653TransitionNativePublicDiagnostics(t *testing.T) {
	dir := t.TempDir()
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", filepath.Join(dir, "qa-native-6653"+ext), ".")
	cmd.Dir = filepath.Join("..", "internal", "pluginhost", "testdata", "issue6653_native")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native build %v\n%s", err, out)
	}
	for _, kind := range []string{"overlap", "margin", "invalid_grant/400", "invalid_grant/0", "token_revoked/400", "token_revoked/0"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", kind, stream), func(t *testing.T) {
				var exchanges, dispatches atomic.Int32
				var m *coreauth.Manager
				id := "qa-public-transition"
				access, grant := "qa-old-access", "qa-old-refresh"
				if kind == "overlap" {
					access = "QA_OVERLAP"
					grant = "QA_OVERLAP_PRIVATE_SUFFIX"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/refresh" {
						dispatches.Add(1)
						t.Error("failed native refresh dispatched")
						return
					}
					exchanges.Add(1)
					code, status := "invalid_grant", 400
					if kind == "overlap" {
						code = grant
						status = 503
					} else if strings.Contains(kind, "/") {
						parts := strings.Split(kind, "/")
						code = parts[0]
						if parts[1] == "0" {
							status = 0
						}
						c, _ := m.GetByID(id)
						c.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
						if _, err := m.Update(context.Background(), c); err != nil {
							t.Error(err)
						}
					}
					d := ojson.NewDoc([]byte(`{}`))
					_ = d.Set("qa_error_code", code)
					_ = d.Set("qa_error_status", status)
					_ = d.Set("qa_error_message", grant+" denied")
					_, _ = w.Write(d.Bytes())
				}))
				defer upstream.Close()
				h := pluginhost.New()
				cfg := &config.Config{}
				cfg.Plugins.Enabled = true
				cfg.Plugins.Dir = dir
				enabled := true
				cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{"qa-native-6653": {Enabled: &enabled}}
				h.ApplyConfig(context.Background(), cfg)
				defer h.ShutdownAll()
				if !h.HasAuthProvider("qa-native-6653") {
					t.Fatal("native fixture not loaded")
				}
				m = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				h.SetAuthManager(m)
				reg := registry.GetGlobalRegistry()
				h.RegisterExecutors(m, reg)
				expiry := time.Now().Add(-time.Hour)
				if kind == "margin" {
					expiry = time.Now().Add(30 * time.Second)
				}
				d := ojson.NewDoc([]byte(`{}`))
				for k, v := range map[string]any{"id": id, "type": "qa-native-6653", "qa_upstream": upstream.URL, "access_token": access, "refresh_token": grant, "expires_at": expiry.Format(time.RFC3339Nano), "qa_policy": map[string]any{"mode": "lazy", "expiry_margin_seconds": 60}} {
					_ = d.Set(k, v)
				}
				a, handled, err := h.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qa-native-6653", RawJSON: d.Bytes()})
				if err != nil || !handled {
					t.Fatalf("native parse %v", err)
				}
				a, err = m.Register(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
				reg.RegisterClient(id, a.Provider, []*registry.ModelInfo{{ID: "qa-model-6653", Type: "openai"}})
				defer reg.UnregisterClient(id)
				base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, m)
				api := openaihandlers.NewOpenAIAPIHandler(base)
				router := gin.New()
				router.POST("/v1/chat/completions", api.ChatCompletions)
				server := httptest.NewServer(router)
				defer server.Close()
				body := fmt.Sprintf(`{"model":"qa-model-6653","stream":%v,"messages":[{"role":"user","content":"qa"}]}`, stream)
				for attempt := 0; attempt < 2; attempt++ {
					if attempt == 1 && kind == "margin" {
						current, _ := m.GetByID(id)
						if current.LastError == nil || current.RefreshError == nil {
							t.Fatal("margin terminal diagnostics missing")
						}
						current.Metadata["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
						if _, err := m.Update(context.Background(), current); err != nil {
							t.Fatal(err)
						}
						after, _ := m.GetByID(id)
						if after.LastError == nil || !after.RequiresSignIn {
							t.Fatal("explicit expiry setup cleared terminal diagnostic")
						}
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
					if resp.StatusCode < 400 {
						t.Fatalf("terminal/failed native refresh admitted: %d %s", resp.StatusCode, payload)
					}
					for _, fragment := range []string{access, grant, "PRIVATE_SUFFIX"} {
						if strings.Contains(string(payload), fragment) {
							t.Fatalf("public first/cached/later error retains credential fragment: %s", payload)
						}
					}
					c, _ := m.GetByID(id)
					for _, stored := range []*coreauth.Error{c.RefreshError, c.LastError} {
						if stored == nil {
							continue
						}
						for _, fragment := range []string{access, grant, "PRIVATE_SUFFIX"} {
							if strings.Contains(stored.Code+stored.Message, fragment) {
								t.Fatal("stored native diagnostic retains credential fragment")
							}
						}
					}
					if kind != "overlap" && (!c.RequiresSignIn || c.RejectedRefreshToken != grant) {
						t.Fatal("terminal native grant rejection lost")
					}
				}
				if exchanges.Load() != 1 || dispatches.Load() != 0 {
					t.Fatal("rejected grant reused or invalid credential dispatched")
				}
			})
		}
	}
}
