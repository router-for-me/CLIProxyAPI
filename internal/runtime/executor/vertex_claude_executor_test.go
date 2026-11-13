package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type vertexClaudeCapture struct {
	mu     sync.Mutex
	path   string
	auth   string
	body   []byte
	status int
}

func (c *vertexClaudeCapture) get() (string, string, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.auth, c.body
}

// newVertexClaudeTestEnv starts fake token and Vertex servers and returns a service-account auth.
func newVertexClaudeTestEnv(t *testing.T, respond func(w http.ResponseWriter, stream bool)) (*cliproxyauth.Auth, *vertexClaudeCapture) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"sa-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)
	capture := &vertexClaudeCapture{}
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.path = r.URL.Path
		capture.auth = r.Header.Get("Authorization")
		capture.body = b
		capture.mu.Unlock()
		respond(w, strings.HasSuffix(r.URL.Path, ":streamRawPredict"))
	}))
	t.Cleanup(apiSrv.Close)
	prev := vertexClaudeBaseURL
	vertexClaudeBaseURL = func(string) string { return apiSrv.URL }
	t.Cleanup(func() { vertexClaudeBaseURL = prev })

	var sa map[string]any
	if err := json.Unmarshal(testVertexServiceAccountJSON(t, tokenSrv.URL), &sa); err != nil {
		t.Fatalf("unmarshal sa: %v", err)
	}
	return &cliproxyauth.Auth{
		ID:       "vertex-sa",
		Provider: "vertex",
		Metadata: map[string]any{"project_id": "proj-1", "location": "us-east5", "service_account": sa},
	}, capture
}

func vertexClaudeOKResponder(w http.ResponseWriter, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
}

const vertexClaudeTestPayload = `{"model":"claude-sonnet-5-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`

func vertexClaudeTestExecutor(cfg *config.Config) *GeminiVertexExecutor {
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.VertexClaudeModels = []string{" claude-sonnet-5-5 "}
	return NewGeminiVertexExecutor(cfg)
}

func vertexClaudeReq(model string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{Model: model, Payload: []byte(vertexClaudeTestPayload)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
}

func TestVertexClaudeExecuteUsesRawPredict(t *testing.T) {
	auth, capture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	path, authHeader, body := capture.get()
	if want := "/v1/projects/proj-1/locations/us-east5/publishers/anthropic/models/claude-sonnet-5-5:rawPredict"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if authHeader != "Bearer sa-token" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if ojson.GetBytes(body, "model").Exists() {
		t.Fatalf("body must not contain model: %s", body)
	}
	if got := ojson.GetBytes(body, "anthropic_version").String(); got != "vertex-2023-10-16" {
		t.Fatalf("anthropic_version = %q", got)
	}
	if ojson.GetBytes(body, "stream").Bool() {
		t.Fatalf("stream must not be true: %s", body)
	}
	if got := ojson.GetBytes(resp.Payload, "content.0.text").String(); got != "hi" {
		t.Fatalf("response payload = %s", resp.Payload)
	}
}

func TestVertexClaudeExecuteTranslatesToOpenAI(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req := cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"}]}`)}
	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := ojson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hi" {
		t.Fatalf("openai payload = %s", resp.Payload)
	}
}

func TestVertexClaudeExecuteStreamUsesStreamRawPredict(t *testing.T) {
	auth, capture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var all strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		all.Write(chunk.Payload)
	}
	path, authHeader, body := capture.get()
	if !strings.HasSuffix(path, "/publishers/anthropic/models/claude-sonnet-5-5:streamRawPredict") {
		t.Fatalf("path = %q", path)
	}
	if authHeader != "Bearer sa-token" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if ojson.GetBytes(body, "model").Exists() || !ojson.GetBytes(body, "stream").Bool() {
		t.Fatalf("unexpected body: %s", body)
	}
	if ojson.GetBytes(body, "anthropic_version").String() != "vertex-2023-10-16" {
		t.Fatalf("missing anthropic_version: %s", body)
	}
	if !strings.Contains(all.String(), "message_stop") || !strings.Contains(all.String(), `"text":"hi"`) {
		t.Fatalf("stream output = %s", all.String())
	}
}

