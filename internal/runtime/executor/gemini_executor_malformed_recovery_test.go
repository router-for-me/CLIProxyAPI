package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiRecoveryFinish = `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: thought\n... call:default_api:run_command{invalid args}"}],"modelVersion":"gemini-4-argon"}`
	geminiRecoveryUsage  = `{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443}}`
	geminiRecoveryFCC    = "toolConfig.functionCallingConfig"
)

type geminiRecoveryFixture struct {
	exec      *GeminiExecutor
	cfg       *config.Config
	ctx       context.Context
	auth      *cliproxyauth.Auth
	req       cliproxyexecutor.Request
	opts      cliproxyexecutor.Options
	requests  []string
	records   <-chan usage.Record
	responses []*http.Response
	bodies    []*geminiRecoveryBody
	headers   http.Header
}

type geminiRecoveryBody struct {
	reader io.Reader
	onEOF  func()
	closed bool
}

func (b *geminiRecoveryBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) && b.onEOF != nil {
		onEOF := b.onEOF
		b.onEOF = nil
		onEOF()
	}
	return n, err
}

func (b *geminiRecoveryBody) Close() error {
	b.closed = true
	return nil
}

func newGeminiRecoveryFixture(t *testing.T, replies ...string) *geminiRecoveryFixture {
	t.Helper()
	f := &geminiRecoveryFixture{
		cfg:  &config.Config{},
		auth: &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini", Attributes: map[string]string{"api_key": "test-key", "base_url": "https://gemini.test"}},
		req:  cliproxyexecutor.Request{Model: "gemini-4-argon", Payload: []byte(`{"model":"gemini-4-argon","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024},"system":"Use the supplied tools.","messages":[{"role":"user","content":"Inspect the working directory."}],"tools":[{"name":"run_command","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{}}}]}`)},
		opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: true},
	}
	f.exec = NewGeminiExecutor(f.cfg)
	capture := &malformedFunctionCallUsageCapture{authID: f.auth.ID, records: make(chan usage.Record, 4)}
	f.records = capture.records
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{}) })
	for index, reply := range replies {
		body := &geminiRecoveryBody{reader: strings.NewReader(reply)}
		f.bodies = append(f.bodies, body)
		f.responses = append(f.responses, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Attempt": {fmt.Sprint(index)}}, Body: body})
	}
	f.ctx = context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		f.requests = append(f.requests, string(body))
		if len(f.requests) > len(replies) {
			return nil, fmt.Errorf("unexpected upstream request %d", len(f.requests))
		}
		return f.responses[len(f.requests)-1], nil
	}))
	return f
}

func geminiRecoverySSE(frames ...string) string {
	return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
}

func geminiRecoverySet(t *testing.T, body, path string, value any) string {
	t.Helper()
	updated, err := sjson.Set(body, path, value)
	if err != nil {
		t.Fatalf("set %s: %v", path, err)
	}
	return updated
}

func (f *geminiRecoveryFixture) stream(t *testing.T) (string, error) {
	t.Helper()
	result, err := f.exec.ExecuteStream(f.ctx, f.auth, f.req, f.opts)
	if err != nil {
		return "", err
	}
	f.headers = result.Headers
	return drainChunks(result)
}

