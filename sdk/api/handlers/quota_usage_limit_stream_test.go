package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// These tests cover a quota exhaustion after the response is committed (200,
// text/event-stream): the usage limit travels in the terminal error event.

const quotaStreamProvider = "quota-usage-limit-stream-fake"

type streamSurface struct {
	quotaSurface
	codexClient bool
}

var streamSurfaces = []streamSurface{
	{quotaSurface: quotaSurfaces[0]},
	{quotaSurface: quotaSurface{name: "responses-codex", path: "/v1/responses", body: quotaSurfaces[0].body}, codexClient: true},
	{quotaSurface: quotaSurfaces[1]},
	{quotaSurface: quotaSurfaces[2]},
}

// partialFrame is output the client receives before the failure.
func partialFrame(format string) []byte {
	switch format {
	case "openai":
		return []byte(`{"id":"chatcmpl-partial","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`)
	case "claude":
		return []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	default:
		return []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_partial\",\"status\":\"in_progress\"}}\n\n")
	}
}

// quotaStreamExecutor streams partial output, then fails with err.
type quotaStreamExecutor struct{ err error }

func (quotaStreamExecutor) Identifier() string { return quotaStreamProvider }

func (quotaStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e quotaStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: partialFrame(opts.SourceFormat.String())}
	chunks <- coreexecutor.StreamChunk{Err: e.err}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (quotaStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (quotaStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (quotaStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// doCommittedStream runs one streaming request against a single credential
// whose stream fails with err after partial output.
func doCommittedStream(t *testing.T, mode string, surface streamSurface, err error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	coreauth.SetQuotaCooldownResponseMode(mode)
	t.Cleanup(func() { coreauth.SetQuotaCooldownResponseMode("") })
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(quotaStreamExecutor{err: err})
	model := "quota-stream-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	authID := model + "-auth"
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: quotaStreamProvider, Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, quotaStreamProvider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
	router.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
	router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	body := ojson.NewDocString(surface.body)
	for path, value := range map[string]any{"model": model, "stream": true} {
		if errSet := body.Set(path, value); errSet != nil {
			t.Fatal(errSet)
		}
	}
	request := httptest.NewRequest(http.MethodPost, surface.path, strings.NewReader(body.String()))
	request.Header.Set("Content-Type", "application/json")
	if surface.codexClient {
		request.Header.Set("Originator", "codex_cli_rs")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// errorEvents returns the JSON of every terminal error event in an SSE body.
func errorEvents(surface streamSurface, body string) []string {
	var events []string
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if surface.name == "chat" {
			if data, ok := strings.CutPrefix(line, "data: "); ok && ojson.Get(data, "error").IsObject() {
				events = append(events, data)
			}
		} else if (line == "event: error" || line == "event: response.failed") && i+1 < len(lines) {
			events = append(events, strings.TrimPrefix(lines[i+1], "data: "))
		}
	}
	return events
}

// A quota failure after output was streamed ends with one terminal error event
// that carries the usage limit, in each format's own shape.
func TestUsageLimitAfterCommittedStream(t *testing.T) {
	for _, surface := range streamSurfaces {
		t.Run(surface.name, func(t *testing.T) {
			recorder := doCommittedStream(t, "usage-limit", surface, codexUsageLimit(2*time.Hour))
			body := recorder.Body.String()
			events := errorEvents(surface, body)
			if recorder.Code != http.StatusOK || !strings.Contains(body, "partial") || len(events) != 1 {
				t.Fatalf("status=%d body=%s, want the partial output then one error event", recorder.Code, body)
			}
			errorNode := ojson.Get(events[0], "error")
			if surface.codexClient {
				errorNode = ojson.Get(events[0], "response.error")
			}
			if diff := errorNode.Get("resets_at").Int() - time.Now().Add(2*time.Hour).Unix(); diff < -60 || diff > 60 || errorNode.Get("retryable").Raw != "false" {
				t.Fatalf("event=%s, want resets_at about two hours away and retryable false", events[0])
			}
			if surface.claude {
				if errorNode.Get("type").String() != "rate_limit_error" || !claudeResetSuffix.MatchString(errorNode.Get("message").String()) {
					t.Fatalf("event=%s, want a rate_limit_error ending in (reset after <2h>)", events[0])
				}
			} else if errorNode.Get("type").String() != "usage_limit_reached" {
				t.Fatalf("event=%s, want error.type usage_limit_reached", events[0])
			}
			if surface.name == "chat" && !strings.HasSuffix(body, events[0]+"\n\ndata: [DONE]\n\n") {
				t.Fatalf("chat stream does not end with the error then [DONE]; body=%s", body)
			}
		})
	}
}

// A committed stream that does not qualify sends the same bytes in both modes.
func TestCommittedStreamUnchangedWithoutUsageLimit(t *testing.T) {
	for _, surface := range streamSurfaces {
		t.Run(surface.name, func(t *testing.T) {
			retryable := doCommittedStream(t, "retryable", surface, genericRateLimit(2*time.Hour)).Body.String()
			if !strings.Contains(retryable, "partial") || len(errorEvents(surface, retryable)) != 1 {
				t.Fatalf("body=%s, want the partial output then one error event", retryable)
			}
			if usageLimit := doCommittedStream(t, "usage-limit", surface, genericRateLimit(2*time.Hour)).Body.String(); usageLimit != retryable {
				t.Fatalf("usage-limit body differs from retryable:\n got: %q\nwant: %q", usageLimit, retryable)
			}
			// Today's stream ends on the error event.
			if strings.Contains(retryable, "[DONE]") {
				t.Fatalf("retryable stream gained [DONE]: %q", retryable)
			}
		})
	}
}
