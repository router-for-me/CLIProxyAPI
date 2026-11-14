package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPSubmissionGeminiApplyPatchTranslationDoesNotReplay(t *testing.T) {
	var submissions atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
			t.Error(errRead)
			return
		}
		submissions.Add(1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected upstream method=%s path=%s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":1}}}]},"finishReason":"STOP"}]}`)
	}))
	defer upstream.Close()
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewGeminiExecutor(&config.Config{}))
	model := "gemini-accepted-" + strings.ReplaceAll(t.Name(), "/", "-")
	for _, suffix := range []string{"a", "b"} {
		auth := &coreauth.Auth{ID: model + suffix, Provider: "gemini", Status: coreauth.StatusActive,
			ProxyURL: "direct", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic"}}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
	body := ojson.NewDocString(`{"model":"","input":"make a patch","stream":false,"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}]}`)
	if errSet := body.Set("model", model); errSet != nil {
		t.Fatal(errSet)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body.String()))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	t.Logf("accepted=%d status=%d retry=%q body=%s", submissions.Load(), response.Code, response.Header().Get("x-should-retry"), response.Body.String())
	if got := submissions.Load(); got != 1 {
		t.Errorf("accepted upstream POSTs=%d, want 1", got)
	}
	if response.Code != http.StatusBadGateway || ojson.Get(response.Body.String(), "error.code").String() != "upstream_outcome_unknown" || ojson.Get(response.Body.String(), "error.retryable").Raw != "false" || response.Header().Get("x-should-retry") != "false" {
		t.Fatal("local translation failure lost the nonretryable unknown outcome")
	}
}
