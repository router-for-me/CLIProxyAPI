package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const issue6258ExecutorContent = `{"responseId":"executor-6258","candidates":[{"content":{"parts":[{"text":"answer"}]}}]}`
const issue6258ExecutorUsage = `{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":50,"totalTokenCount":160}}`

var issue6258Providers = []string{"gemini", "vertex_api_key", "vertex_service_account", "antigravity"}

type issue6258StreamExecutor interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

type issue6258RedirectTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport issue6258RedirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Exercise real HTTP body truncation, while keeping the fixed Vertex host local.
	local := request.Clone(request.Context())
	local.URL.Scheme = transport.target.Scheme
	local.URL.Host = transport.target.Host
	local.Host = transport.target.Host
	return transport.base.RoundTrip(local)
}

func issue6258Executor(t *testing.T, provider, baseURL string) (context.Context, issue6258StreamExecutor, *cliproxyauth.Auth) {
	t.Helper()
	ctx := t.Context()
	cfg := &config.Config{RequestRetry: 1}
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "test-key", "base_url": baseURL}}
	switch provider {
	case "gemini":
		return ctx, NewGeminiExecutor(cfg), auth
	case "vertex_api_key":
		return ctx, NewGeminiVertexExecutor(cfg), auth
	case "vertex_service_account":
		var serviceAccount map[string]any
		if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, baseURL+"/token"), &serviceAccount); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		auth.Attributes = nil
		auth.Metadata = map[string]any{"project_id": "proxy-test", "location": "global", "service_account": serviceAccount}
		target, errParse := url.Parse(baseURL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
		return ctx, NewGeminiVertexExecutor(cfg), auth
	case "antigravity":
		auth.Attributes = map[string]string{"base_url": baseURL}
		auth.Metadata = map[string]any{"access_token": "test-token", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339), "project_id": "proxy-test"}
		return ctx, NewAntigravityExecutor(cfg), auth
	default:
		t.Fatalf("unknown provider %q", provider)
		return nil, nil, nil
	}
}

func issue6258StartStream(t *testing.T, ctx context.Context, executor issue6258StreamExecutor, auth *cliproxyauth.Auth, payload string) <-chan cliproxyexecutor.StreamChunk {
	t.Helper()
	request := cliproxyexecutor.Request{Model: "gemini-3.7-flash", Payload: []byte(payload)}
	stream, errExecuteStream := executor.ExecuteStream(ctx, auth, request, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request.Payload, Stream: true,
	})
	if errExecuteStream != nil {
		t.Fatalf("ExecuteStream: %v", errExecuteStream)
	}
	return stream.Chunks
}

func issue6258Events(payload []byte) []gjson.Result {
	var events []gjson.Result
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "data:") {
			event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			if event.Get("type").Exists() {
				events = append(events, event)
			}
		}
	}
	return events
}

func issue6258ExecutorTerminal(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) gjson.Result {
	t.Helper()
	var response gjson.Result
	terminals := 0
	done := map[string]int{}
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Errorf("clean stream returned error: %v", chunk.Err)
		}
		for _, event := range issue6258Events(chunk.Payload) {
			switch event.Get("type").String() {
			case "response.completed", "response.incomplete", "response.failed":
				terminals++
				response = event.Get("response")
			case "response.output_item.done":
				done[event.Get("item.id").String()]++
			}
		}
	}
	if terminals != 1 || response.Get("status").String() != "completed" {
		t.Errorf("clean stream terminal count=%d status=%q, want one completed", terminals, response.Get("status").String())
	}
	for _, item := range response.Get("output").Array() {
		if count := done[item.Get("id").String()]; count != 1 {
			t.Errorf("item done count=%d, want 1: %s", count, item.Raw)
		}
	}
	return response
}

func issue6258SSE(provider, body string, trace bool) string {
	if provider == "antigravity" {
		body = `{"response":` + body + `}`
	}
	if trace {
		body = strings.TrimSuffix(body, "}") + `,"traceId":"trace-6258"}`
	}
	return "data: " + body + "\n\n"
}

func issue6258TokenResponse(w http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path != "/token" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
	return true
}