func TestGeminiExecutorMalformedRecoveryPreservesRequestAndStream(t *testing.T) {
	f := newGeminiRecoveryFixture(t,
		geminiRecoverySSE(`{"responseId":"discarded-attempt","modelVersion":"gemini-4-argon"}`, geminiRecoveryFinish, geminiRecoveryUsage),
		geminiRecoverySSE(
			`{"responseId":"recovered-attempt","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"pwd"}}}]}}]}`,
			`{"candidates":[{"finishReason":"STOP"}]}`,
			`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":12}}`),
	)
	output, err := f.stream(t)
	if err != nil || len(f.requests) != 2 {
		t.Fatalf("requests = %d, error = %v; want two requests and success", len(f.requests), err)
	}
	if f.headers.Get("X-Attempt") != "1" || !f.bodies[0].closed || !f.bodies[1].closed {
		t.Fatalf("headers or body ownership were not transferred: headers=%v closed=%v/%v", f.headers, f.bodies[0].closed, f.bodies[1].closed)
	}
	first := f.requests[0]
	if gjson.Get(first, "tools.0.functionDeclarations.#").Int() != 2 || !gjson.Get(first, "generationConfig.thinkingConfig").Exists() {
		t.Fatalf("missing tools or thinking settings in initial request: %s", first)
	}
	want := geminiRecoverySet(t, first, geminiRecoveryFCC+".mode", "ANY")
	want = geminiRecoverySet(t, want, geminiRecoveryFCC+".allowedFunctionNames", []string{"run_command"})
	if !reflect.DeepEqual(gjson.Parse(f.requests[1]).Value(), gjson.Parse(want).Value()) {
		t.Fatalf("recovery changed fields beyond the targeted tool constraint\ngot: %s\nwant: %s", f.requests[1], want)
	}
	if strings.Contains(output, "discarded-attempt") || strings.Contains(output, "event: error\n") || strings.Count(output, "event: message_start\n") != 1 {
		t.Fatalf("first attempt leaked into recovered stream: %s", output)
	}
	for _, fragment := range []string{"recovered-attempt", `"name":"run_command"`, `\"command\":\"pwd\"`, `"stop_reason":"tool_use"`, "event: content_block_stop\n", "event: message_stop\n"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("recovered stream missing %q: %s", fragment, output)
		}
	}
	var finalUsage gjson.Result
	for _, line := range strings.Split(output, "\n") {
		event := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if event.Get("type").String() == "message_delta" {
			finalUsage = event.Get("usage")
		}
	}
	if finalUsage.Get("input_tokens").Int() != 7 || finalUsage.Get("output_tokens").Int() != 5 {
		t.Errorf("final Claude usage = %s, want input=7 output=5", finalUsage.Raw)
	}
	assertMalformedFunctionCallFailedUsage(t, f.records)
	select {
	case record := <-f.records:
		if record.Failed || record.Detail.InputTokens != 7 || record.Detail.OutputTokens != 3 || record.Detail.ReasoningTokens != 2 || record.Detail.TotalTokens != 12 {
			t.Fatalf("second request usage = %+v, want successful 7/3/2/12", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for second request usage")
	}
}

func TestGeminiExecutorMalformedRecoveryIneligible(t *testing.T) {
	for _, tc := range []struct {
		name      string
		message   string
		prefix    string
		source    sdktranslator.Format
		response  sdktranslator.Format
		params    map[string]any
		filters   []string
		retryOnly bool
	}{
		{name: "unknown target", message: "Malformed function call: call:default_api:unknown{invalid args}"},
		{name: "multiple targets", message: "Malformed function call: call:default_api:run_command{invalid} call:default_api:get_weather{invalid}"},
		{name: "missing target", message: "Malformed function call: invalid args"},
		{name: "text already emitted", prefix: `{"candidates":[{"content":{"parts":[{"text":"Already sent"}]}}]}`},
		{name: "thought already emitted", prefix: `{"candidates":[{"content":{"parts":[{"thought":true,"text":"Already sent"}]}}]}`},
		{name: "native Gemini input", source: sdktranslator.FormatGemini},
		{name: "native Gemini output", response: sdktranslator.FormatGemini},
		{name: "tools disabled", params: map[string]any{geminiRecoveryFCC + ".mode": "NONE"}},
		{name: "target outside allowlist", params: map[string]any{geminiRecoveryFCC + ".allowedFunctionNames": []string{"get_weather"}}},
		{name: "target already forced", params: map[string]any{geminiRecoveryFCC + ".mode": "ANY", geminiRecoveryFCC + ".allowedFunctionNames": []string{"run_command"}}},
		{name: "payload cancels constraint", retryOnly: true, params: map[string]any{geminiRecoveryFCC + ".mode": "AUTO"}},
		{name: "payload broadens allowlist", retryOnly: true, params: map[string]any{geminiRecoveryFCC + ".allowedFunctionNames": []string{"run_command", "get_weather"}}},
		{name: "payload removes constraint", retryOnly: true, filters: []string{geminiRecoveryFCC}},
		{name: "payload removes target", retryOnly: true, filters: []string{"tools.0.functionDeclarations.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finish := geminiRecoveryFinish
			if tc.message != "" {
				finish = geminiRecoverySet(t, finish, "candidates.0.finishMessage", tc.message)
			}
			frames := []string{finish, geminiRecoveryUsage}
			if tc.prefix != "" {
				frames = append([]string{tc.prefix}, frames...)
			}
			f := newGeminiRecoveryFixture(t, geminiRecoverySSE(frames...))
			if tc.source != "" {
				f.opts.SourceFormat = tc.source
				f.req.Payload = []byte(`{"contents":[{"role":"user","parts":[{"text":"Inspect the working directory."}]}],"tools":[{"functionDeclarations":[{"name":"run_command","parameters":{"type":"object","properties":{"command":{"type":"string"}}}},{"name":"get_weather","parameters":{"type":"object","properties":{}}}]}]}`)
			}
			if tc.response != "" {
				f.opts.ResponseFormat = tc.response
			}
			model := config.PayloadModelRule{Name: "gemini-4-argon", Protocol: "gemini"}
			if tc.retryOnly {
				model.Match = []map[string]any{{geminiRecoveryFCC + ".mode": "ANY"}}
			}
			f.cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{model}, Params: tc.params}}
			f.cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{model}, Params: tc.filters}}
			output, err := f.stream(t)
			if len(f.requests) != 1 {
				t.Fatalf("requests = %d, want no recovery; error = %v", len(f.requests), err)
			}
			if f.opts.ResponseFormat == sdktranslator.FormatClaude && err == nil {
				t.Fatalf("missing terminal malformed error: %s", output)
			}
			if tc.prefix != "" && !strings.Contains(output, "Already sent") {
				t.Fatalf("valid content was discarded: %s", output)
			}
		})
	}
}

