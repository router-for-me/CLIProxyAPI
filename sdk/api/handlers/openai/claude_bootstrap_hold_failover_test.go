package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// Account a answers a streamed call with message_start, ping and then its own
// rate limit inside the 200. With the hold on (the default), nothing of
// a reached the caller, so the conductor serves the call from b and the caller
// only ever sees b's bytes.
func TestClaudeBootstrapHoldStreamFailsOverToSecondAccount(t *testing.T) {
	const accountA = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_account_a\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: ping\ndata: {\"type\": \"ping\"}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your account's rate limit. Please try again later.\"}}\n\n"
	const accountB = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_account_b\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"served-by-b\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	var submissionsA, submissionsB atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil || r.URL.Path != "/v1/messages" || ojson.GetBytesNoCopy(body, "stream").Type != ojson.True {
			t.Errorf("unexpected upstream request: path=%s err=%v body=%s", r.URL.Path, errRead, body)
			return
		}
		key := r.Header.Get("x-api-key") + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		events := accountB
		if strings.Contains(key, "synthetic-a") {
			submissionsA.Add(1)
			events = accountA
		} else {
			submissionsB.Add(1)
		}
		for _, event := range strings.SplitAfter(events, "\n\n") {
			_, _ = io.WriteString(w, event)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	model := "claude-bootstrap-hold-" + strings.ReplaceAll(t.Name(), "/", "-")
	for _, suffix := range []string{"a", "b"} {
		auth := &coreauth.Auth{
			ID: model + "-" + suffix, Provider: "claude", Status: coreauth.StatusActive,
			ProxyURL: "direct", Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic-" + suffix},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1/chat/completions", NewOpenAIAPIHandler(base).ChatCompletions)
	public := httptest.NewServer(router)
	defer public.Close()

	requestDoc := ojson.NewDoc([]byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":true}`))
	_ = requestDoc.Set("model", model)
	request, errRequest := http.NewRequest(http.MethodPost, public.URL+"/v1/chat/completions", strings.NewReader(requestDoc.String()))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	request.Header.Set("Content-Type", "application/json")
	client := public.Client()
	client.Timeout = 15 * time.Second
	response, errResponse := client.Do(request)
	if errResponse != nil {
		t.Fatal(errResponse)
	}
	defer func() { _ = response.Body.Close() }()
	body, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	t.Logf("status=%d a=%d b=%d body=%s", response.StatusCode, submissionsA.Load(), submissionsB.Load(), body)

	if submissionsA.Load() != 1 || submissionsB.Load() != 1 {
		t.Fatalf("upstream submissions a=%d b=%d, want one each: a fails over to b", submissionsA.Load(), submissionsB.Load())
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	text := string(body)
	if !strings.Contains(text, "served-by-b") || !strings.Contains(text, "msg_account_b") {
		t.Fatalf("body is missing account b's stream: %s", text)
	}
	for _, leaked := range []string{"msg_account_a", "rate_limit"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("caller saw %q from account a: %s", leaked, text)
		}
	}
}
