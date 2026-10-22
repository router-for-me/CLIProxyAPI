package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const executorPatchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}],"input":"patch a file"}`
const executorPatchResponse = `{"responseId":"patch","candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":"  *** Begin Patch\n*** End Patch\n "}}}]},"finishReason":"STOP"}]}`
const executorPatchInput = "  *** Begin Patch\n*** End Patch\n "

func assertExecutorPatchDeclaration(t *testing.T, payload []byte) {
	t.Helper()
	declaration := gjson.GetBytes(payload, "tools.0.functionDeclarations.0")
	if declaration.Get("name").String() != "functions__apply_patch" || !strings.Contains(declaration.Get("description").String(), "*** Begin Patch") || !strings.Contains(declaration.Get("description").String(), "start: patch") || declaration.Get("parametersJsonSchema.properties.input.type").String() != "string" || !declaration.Get("parametersJsonSchema.additionalProperties").Exists() || declaration.Get("parametersJsonSchema.additionalProperties").Bool() {
		t.Fatalf("missing standard patch declaration: %s", payload)
	}
}
func assertExecutorPatchOutput(t *testing.T, payload []byte) {
	t.Helper()
	item := gjson.GetBytes(payload, `output.#(type=="custom_tool_call")`)
	if item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != executorPatchInput || item.Get("name").String() != "apply_patch" || item.Get("namespace").String() != "functions" {
		t.Fatalf("wrong patch output: %s", payload)
	}
}
func assertExecutorPatchStream(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk, acknowledgeSnapshot ...func()) {
	t.Helper()
	acknowledged := len(acknowledgeSnapshot) == 0
	var delta strings.Builder
	var done, item, final gjson.Result
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			ev := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch ev.Get("type").String() {
			case "response.output_text.delta":
				if !acknowledged {
					acknowledged = true
					acknowledgeSnapshot[0]()
				}
			case "response.custom_tool_call_input.delta":
				if !acknowledged {
					t.Fatal("fabricated patch progress before upstream snapshot")
				}
				delta.WriteString(ev.Get("delta").String())
			case "response.custom_tool_call_input.done":
				done = ev
			case "response.output_item.done":
				if ev.Get("item.type").String() == "custom_tool_call" {
					item = ev.Get("item")
				}
			case "response.completed":
				final = ev.Get("response")
			}
		}
	}
	if !acknowledged || delta.String() != executorPatchInput || done.Get("input").String() != executorPatchInput || item.Get("input").String() != executorPatchInput || done.Get("item_id").String() != item.Get("id").String() || done.Get("call_id").String() != item.Get("call_id").String() {
		t.Fatalf("inconsistent stream delta=%q done=%s item=%s", delta.String(), done.Raw, item.Raw)
	}
	assertExecutorPatchOutput(t, []byte(final.Raw))
}

func TestGeminiVertexApplyPatchExecutorReuse(t *testing.T) {
	requests := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errReadAll := io.ReadAll(r.Body)
		if errReadAll != nil {
			t.Error(errReadAll)
			w.WriteHeader(500)
			return
		}
		requests <- body
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+executorPatchResponse+"\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, executorPatchResponse)
		}
	}))
	defer server.Close()
	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "vertex-patch", Provider: "vertex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(executorPatchRequest)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}
	response, errExecute := exec.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	assertExecutorPatchDeclaration(t, <-requests)
	assertExecutorPatchOutput(t, response.Payload)
	stream, errExecuteStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}
	assertExecutorPatchDeclaration(t, <-requests)
	assertExecutorPatchStream(t, stream.Chunks)
}