func TestGeminiExecutorUsageWaitsForTerminalFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason any
	}{
		{"empty", ""}, {"null", nil}, {"unspecified", "FINISH_REASON_UNSPECIFIED"}, {"unknown", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := `{"responseId":"normal","candidates":[{"content":{"parts":[{"text":"Preparing"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`
			first = geminiRecoverySet(t, first, "candidates.0.finishReason", tc.reason)
			f := newGeminiRecoveryFixture(t, geminiRecoverySSE(first,
				`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run_command","args":{"command":"pwd"}}}]}}]}`,
				`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":12}}`))
			output, err := f.stream(t)
			tool := strings.Index(output, `"name":"run_command"`)
			terminal := strings.Index(output, "event: message_delta\n")
			if err != nil || len(f.requests) != 1 || tool < 0 || terminal < tool || strings.Count(output, "event: message_delta\n") != 1 || !strings.Contains(output, `"stop_reason":"tool_use"`) {
				t.Fatalf("nonterminal usage finalized the stream: requests=%d error=%v output=%s", len(f.requests), err, output)
			}
		})
	}
}

func TestGeminiExecutorMalformedRecoveryTransportFailure(t *testing.T) {
	for _, kind := range []string{"http", "truncated http", "empty", "metadata", "metadata done", "unspecified", "unknown", "api error", "read"} {
		t.Run(kind, func(t *testing.T) {
			f := newGeminiRecoveryFixture(t, geminiRecoverySSE(geminiRecoveryFinish, geminiRecoveryUsage), "")
			sentinel := errors.New("recovery interrupted")
			switch kind {
			case "http":
				f.responses[1].StatusCode = http.StatusServiceUnavailable
			case "truncated http":
				f.responses[1].StatusCode = http.StatusTooManyRequests
				f.bodies[1].reader = io.MultiReader(strings.NewReader(`{"error":`), iotest.ErrReader(io.ErrUnexpectedEOF))
			case "metadata":
				f.bodies[1].reader = strings.NewReader(geminiRecoverySSE(`{"responseId":"metadata-only"}`))
			case "metadata done":
				f.bodies[1].reader = strings.NewReader(geminiRecoverySSE(`{"responseId":"metadata-only"}`, "[DONE]"))
			case "unspecified":
				f.bodies[1].reader = strings.NewReader(geminiRecoverySSE(`{"candidates":[{"content":{"parts":[]},"finishReason":"FINISH_REASON_UNSPECIFIED"}]}`))
			case "unknown":
				f.bodies[1].reader = strings.NewReader(geminiRecoverySSE(`{"error":null,"candidates":[{"content":{"parts":[]},"finishReason":"UNKNOWN"}]}`))
			case "api error":
				f.bodies[1].reader = strings.NewReader(geminiRecoverySSE(`{"error":{"code":500,"message":"unavailable"}}`))
			case "read":
				f.bodies[1].reader = iotest.ErrReader(sentinel)
			}
			output, err := f.stream(t)
			if err == nil || len(f.requests) != 2 || strings.Contains(output, "event: message_stop\n") {
				t.Fatalf("failed recovery was not terminal: requests=%d error=%v output=%s", len(f.requests), err, output)
			}
			if kind == "truncated http" && (clienterror.HTTPStatusFromError(err) != http.StatusTooManyRequests || !errors.Is(err, io.ErrUnexpectedEOF)) {
				t.Fatalf("truncated response lost status or cause: %v", err)
			}
			if kind == "read" && !errors.Is(err, sentinel) {
				t.Fatalf("read error lost: %v", err)
			}
			if !f.bodies[0].closed || !f.bodies[1].closed {
				t.Fatalf("response body leak: closed=%v/%v", f.bodies[0].closed, f.bodies[1].closed)
			}
		})
	}
}

