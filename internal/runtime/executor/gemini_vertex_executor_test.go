package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const executorPatchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}],"input":"patch a file"}`
const executorPatchResponse = `{"responseId":"patch","candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":"  *** Begin Patch\n*** End Patch\n "}}}]},"finishReason":"STOP"}]}`
const executorPatchInput = "  *** Begin Patch\n*** End Patch\n "

func assertExecutorPatchDeclaration(t *testing.T, payload []byte) {
	t.Helper()
	declaration := ojson.GetBytes(payload, "tools.0.functionDeclarations.0")
	if declaration.Get("name").String() != "functions__apply_patch" || !strings.Contains(declaration.Get("description").String(), "*** Begin Patch") || !strings.Contains(declaration.Get("description").String(), "start: patch") || declaration.Get("parametersJsonSchema.properties.input.type").String() != "string" || !declaration.Get("parametersJsonSchema.additionalProperties").Exists() || declaration.Get("parametersJsonSchema.additionalProperties").Bool() {
		t.Fatalf("missing standard patch declaration: %s", payload)
	}
}
func assertExecutorPatchOutput(t *testing.T, payload []byte) {
	t.Helper()
	item := ojson.GetBytes(payload, `output.#(type=="custom_tool_call")`)
	if item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != executorPatchInput || item.Get("name").String() != "apply_patch" || item.Get("namespace").String() != "functions" {
		t.Fatalf("wrong patch output: %s", payload)
	}
}
func assertExecutorPatchStream(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk, acknowledgeSnapshot ...func()) {
	t.Helper()
	acknowledged := len(acknowledgeSnapshot) == 0
	var delta strings.Builder
	var done, item, final ojson.Result
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			ev := ojson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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
			if got := ojson.Get(errExec.Error(), "error.type").String(); got != "api_error" {
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
			if strings.TrimSpace(streamText) != "" {
				t.Fatalf("stream unexpectedly emitted payload before bootstrap retryable error:\n%s", streamText)
			}
			streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
			if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
			}
			if got := ojson.Get(streamErr.Error(), "error.type").String(); got != "api_error" {
				t.Fatalf("ExecuteStream() chunk.Err error.type = %q, want api_error; err=%s", got, streamErr.Error())
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
			resultHook := &malformedFunctionCallResultHook{authID: auth.ID, results: make(chan cliproxyauth.Result, 1)}
			manager := cliproxyauth.NewManager(nil, nil, resultHook)
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
			assertMalformedFunctionCallConductorResultUncooled(t, manager, auth.ID, "gemini-4-argon", resultHook.results)
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

func TestGeminiVertexExecutorPromptFeedbackBlockWithoutCandidatesReturnsError(t *testing.T) {
	blockedBody := `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":413751,"totalTokenCount":413751},"modelVersion":"gemini-4-argon"}`
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
					_, _ = io.WriteString(w, "data: "+blockedBody+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, blockedBody)
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
				Payload: []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatOpenAI,
			}

			_, errExec := exec.Execute(ctx, auth, req, opts)
			if errExec == nil {
				t.Fatal("Execute() error = nil, want 502 prompt block error")
			}
			status, okStatus := errExec.(interface{ StatusCode() int })
			if !okStatus || status.StatusCode() != http.StatusBadGateway {
				t.Fatalf("Execute() error = %T %v, want status 502", errExec, errExec)
			}
			if !strings.Contains(errExec.Error(), "upstream blocked the prompt: SAFETY - The prompt is blocked due to safety") {
				t.Fatalf("Execute() error = %q, want prompt block reason and message", errExec.Error())
			}

			streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var streamErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil {
				t.Fatal("ExecuteStream() chunk.Err = nil, want 502 prompt block error")
			}
			streamStatus, okStreamStatus := streamErr.(interface{ StatusCode() int })
			if !okStreamStatus || streamStatus.StatusCode() != http.StatusBadGateway {
				t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
			}
			if !strings.Contains(streamErr.Error(), "upstream blocked the prompt: SAFETY - The prompt is blocked due to safety") {
				t.Fatalf("ExecuteStream() chunk.Err = %q, want prompt block reason and message", streamErr.Error())
			}

			for i := 0; i < 2; i++ {
				record := <-capture.records
				if !record.Failed || record.Detail.InputTokens != 413751 {
					t.Fatalf("usage record[%d] = %+v, want Failed=true InputTokens=413751", i, record)
				}
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
		{"", "https://aiplatform.googleapis.com"},
		{"   ", "https://aiplatform.googleapis.com"},
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

func TestGeminiVertexExecutor_AliasedHydrogenPreservesNativeThinkingLevelWhenShadowed(t *testing.T) {
	for _, shadowCase := range []struct {
		name      string
		shadowMod *registry.ModelInfo
	}{
		{
			name: "shadowed_by_user_defined_model",
			shadowMod: &registry.ModelInfo{
				ID:          "gemini-4-hydrogen",
				Type:        "gemini",
				UserDefined: true,
			},
		},
		{
			name: "shadowed_by_non_thinking_model",
			shadowMod: &registry.ModelInfo{
				ID:          "gemini-4-hydrogen",
				Type:        "openai",
				UserDefined: false,
				Thinking:    nil,
			},
		},
	} {
		for _, level := range []string{"medium", "xhigh"} {
			t.Run(shadowCase.name+"_"+level, func(t *testing.T) {
				capturedBodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					capturedBodies <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-4-hydrogen\"}\n\n")
				}))
				defer server.Close()

				serviceAccount, okSA := ojson.ParseBytes(testVertexServiceAccountJSON(t, server.URL+"/token")).Value().(map[string]any)
				if !okSA {
					t.Fatal("failed to parse test service account JSON into map")
				}
				auth := &cliproxyauth.Auth{
					ID:       t.Name() + "-vertex",
					Provider: "vertex",
					Status:   cliproxyauth.StatusActive,
					Metadata: map[string]any{
						"project_id":      "proxy-test",
						"location":        "global",
						"service_account": serviceAccount,
					},
				}

				target, errParse := parseTestURL(t, server.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				t.Cleanup(transport.CloseIdleConnections)
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})

				exec := NewGeminiVertexExecutor(&config.Config{})
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
					"vertex": {{Name: "gemini-4-hydrogen", Alias: "vertex/gemini-4-hydrogen", Fork: false}},
				})
				manager.RegisterExecutor(exec)
				if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
					t.Fatalf("Register() error = %v", errRegister)
				}

				reg := registry.GetGlobalRegistry()
				shadowClientID := t.Name() + "-shadow"
				reg.RegisterClient(auth.ID, "vertex", []*registry.ModelInfo{{
					ID:       "vertex/gemini-4-hydrogen",
					Type:     "gemini",
					Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
				}})
				reg.RegisterClient(shadowClientID, "other-plugin", []*registry.ModelInfo{shadowCase.shadowMod})
				t.Cleanup(func() {
					reg.UnregisterClient(auth.ID)
					reg.UnregisterClient(shadowClientID)
				})

				req := cliproxyexecutor.Request{
					Model:   "vertex/gemini-4-hydrogen",
					Payload: []byte(`{"generationConfig":{"maxOutputTokens":1030323,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatGemini,
					ResponseFormat: sdktranslator.FormatGemini,
					Stream:         true,
				}

				streamRes, errStream := manager.ExecuteStream(ctx, []string{"vertex"}, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}

				upstreamBody := <-capturedBodies
				if got := ojson.GetBytes(upstreamBody, "generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
					t.Fatalf("upstream thinkingLevel = %q, want %q; body=%s", got, level, upstreamBody)
				}
				if ojson.GetBytes(upstreamBody, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
					t.Fatalf("unexpected thinkingBudget in upstream Vertex payload: %s", upstreamBody)
				}
				if !ojson.GetBytes(upstreamBody, "generationConfig.thinkingConfig.includeThoughts").Bool() {
					t.Fatalf("expected includeThoughts=true in upstream Vertex payload: %s", upstreamBody)
				}
			})
		}
	}
}