func TestIssue6258ExecutorResponsesSplitUsage(t *testing.T) {
	for _, provider := range issue6258Providers {
		for _, trace := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trace=%t", provider, trace), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if issue6258TokenResponse(w, request) {
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, body := range []string{issue6258ExecutorContent, `{"candidates":[{"finishReason":"STOP"}]}`, issue6258ExecutorUsage} {
						_, _ = io.WriteString(w, issue6258SSE(provider, body, trace))
					}
				}))
				defer server.Close()
				ctx, executor, auth := issue6258Executor(t, provider, server.URL)
				response := issue6258ExecutorTerminal(t, issue6258StartStream(t, ctx, executor, auth, `{"input":"hello","stream":true}`))
				for path, want := range map[string]int64{"input_tokens": 100, "output_tokens": 60, "total_tokens": 160, "output_tokens_details.reasoning_tokens": 50} {
					got := response.Get("usage." + path)
					if !got.Exists() || got.Int() != want {
						t.Errorf("terminal usage.%s=%s, want %d; usage=%s", path, got.Raw, want, response.Get("usage").Raw)
					}
				}
			})
		}
	}
}

func TestIssue6258ExecutorReadErrorHasNoTerminal(t *testing.T) {
	for _, provider := range issue6258Providers {
		t.Run(provider, func(t *testing.T) {
			body := issue6258SSE(provider, issue6258ExecutorContent, false)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if issue6258TokenResponse(w, request) {
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+128))
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			ctx, executor, auth := issue6258Executor(t, provider, server.URL)
			readErrors, deltas, terminals := 0, 0, 0
			for chunk := range issue6258StartStream(t, ctx, executor, auth, `{"input":"hello","stream":true}`) {
				if chunk.Err != nil {
					readErrors++
					if !errors.Is(chunk.Err, io.ErrUnexpectedEOF) {
						t.Errorf("read error=%v, want unexpected EOF", chunk.Err)
					}
				}
				for _, event := range issue6258Events(chunk.Payload) {
					kind := event.Get("type").String()
					if kind == "response.output_text.delta" {
						deltas++
					}
					if kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
						terminals++
						t.Errorf("read-error stream synthesized %s before unexpected EOF", kind)
					}
				}
			}
			if readErrors != 1 || deltas == 0 || terminals != 0 {
				t.Errorf("read-error stream: errors=%d deltas=%d terminals=%d, want 1/>0/0", readErrors, deltas, terminals)
			}
		})
	}
}

func TestIssue6258ExecutorCancellationHasNoTerminal(t *testing.T) {
	for _, provider := range issue6258Providers {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if issue6258TokenResponse(w, request) {
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, issue6258SSE(provider, issue6258ExecutorContent, false))
				w.(http.Flusher).Flush()
				<-request.Context().Done()
			}))
			defer server.Close()
			baseCtx, executor, auth := issue6258Executor(t, provider, server.URL)
			ctx, cancel := context.WithCancel(baseCtx)
			defer cancel()
			started := false
			for chunk := range issue6258StartStream(t, ctx, executor, auth, `{"input":"hello","stream":true}`) {
				for _, event := range issue6258Events(chunk.Payload) {
					kind := event.Get("type").String()
					if kind == "response.output_text.delta" {
						started = true
						cancel()
					}
					if kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" {
						t.Errorf("canceled stream synthesized %s", kind)
					}
				}
			}
			if !started {
				t.Fatal("cancellation did not exercise an established content stream")
			}
		})
	}
}

func TestIssue6258ExecutorCleanEOFControl(t *testing.T) {
	for _, provider := range issue6258Providers {
		for _, patch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/apply_patch=%t", provider, patch), func(t *testing.T) {
				body, payload := issue6258ExecutorContent, `{"input":"hello","stream":true}`
				if patch {
					// A valid source STOP must not become an invalid patch when Responses finalization is deferred.
					body, payload = executorPatchResponse, executorPatchRequest
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					if issue6258TokenResponse(w, request) {
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, issue6258SSE(provider, body, false))
				}))
				defer server.Close()
				ctx, executor, auth := issue6258Executor(t, provider, server.URL)
				response := issue6258ExecutorTerminal(t, issue6258StartStream(t, ctx, executor, auth, payload))
				if patch {
					assertExecutorPatchOutput(t, []byte(response.Raw))
				} else if response.Get("output.0.content.0.text").String() != "answer" {
					t.Errorf("clean EOF lost partial output: %s", response.Raw)
				}
			})
		}
	}
}
