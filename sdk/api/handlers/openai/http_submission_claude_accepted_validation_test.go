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

// This file is self-contained so the same probe can run before carry 0599.
func TestHTTPSubmissionClaudeAcceptedValidationDoesNotReplay(t *testing.T) {
	const messageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
	const content = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	const messageDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"
	const messageStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{name: "empty", body: ""},
		{name: "malformed", body: "data: {\n\n"},
		{name: "missing_events", body: "event: ping\ndata: {\"type\":\"ping\"}\n\n"},
		{name: "missing_message_delta", body: messageStart + content},
		{name: "valid_terminal", body: messageStart + content + messageDelta + messageStop, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submissions, invalidRequests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
					return
				}
				// Count fully received requests, not executor invocations or TCP dials.
				submissions.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || !ojson.ValidBytes(body) || ojson.GetBytesNoCopy(body, "stream").Type != ojson.True {
					invalidRequests.Add(1)
					t.Errorf("wrong upstream path: method=%s path=%s body=%s", r.Method, r.URL.Path, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()

			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
			model := "claude-accepted-" + strings.ReplaceAll(t.Name(), "/", "-")
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
			requestDoc := ojson.NewDoc([]byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":false}`))
			_ = requestDoc.Set("model", model)
			request, errRequest := http.NewRequest(http.MethodPost, public.URL+"/v1/chat/completions", strings.NewReader(requestDoc.String()))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			request.Header.Set("Content-Type", "application/json")
			client := public.Client()
			response, errResponse := client.Do(request)
			if errResponse != nil {
				t.Fatal(errResponse)
			}
			defer func() {
				if errClose := response.Body.Close(); errClose != nil {
					t.Errorf("response body close: %v", errClose)
				}
			}()
			body, errRead := io.ReadAll(response.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			t.Logf("accepted_submissions=%d public_status=%d x-should-retry=%q body=%s", submissions.Load(), response.StatusCode, response.Header.Get("x-should-retry"), body)
			if got := submissions.Load(); got != 1 {
				t.Errorf("accepted upstream submissions=%d, want 1", got)
			}
			if invalidRequests.Load() != 0 {
				t.Fatal("probe did not reach the expected Claude upstream stream path")
			}
			if tc.valid {
				completion := ojson.ParseBytes(body)
				choices := completion.Get("choices")
				if response.StatusCode != http.StatusOK || !ojson.ValidBytes(body) || completion.Get("object").String() != "chat.completion" || !choices.IsArray() || len(choices.Array()) != 1 || choices.Get("0.message.content").String() != "done" || choices.Get("0.finish_reason").String() != "stop" {
					t.Fatalf("valid terminal response changed: status=%d body=%s", response.StatusCode, body)
				}
				return
			}
			if response.StatusCode != http.StatusBadGateway {
				t.Errorf("status=%d, want 502", response.StatusCode)
			}
			failure := ojson.ParseBytes(body)
			if !ojson.ValidBytes(body) || failure.Get("error.code").String() != "upstream_outcome_unknown" || failure.Get("error.retryable").Type != ojson.False {
				t.Errorf("missing nonretryable unknown outcome: %s", body)
			}
			if got := response.Header.Get("x-should-retry"); got != "false" {
				t.Errorf("x-should-retry=%q, want false", got)
			}
		})
	}
}