func TestGeminiVertexExecutorClaudeMalformedFunctionCallReturnsRetryableError(t *testing.T) {
	malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	for _, authMode := range []string{"api_key", "service_account"} {
		t.Run(authMode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+malformedBody+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, malformedBody)
			}))
			defer server.Close()

			ctx := context.Background()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
			if authMode == "api_key" {
				auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
			} else {
				var serviceAccount map[string]any
				if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
					t.Fatal(errUnmarshal)
				}
				auth.Metadata = map[string]any{
					"project_id":      "proxy-test",
					"location":        "global",
					"service_account": serviceAccount,
				}
				target, errParse := parseTestURL(t, server.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				t.Cleanup(transport.CloseIdleConnections)
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
			}

			capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 2)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
			})

			exec := NewGeminiVertexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: []byte(`{"model":"gemini-4-argon","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			}

			_, errExec := exec.Execute(ctx, auth, req, opts)
			if errExec == nil {
				t.Fatal("Execute() error = nil, want retryable 502 error for MALFORMED_FUNCTION_CALL")
			}
			status, okStatus := errExec.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
			}
			if got := gjson.Get(errExec.Error(), "error.type").String(); got != "api_error" {
				t.Fatalf("Execute() error.type = %q, want api_error. Error: %s", got, errExec.Error())
			}
			assertMalformedFunctionCallFailedUsage(t, capture.records)

			streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var combined bytes.Buffer
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
					continue
				}
				combined.Write(chunk.Payload)
			}
			if streamErr == nil {
				t.Fatal("ExecuteStream() chunk.Err = nil, want 502 error propagated")
			}
			streamText := combined.String()
			if strings.Contains(streamText, `"stop_reason":"refusal"`) {
				t.Fatalf("stream unexpectedly emitted stop_reason refusal:\n%s", streamText)
			}
			if !strings.Contains(streamText, "event: error\n") || !strings.Contains(streamText, `"type":"api_error"`) {
				t.Fatalf("stream missing retryable api_error event:\n%s", streamText)
			}
			assertMalformedFunctionCallFailedUsage(t, capture.records)
		})
	}
}

func TestGeminiVertexExecutorClaudeMalformedFunctionCallCancelAfterErrorRecordsFailure(t *testing.T) {
	malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443},"modelVersion":"gemini-4-argon"}`
	for _, authMode := range []string{"api_key", "service_account"} {
		t.Run(authMode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+malformedBody+"\n\n")
				if flusher, okFlush := w.(http.Flusher); okFlush {
					flusher.Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()

			ctx := context.Background()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex", Status: cliproxyauth.StatusActive}
			if authMode == "api_key" {
				auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
			} else {
				var serviceAccount map[string]any
				if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
					t.Fatal(errUnmarshal)
				}
				auth.Metadata = map[string]any{
					"project_id":      "proxy-test",
					"location":        "global",
					"service_account": serviceAccount,
				}
				target, errParse := parseTestURL(t, server.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				t.Cleanup(transport.CloseIdleConnections)
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
			}

			capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
			})

			exec := NewGeminiVertexExecutor(&config.Config{})
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(exec)
			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			streamCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			req := cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: []byte(`{"model":"gemini-4-argon","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			}

			streamRes, errStream := manager.ExecuteStream(streamCtx, []string{"vertex"}, req, opts)
			if errStream != nil {
				t.Fatalf("manager.ExecuteStream() error = %v", errStream)
			}
			for chunk := range streamRes.Chunks {
				if strings.Contains(string(chunk.Payload), "event: error\n") {
					cancel()
				}
			}
			assertMalformedFunctionCallFailedUsage(t, capture.records)
			updated, okAuth := manager.GetByID(auth.ID)
			if !okAuth || updated.ModelStates["gemini-4-argon"] == nil || updated.ModelStates["gemini-4-argon"].LastError == nil {
				t.Fatalf("expected conductor to record failure on model state after client cancel on error event, got %+v", updated)
			}
			if updated.ModelStates["gemini-4-argon"].LastError.HTTPStatus != http.StatusBadGateway {
				t.Fatalf("LastError.HTTPStatus = %d, want 502; err=%+v", updated.ModelStates["gemini-4-argon"].LastError.HTTPStatus, updated.ModelStates["gemini-4-argon"].LastError)
			}
		})
	}
}

// TestVertexAPIKeyModelsURL pinpoints the exact URL construction rule for
// API-key mode: the configured base URL is honored as a full resource prefix
// when it already contains "/publishers/", otherwise "/v1/publishers/google/models"
// is inserted for it. Keeps third-party short hosts (zenmux-style) working
// while allowing real Vertex paths that embed projects/locations.
func TestVertexAPIKeyModelsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		model   string
		action  string
		want    string
	}{
		{
			name:    "empty falls back to default Vertex host with /v1",
			baseURL: "",
			model:   "gemini-4-argon",
			action:  "streamGenerateContent",
			want:    "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-4-argon:streamGenerateContent",
		},
		{
			name:    "bare host gets /v1/publishers/google/models appended",
			baseURL: "https://zenmux.ai",
			model:   "gemini-2.5-flash",
			action:  "generateContent",
			want:    "https://zenmux.ai/v1/publishers/google/models/gemini-2.5-flash:generateContent",
		},
		{
			name:    "host with trailing slash still gets /v1/...",
			baseURL: "https://zenmux.ai/",
			model:   "gemini-2.5-flash",
			action:  "streamGenerateContent",
			want:    "https://zenmux.ai/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
		},
		{
			name:    "third-party host including /v1 keeps its version and appends suffix",
			baseURL: "https://zenmux.ai/v1",
			model:   "gemini-2.5-flash",
			action:  "streamGenerateContent",
			want:    "https://zenmux.ai/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
		},
		{
			name:    "third-party already encoding /publishers/ is used as-is",
			baseURL: "https://api.zenmux.ai/v1/publishers/google/models",
			model:   "gemini-2.5-flash",
			action:  "streamGenerateContent",
			want:    "https://api.zenmux.ai/v1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
		},
		{
			name:    "real Vertex with projects and locations is used as-is (no double /v1)",
			baseURL: "https://aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models",
			model:   "gemini-4-argon",
			action:  "streamGenerateContent",
			want:    "https://aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models/gemini-4-argon:streamGenerateContent",
		},
		{
			name:    "real Vertex with projects and locations uses countTokens action",
			baseURL: "https://aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models",
			model:   "gemini-4-argon",
			action:  "countTokens",
			want:    "https://aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models/gemini-4-argon:countTokens",
		},
		{
			name:    "custom location with regional endpoint preserved verbatim",
			baseURL: "https://us-central1-aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models",
			model:   "gemini-4-argon",
			action:  "streamGenerateContent",
			want:    "https://us-central1-aiplatform.googleapis.com/v1/projects/279118250380/locations/us-central1/publishers/google/models/gemini-4-argon:streamGenerateContent",
		},
		{
			name:    "v1beta1 full prefix is preserved (no forced /v1)",
			baseURL: "https://aiplatform.googleapis.com/v1beta1/projects/279118250380/locations/us-central1/publishers/google/models",
			model:   "gemini-4-argon",
			action:  "streamGenerateContent",
			want:    "https://aiplatform.googleapis.com/v1beta1/projects/279118250380/locations/us-central1/publishers/google/models/gemini-4-argon:streamGenerateContent",
		},
		{
			name:    "whitespace around base URL is trimmed",
			baseURL: "  https://zenmux.ai  ",
			model:   "gemini-2.5-flash",
			action:  "generateContent",
			want:    "https://zenmux.ai/v1/publishers/google/models/gemini-2.5-flash:generateContent",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := vertexAPIKeyModelsURL(tc.baseURL, tc.model, tc.action)
			if got != tc.want {
				t.Errorf("vertexAPIKeyModelsURL(%q, %q, %q)\n  got: %q\n want: %q", tc.baseURL, tc.model, tc.action, got, tc.want)
			}
		})
	}
}

func TestVertexBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		location string
		want     string
	}{
		{"", "https://us-central1-aiplatform.googleapis.com"},
		{"global", "https://aiplatform.googleapis.com"},
		{"us-central1", "https://us-central1-aiplatform.googleapis.com"},
		{"us", "https://aiplatform.us.rep.googleapis.com"},
		{"eu", "https://aiplatform.eu.rep.googleapis.com"},
		{" US ", "https://aiplatform.us.rep.googleapis.com"},
		{" EU ", "https://aiplatform.eu.rep.googleapis.com"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.location, func(t *testing.T) {
			t.Parallel()
			if got := vertexBaseURL(tc.location); got != tc.want {
				t.Errorf("vertexBaseURL(%q) = %q, want %q", tc.location, got, tc.want)
			}
		})
	}
}
