package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Explicit provider errors must stay distinguishable from incomplete responses.
func TestHTTPSubmissionClaudeAcceptedErrorAndMalformedStart(t *testing.T) {
	const messageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
	const content = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"
	const messageDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"
	const messageStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	const validTerminal = messageStart + content + messageDelta + messageStop
	for _, tc := range []struct {
		name     string
		body     string
		valid    bool
		failover bool
	}{
		{
			name:  "explicit_error_can_fail_over",
			body:  "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"synthetic explicit refusal\"}}\n\n",
			valid: true, failover: true,
		},
		{
			name: "message_start_missing_id",
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[]}}\n\n" + content + messageDelta + messageStop,
		},
		{
			name: "message_start_missing_model",
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" + content + messageDelta + messageStop,
		},
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
				attempt := submissions.Add(1)
				var request struct {
					Stream bool `json:"stream"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || json.Unmarshal(body, &request) != nil || !request.Stream {
					invalidRequests.Add(1)
					t.Errorf("wrong upstream path: method=%s path=%s body=%s", r.Method, r.URL.Path, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				responseBody := tc.body
				if tc.failover && attempt > 1 {
					responseBody = validTerminal
				}
				_, _ = io.WriteString(w, responseBody)
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
			requestBody := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":false}`, model)
			request, errRequest := http.NewRequest(http.MethodPost, public.URL+"/v1/chat/completions", strings.NewReader(requestBody))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			request.Header.Set("Content-Type", "application/json")
			client := public.Client()
			client.Timeout = 10 * time.Second
			response, errResponse := client.Do(request)
			if errResponse != nil {
				t.Fatal(errResponse)
			}
			defer response.Body.Close()
			body, errRead := io.ReadAll(response.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			t.Logf("accepted_submissions=%d public_status=%d x-should-retry=%q body=%s", submissions.Load(), response.StatusCode, response.Header.Get("x-should-retry"), body)
			wantSubmissions := int32(1)
			if tc.failover {
				wantSubmissions = 2
			}
			if got := submissions.Load(); got != wantSubmissions {
				t.Errorf("accepted upstream submissions=%d, want %d", got, wantSubmissions)
			}
			if invalidRequests.Load() != 0 {
				t.Fatal("probe did not reach the expected Claude upstream stream path")
			}
			if tc.valid {
				var completion struct {
					Object  string `json:"object"`
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
				}
				if response.StatusCode != http.StatusOK || json.Unmarshal(body, &completion) != nil || completion.Object != "chat.completion" || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "done" || completion.Choices[0].FinishReason != "stop" {
					t.Fatalf("valid terminal response changed: status=%d body=%s", response.StatusCode, body)
				}
				return
			}
			if response.StatusCode != http.StatusBadGateway {
				t.Errorf("status=%d, want 502", response.StatusCode)
			}
			var failure struct {
				Error struct {
					Code      string `json:"code"`
					Retryable *bool  `json:"retryable"`
				} `json:"error"`
			}
			if json.Unmarshal(body, &failure) != nil || failure.Error.Code != "upstream_outcome_unknown" || failure.Error.Retryable == nil || *failure.Error.Retryable {
				t.Errorf("missing nonretryable unknown outcome: %s", body)
			}
			if got := response.Header.Get("x-should-retry"); got != "false" {
				t.Errorf("x-should-retry=%q, want false", got)
			}
		})
	}
}
