package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type codexDeferredLogTransport struct {
	requestBody    []byte
	mode           string
	discardRequest bool
}

func (tr *codexDeferredLogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var err error
	if tr.discardRequest {
		_, err = io.Copy(io.Discard, req.Body)
	} else {
		tr.requestBody, err = io.ReadAll(req.Body)
	}
	if err != nil {
		return nil, err
	}
	status := http.StatusOK
	body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_log\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
	switch tr.mode {
	case "HTTP failure":
		status = 503
		body = `{"error":{"message":"unavailable"}}`
	case "stream failure":
		body = "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"unavailable\"}}}\n\n"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func TestCodexDeferredLogRetainsFinalOutboundBody(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		for _, source := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
			for _, mode := range []string{"success", "HTTP failure", "stream failure"} {
				t.Run(fmt.Sprintf("buffered=%t/%s/%s", buffered, source, mode), func(t *testing.T) {
					ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx := context.WithValue(t.Context(), "gin", ginCtx)
					transport := &codexDeferredLogTransport{mode: mode}
					ctx = context.WithValue(ctx, "cliproxy.roundtripper", transport)
					cfg := &config.Config{}
					cfg.Codex.StreamBootstrapBuffering = buffered
					executor := NewCodexExecutor(cfg)
					payload := []byte(`{"model":"gpt-5.4","input":"hello","prompt_cache_key":"log-test"}`)
					if source == sdktranslator.FormatClaude {
						payload = []byte(`{"model":"gpt-5.4","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`)
					}
					original := string(payload)
					result, err := executor.ExecuteStream(ctx, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}, cliproxyexecutor.Options{SourceFormat: source, ResponseFormat: source, Stream: true})
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
					if (err != nil) != (mode != "success") {
						t.Fatalf("error=%v for mode %s", err, mode)
					}
					if string(payload) != original {
						t.Fatal("caller payload mutated")
					}
					value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
					if !exists {
						t.Fatal("missing deferred request")
					}
					captures := value.([]logging.DeferredAPIRequest)
					if len(captures) != 1 {
						t.Fatalf("captures=%d, want 1", len(captures))
					}
					log := string(captures[0]())
					_, got, ok := strings.Cut(log, "\nBody:\n")
					if !ok || got != string(transport.requestBody)+"\n\n" {
						t.Fatalf("deferred body differs from actual outbound body:\n%s\nwant %s", got, transport.requestBody)
					}
				})
			}
		}
	}
}

func BenchmarkCodexDeferredRequestLogging(b *testing.B) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	executor := NewCodexExecutor(cfg)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(b.Context(), "gin", ginCtx)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", &codexDeferredLogTransport{mode: "success", discardRequest: true})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"` + strings.Repeat("x", 1<<20) + `","prompt_cache_key":"log-benchmark"}`)}
	opts := cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatClaude}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ginCtx.Keys = nil
		result, err := executor.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			b.Fatal(err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				b.Fatal(chunk.Err)
			}
		}
	}
}
