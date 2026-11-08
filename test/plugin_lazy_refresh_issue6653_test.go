//go:build cgo && (linux || darwin || freebsd)

package test

import (
	"context"
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
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6653NativePublicHTTPHandlers(t *testing.T) {
	dir := t.TempDir()
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", filepath.Join(dir, "qa-native-6653"+ext), ".")
	cmd.Dir = filepath.Join("..", "internal", "pluginhost", "testdata", "issue6653_native")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native build %v\n%s", err, output)
	}
	for _, operation := range []string{"chat", "sse", "image", "count", "refresh-failure", "secret-buffered", "secret-prestream", "retry-buffered", "retry-prestream"} {
		t.Run(operation, func(t *testing.T) {
			var refreshes, inferences atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/refresh" {
					refreshes.Add(1)
					if operation == "retry-buffered" || operation == "retry-prestream" {
						d := ojson.NewDoc([]byte(`{}`))
						_ = d.Set("qa_error_code", "qa_retry_failure")
						_ = d.Set("qa_error_status", 503)
						_ = d.Set("qa_retry_after", 7)
						_ = d.Set("qa_error_message", strings.Repeat("界", 290)+" qa-old-refresh renewal unavailable")
						_, _ = w.Write(d.Bytes())
						return
					}
					if operation == "secret-buffered" || operation == "secret-prestream" {
						w.WriteHeader(503)
						_, _ = io.WriteString(w, "qa-old-access qa-old-refresh Bearer QA_BEARER_SECRET https://example.invalid/?secret=QA_URL_SECRET code=QA_CODE_SECRET")
						return
					}
					if operation == "refresh-failure" {
						w.WriteHeader(503)
						_, _ = io.WriteString(w, "qa-http-refresh-failure")
						return
					}
					_, _ = io.WriteString(w, `{"access_token":"qa-native-rotated-access","refresh_token":"qa-native-rotated-refresh"}`)
					return
				}
				inferences.Add(1)
				if r.Header.Get("Authorization") != "Bearer qa-native-rotated-access" {
					t.Error("public handler sent stale native token")
					w.WriteHeader(401)
					return
				}
				if operation == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"qa\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"qa\"}}]}\n\n")
					return
				}
				if operation == "count" {
					_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":7}}`)
					return
				}
				if operation == "image" {
					_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"cWE="}]}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"qa","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"qa"}}]}`)
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
				t.Fatal("native fixture not loaded through production discovery")
			}
			m := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			h.SetAuthManager(m)
			reg := registry.GetGlobalRegistry()
			h.RegisterExecutors(m, reg)
			if _, ok := m.Executor("qa-native-6653"); !ok {
				t.Fatal("native executor not registered")
			}
			id := "qa-public-" + operation
			d := ojson.NewDoc([]byte(`{}`))
			for key, value := range map[string]any{"id": id, "type": "qa-native-6653", "qa_upstream": upstream.URL, "access_token": "qa-old-access", "refresh_token": "qa-old-refresh", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano), "qa_policy": map[string]any{"mode": "lazy", "expiry_margin_seconds": 60}} {
				_ = d.Set(key, value)
			}
			a, handled, err := h.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "qa-native-6653", RawJSON: d.Bytes()})
			if err != nil || !handled {
				t.Fatalf("native parse %v", err)
			}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			model := "qa-model-6653"
			if operation == "image" {
				model = "qa-image-6653"
			}
			modelType := "openai"
			if operation == "image" {
				modelType = registry.OpenAIImageModelType
			}
			reg.RegisterClient(id, a.Provider, []*registry.ModelInfo{{ID: model, Type: modelType}})
			defer reg.UnregisterClient(id)
			base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, m)
			openai := openaihandlers.NewOpenAIAPIHandler(base)
			claude := claudehandlers.NewClaudeCodeAPIHandler(base)
			router := gin.New()
			router.POST("/v1/chat/completions", openai.ChatCompletions)
			router.POST("/v1/images/generations", openai.ImagesGenerations)
			router.POST("/v1/messages/count_tokens", claude.ClaudeCountTokens)
			server := httptest.NewServer(router)
			defer server.Close()
			path := "/v1/chat/completions"
			body := `{"model":"qa-model-6653","messages":[{"role":"user","content":"qa"}]}`
			if operation == "sse" || operation == "secret-prestream" || operation == "retry-prestream" {
				body = `{"model":"qa-model-6653","stream":true,"messages":[{"role":"user","content":"qa"}]}`
			}
			if operation == "image" {
				path = "/v1/images/generations"
				body = `{"model":"qa-image-6653","prompt":"qa"}`
			}
			if operation == "count" {
				path = "/v1/messages/count_tokens"
			}
			resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			payload, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantInference := 200, int32(1)
			if operation == "retry-buffered" || operation == "retry-prestream" {
				wantStatus = 503
				wantInference = 0
				if resp.Header.Get("Retry-After") != "7" || ojson.GetBytes(payload, "error.code").String() != "internal_server_error" {
					t.Errorf("first public retry status/code/header lost: header=%v body=%s", resp.Header, payload)
				}
				cached, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				cachedPayload, err := io.ReadAll(cached.Body)
				cached.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				for _, body := range [][]byte{payload, cachedPayload} {
					if strings.Contains(string(body), "qa-old-refresh") || strings.Contains(string(body), "qa-old-r") {
						t.Fatalf("first/cached public truncation leaked dummy grant fragment: %s", body)
					}
				}
				if cached.StatusCode != 503 || cached.Header.Get("Retry-After") != "7" || ojson.GetBytes(cachedPayload, "error.code").String() != "internal_server_error" || refreshes.Load() != 1 || inferences.Load() != 0 {
					t.Fatalf("cached public retry status/code/header/cache lost: status=%d header=%v body=%s refresh=%d inference=%d", cached.StatusCode, cached.Header, cachedPayload, refreshes.Load(), inferences.Load())
				}
			}
			if operation == "secret-buffered" || operation == "secret-prestream" {
				wantStatus = 503
				wantInference = 0
				for _, marker := range []string{"qa-old-access", "qa-old-refresh", "QA_BEARER_SECRET", "QA_URL_SECRET", "QA_CODE_SECRET"} {
					if strings.Contains(string(payload), marker) {
						t.Errorf("dummy secret reached public error: %s", payload)
					}
				}
			}
			if operation == "refresh-failure" {
				wantStatus = 503
				wantInference = 0
				if !strings.Contains(string(payload), "qa-http-refresh-failure") {
					t.Errorf("actual native refresh failure lost: %s", payload)
				}
			}
			if resp.StatusCode != wantStatus || refreshes.Load() != 1 || inferences.Load() != wantInference {
				t.Fatalf("public %s status=%d want=%d refresh=%d inference=%d body=%s", operation, resp.StatusCode, wantStatus, refreshes.Load(), inferences.Load(), payload)
			}
			t.Logf("production native discovery + %s: status=%d refresh=1 inference=%d", path, resp.StatusCode, inferences.Load())
		})
	}
}