// A policy refusal must not truncate the stream: remaining events, including
// message_stop, are forwarded before the terminal error, matching the Claude executor.
func TestVertexClaudeExecuteStreamForwardsEventsAfterRefusal(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, stream bool) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"explanation\":\"blocked\"}},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var payloads strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if streamErr != nil {
			t.Fatalf("payload after terminal error: %q", chunk.Payload)
		}
		payloads.Write(chunk.Payload)
	}
	if !strings.Contains(payloads.String(), "event: message_stop\n") {
		t.Fatalf("message_stop was not forwarded: %q", payloads.String())
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "refusal") {
		t.Fatalf("terminal error = %v, want refusal error", streamErr)
	}
}

// A non-stream policy refusal must be a 400 error with failed usage, matching
// the stream path and the Claude executor instead of a 200 success.
func TestVertexClaudeExecuteRefusalReturnsError(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","explanation":"blocked"},"usage":{"input_tokens":3,"output_tokens":0}}`)
	})
	records := captureVertexClaudeUsage(t, auth)
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	_, err := exec.Execute(context.Background(), auth, req, opts)
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != http.StatusBadRequest || !clienterror.IsClaudePolicyRefusalError(err) {
		t.Fatalf("err = %v, want 400 policy refusal error", err)
	}
	record := records.next(t)
	if !record.Failed || record.Detail.InputTokens != 3 {
		t.Fatalf("usage record = failed %v input %d, want failed with input 3", record.Failed, record.Detail.InputTokens)
	}
}

// Claude-format clients must receive complete SSE events with their framing intact.
func TestVertexClaudeExecuteStreamPassthroughKeepsSSEFraming(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if len(chunks) != 6 {
		t.Fatalf("got %d chunks, want 6 complete events: %q", len(chunks), chunks)
	}
	for i, c := range chunks {
		if !strings.HasPrefix(c, "event: ") || !strings.Contains(c, "\ndata: ") || !strings.HasSuffix(c, "\n\n") {
			t.Fatalf("chunk %d is not a complete SSE event: %q", i, c)
		}
	}
	if !strings.HasPrefix(chunks[5], "event: message_stop\n") {
		t.Fatalf("last chunk = %q", chunks[5])
	}
}