func TestGeminiExecutorMalformedRecoveryCancelledBeforeRetry(t *testing.T) {
	f := newGeminiRecoveryFixture(t, geminiRecoverySSE(geminiRecoveryFinish, geminiRecoveryUsage))
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.ctx = ctx
	f.bodies[0].onEOF = cancel
	_, err := f.stream(t)
	if !errors.Is(err, context.Canceled) || len(f.requests) != 1 || !f.bodies[0].closed {
		t.Fatalf("cancelled request continued: requests=%d closed=%v error=%v", len(f.requests), f.bodies[0].closed, err)
	}
}

func TestGeminiExecutorMalformedRecoveryRespectsBootstrapBudget(t *testing.T) {
	failure := geminiRecoverySSE(geminiRecoveryFinish, geminiRecoveryUsage)
	f := newGeminiRecoveryFixture(t, failure, "", failure, "")
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(f.exec)
	f.auth.Status = cliproxyauth.StatusActive
	f.auth.Metadata = map[string]any{"disable_cooling": true}
	if _, err := manager.Register(f.ctx, f.auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(f.auth.ID, "gemini", []*registry.ModelInfo{{ID: f.req.Model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(f.auth.ID) })
	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}}, manager)
	data, _, failures := handler.ExecuteStreamWithAuthManager(f.ctx, "claude", f.req.Model, f.req.Payload, "")
	if data == nil || failures == nil {
		t.Fatal("gateway did not start the stream")
	}
	for chunk := range data {
		if len(chunk) != 0 {
			t.Errorf("failed attempts leaked payload: %s", chunk)
		}
	}
	terminalErrors := 0
	for failure := range failures {
		if failure != nil {
			terminalErrors++
		}
	}
	if len(f.requests) != 4 || terminalErrors != 1 {
		t.Fatalf("requests=%d terminal errors=%d; want four requests and one terminal error", len(f.requests), terminalErrors)
	}
}

func TestGeminiExecutorMalformedRecoveryStopsAfterSecondFailure(t *testing.T) {
	failure := geminiRecoverySSE(geminiRecoveryFinish, geminiRecoveryUsage)
	f := newGeminiRecoveryFixture(t, failure, failure)
	output, err := f.stream(t)
	if len(f.requests) != 2 || err == nil {
		t.Fatalf("requests = %d, error = %v; want two requests and terminal error", len(f.requests), err)
	}
	if status := clienterror.HTTPStatusFromError(err); status != http.StatusBadGateway {
		t.Fatalf("terminal status = %d, want 502", status)
	}
	if strings.Count(output, "event: error\n") != 1 || strings.Count(output, "event: message_start\n") > 1 {
		t.Fatalf("attempts leaked duplicate error or handshake events: %s", output)
	}
	assertMalformedFunctionCallFailedUsage(t, f.records)
	assertMalformedFunctionCallFailedUsage(t, f.records)
}