func TestVertexClaudeUpstreamErrorPropagatesStatus(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"quota"}`)
	})
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	_, err := exec.Execute(context.Background(), auth, req, opts)
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("err = %v", err)
	}
}

func TestVertexClaudeDoesNotFollowRedirects(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	_, err := exec.Execute(context.Background(), auth, req, opts)
	if err == nil || leaked {
		t.Fatalf("redirect followed or no error: err=%v leaked=%v", err, leaked)
	}
}

func TestVertexClaudeCountTokensNotImplemented(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	_, err := exec.CountTokens(context.Background(), auth, req, opts)
	se, ok := err.(interface{ StatusCode() int })
	if !ok || se.StatusCode() != http.StatusNotImplemented {
		t.Fatalf("err = %v", err)
	}
}

// vertexClaudeUsageCapture collects usage records published for one auth.
type vertexClaudeUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *vertexClaudeUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "vertex" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type vertexClaudeUsageNoop struct{}

func (vertexClaudeUsageNoop) HandleUsage(context.Context, usage.Record) {}

// captureVertexClaudeUsage gives the auth a test-unique ID and registers a usage sink for it.
func captureVertexClaudeUsage(t *testing.T, auth *cliproxyauth.Auth) *vertexClaudeUsageCapture {
	t.Helper()
	auth.ID = t.Name()
	capture := &vertexClaudeUsageCapture{authID: auth.ID, records: make(chan usage.Record, 4)}
	pluginName := "test-vertex-claude-usage-" + t.Name()
	usage.RegisterNamedPlugin(pluginName, capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(pluginName, vertexClaudeUsageNoop{}) })
	return capture
}

func (c *vertexClaudeUsageCapture) next(t *testing.T) usage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func drainVertexClaudeStream(t *testing.T, result *cliproxyexecutor.StreamResult) (string, error) {
	t.Helper()
	var payloads strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		payloads.Write(chunk.Payload)
	}
	return payloads.String(), streamErr
}

// Successful requests must publish the upstream Claude token usage in both modes.
func TestVertexClaudePublishesUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "non-stream"},
		{name: "stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
			records := captureVertexClaudeUsage(t, auth)
			exec := vertexClaudeTestExecutor(nil)
			req, opts := vertexClaudeReq("claude-sonnet-5-5")
			if tc.stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				if _, errStream := drainVertexClaudeStream(t, result); errStream != nil {
					t.Fatalf("stream error: %v", errStream)
				}
			} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			record := records.next(t)
			if record.Failed {
				t.Fatalf("usage record marked failed: %+v", record.Fail)
			}
			if record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 2 {
				t.Fatalf("usage = input %d output %d, want 3/2", record.Detail.InputTokens, record.Detail.OutputTokens)
			}
		})
	}
}

// Upstream closes the stream without message_stop. Both the passthrough and the
// translated paths must surface a 502 truncation error and record failed usage.
func TestVertexClaudeStreamTruncationReportsError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format sdktranslator.Format
	}{
		{name: "claude passthrough", format: sdktranslator.FormatClaude},
		{name: "openai translated", format: sdktranslator.FormatOpenAI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n")
				// Clean EOF without message_stop.
			})
			records := captureVertexClaudeUsage(t, auth)
			exec := vertexClaudeTestExecutor(nil)
			req, _ := vertexClaudeReq("claude-sonnet-5-5")
			result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}
			_, streamErr := drainVertexClaudeStream(t, result)
			se, ok := streamErr.(interface{ StatusCode() int })
			if !ok || se.StatusCode() != http.StatusBadGateway || !strings.Contains(streamErr.Error(), "message_stop") {
				t.Fatalf("stream error = %v, want 502 truncation error", streamErr)
			}
			// Truncation must be flagged so auth managers neither cool the credential nor replay.
			if te, ok := streamErr.(cliproxyexecutor.StreamTruncatedError); !ok || !te.IsStreamTruncated() {
				t.Fatalf("stream error = %v, want StreamTruncatedError", streamErr)
			}
			if record := records.next(t); !record.Failed {
				t.Fatal("usage record must be marked failed on a truncated stream")
			}
		})
	}
}

// A client that cancels mid-stream, before message_stop, must be recorded as a
// failed request rather than a success with partial usage.
func TestVertexClaudeStreamClientCancelRecordsFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format sdktranslator.Format
	}{
		{name: "claude passthrough", format: sdktranslator.FormatClaude},
		{name: "openai translated", format: sdktranslator.FormatOpenAI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				for i := 0; i < 32; i++ {
					_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"x\"}}\n\n")
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				// Keep the stream open so the executor is blocked on delivery, not EOF.
				<-release
			})
			t.Cleanup(func() { close(release) })
			records := captureVertexClaudeUsage(t, auth)
			exec := vertexClaudeTestExecutor(nil)
			req, _ := vertexClaudeReq("claude-sonnet-5-5")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}
			if first, ok := <-result.Chunks; !ok || first.Err != nil {
				t.Fatalf("first chunk = %+v, ok=%v", first, ok)
			}
			cancel()
			if record := records.next(t); !record.Failed {
				t.Fatal("usage record must be marked failed when the client cancels before message_stop")
			}
		})
	}
}

// rawPredict returns a Messages JSON object; Gemini clients must still receive
// the content and usage, not an empty default response.
func TestVertexClaudeExecuteTranslatesToGemini(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	req := cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)}
	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := ojson.GetBytes(resp.Payload, "candidates.0.content.parts.0.text").String(); got != "hi" {
		t.Fatalf("gemini payload = %s", resp.Payload)
	}
	if got := ojson.GetBytes(resp.Payload, "usageMetadata.promptTokenCount").Int(); got != 3 {
		t.Fatalf("promptTokenCount = %d: %s", got, resp.Payload)
	}
}

// Thinking and forced tool use cannot be combined on Anthropic; a valid OpenAI
// request with both must have thinking removed before it is sent.
func TestVertexClaudeForcedToolChoiceDropsThinking(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "rawPredict"},
		{name: "streamRawPredict", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, capture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
			exec := vertexClaudeTestExecutor(nil)
			req := cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: []byte(`{"model":"claude-sonnet-5-5","reasoning_effort":"high","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"tool_choice":"required"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			if tc.stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				_, _ = drainVertexClaudeStream(t, result)
			} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			_, _, body := capture.get()
			if got := ojson.GetBytes(body, "tool_choice.type").String(); got != "any" {
				t.Fatalf("tool_choice.type = %q: %s", got, body)
			}
			if ojson.GetBytes(body, "thinking").Exists() || ojson.GetBytes(body, "output_config.effort").Exists() {
				t.Fatalf("thinking must be removed with forced tool use: %s", body)
			}
		})
	}
}

// An upstream that ends right after the message_stop data line, without the
// trailing blank line, must still yield a properly delimited final event.
func TestVertexClaudePassthroughRestoresFinalEventDelimiter(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, func(w http.ResponseWriter, _ bool) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n")
	})
	exec := vertexClaudeTestExecutor(nil)
	req, opts := vertexClaudeReq("claude-sonnet-5-5")
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if len(chunks) != 2 || !strings.HasPrefix(chunks[1], "event: message_stop\n") || !strings.HasSuffix(chunks[1], "\n\n") {
		t.Fatalf("chunks = %q, want final message_stop ending with a blank line", chunks)
	}
}

// Model IDs are registered case-insensitively, so routing must accept any case and
// the upstream URL must use the lowercase Vertex model ID.
func TestVertexClaudeRouteMatchesCaseInsensitively(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5-5", "Claude-Sonnet-5-5"} {
		t.Run(model, func(t *testing.T) {
			auth, capture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
			exec := NewGeminiVertexExecutor(&config.Config{VertexClaudeModels: []string{"CLAUDE-SONNET-5-5"}})
			req, opts := vertexClaudeReq(model)
			if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			path, _, _ := capture.get()
			if !strings.HasSuffix(path, "/publishers/anthropic/models/claude-sonnet-5-5:rawPredict") {
				t.Fatalf("path = %q, want lowercase anthropic rawPredict", path)
			}
		})
	}
}

// Requests that do not qualify for the Anthropic publisher path must still reach
// the Gemini generateContent endpoint through Execute.
func TestVertexClaudeNonQualifyingRequestsUseGenerateContent(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	geminiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	t.Cleanup(geminiSrv.Close)
	target, errParse := url.Parse(geminiSrv.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	// Redirect the fixed Vertex host to the local server; token exchange is unaffected.
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})

	saAuth, claudeCapture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	apiKeyAuth := &cliproxyauth.Auth{ID: "vertex-key", Provider: "vertex", Attributes: map[string]string{"api_key": "AIza-test", "base_url": geminiSrv.URL}}
	for _, tc := range []struct {
		name  string
		auth  *cliproxyauth.Auth
		model string
	}{
		{name: "service account with unlisted claude model", auth: saAuth, model: "claude-opus-5-5"},
		{name: "api key with listed claude model", auth: apiKeyAuth, model: "claude-sonnet-5-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			gotPath = ""
			mu.Unlock()
			exec := vertexClaudeTestExecutor(nil)
			req, opts := vertexClaudeReq(tc.model)
			_, _ = exec.Execute(ctx, tc.auth, req, opts)
			mu.Lock()
			path := gotPath
			mu.Unlock()
			if !strings.HasSuffix(path, "/models/"+tc.model+":generateContent") || strings.Contains(path, "publishers/anthropic") {
				t.Fatalf("upstream path = %q, want Gemini generateContent", path)
			}
			if claudePath, _, _ := claudeCapture.get(); claudePath != "" {
				t.Fatalf("request leaked to Anthropic publisher path: %q", claudePath)
			}
		})
	}
}

func TestVertexClaudeRouteSelection(t *testing.T) {
	auth, _ := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
	exec := vertexClaudeTestExecutor(nil)
	if _, _, _, ok := exec.vertexClaudeRoute(auth, "claude-sonnet-5-5"); !ok {
		t.Fatal("listed model should route to Claude path")
	}
	if _, _, _, ok := exec.vertexClaudeRoute(auth, "claude-sonnet-5-5(8192)"); !ok {
		t.Fatal("suffix model should route to Claude path")
	}
	if _, _, _, ok := exec.vertexClaudeRoute(auth, "claude-opus-5-5"); ok {
		t.Fatal("non-listed claude model must keep Gemini path")
	}
	if _, _, _, ok := exec.vertexClaudeRoute(auth, "gemini-2.5-pro"); ok {
		t.Fatal("gemini model must keep Gemini path")
	}
	apiKeyAuth := &cliproxyauth.Auth{ID: "k", Provider: "vertex", Attributes: map[string]string{"api_key": "AIza-test"}}
	if _, _, _, ok := exec.vertexClaudeRoute(apiKeyAuth, "claude-sonnet-5-5"); ok {
		t.Fatal("API key auth must be unaffected")
	}
	if _, _, _, ok := NewGeminiVertexExecutor(&config.Config{}).vertexClaudeRoute(auth, "claude-sonnet-5-5"); ok {
		t.Fatal("empty config must not route")
	}
}

// User payload rules must be the final barrier on both rawPredict and
// streamRawPredict: overrides win over built-in fields and filters remove
// fields the executor injected.
func TestVertexClaudePayloadRulesOrdering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "rawPredict"},
		{name: "streamRawPredict", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, capture := newVertexClaudeTestEnv(t, vertexClaudeOKResponder)
			models := []config.PayloadModelRule{{Name: "claude-sonnet-5-5", Protocol: "claude"}}
			cfg := &config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{
					Models: models,
					Params: map[string]any{"max_tokens": 7, "custom_claude_param": "final_barrier_applied"},
				}},
				Filter: []config.PayloadFilterRule{{
					Models: models,
					Params: []string{"anthropic_version"},
				}},
			}}
			exec := vertexClaudeTestExecutor(cfg)
			req, opts := vertexClaudeReq("claude-sonnet-5-5")
			if tc.stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				if _, errStream := drainVertexClaudeStream(t, result); errStream != nil {
					t.Fatalf("stream error: %v", errStream)
				}
			} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			_, _, body := capture.get()
			if got := ojson.GetBytes(body, "max_tokens").Int(); got != 7 {
				t.Fatalf("max_tokens = %d: %s", got, body)
			}
			if got := ojson.GetBytes(body, "custom_claude_param").String(); got != "final_barrier_applied" {
				t.Fatalf("override missing: %s", body)
			}
			if ojson.GetBytes(body, "anthropic_version").Exists() {
				t.Fatalf("filtered built-in field was restored: %s", body)
			}
			if got := ojson.GetBytes(body, "stream").Bool(); got != tc.stream {
				t.Fatalf("stream = %v, want %v: %s", got, tc.stream, body)
			}
		})
	}
}
