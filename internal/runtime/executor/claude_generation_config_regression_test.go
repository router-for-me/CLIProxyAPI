package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

func TestAntigravityClaudeFormatOutputConfigAndStopSequences(t *testing.T) {
	wantSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
		},
		"required":             []any{"title", "count"},
		"additionalProperties": false,
	}

	rawClaudePayload := []byte(`{
		"model": "claude-sonnet-5-5-high",
		"max_tokens": 1024,
		"stop_sequences": ["END_MARKER"],
		"tools": [{
			"name": "get_time",
			"description": "Get time",
			"input_schema": {
				"type": "object",
				"properties": {"tz": {"type": "string"}},
				"required": ["tz"]
			}
		}],
		"tool_choice": {"type": "auto"},
		"output_config": {
			"format": {
				"type": "json_schema",
				"schema": {
					"type": "object",
					"properties": {
						"title": {"type": "string"},
						"count": {"type": "integer"}
					},
					"required": ["title", "count"],
					"additionalProperties": false
				}
			}
		},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	t.Run("claude-5-5 falls back to prompt instruction and preserves stopSequences", func(t *testing.T) {
		for _, model := range []string{"claude-opus-5-5-high", "claude-sonnet-5-5-high"} {
			t.Run(model, func(t *testing.T) {
				translated := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, model, rawClaudePayload, false)
				body := buildRequestBodyFromRawPayload(t, model, translated)
				encoded, errMarshal := json.Marshal(body)
				if errMarshal != nil {
					t.Fatalf("marshal outbound request: %v", errMarshal)
				}
				request := ojson.GetBytes(encoded, "request")

				if got := request.Get("generationConfig.stopSequences.0").String(); got != "END_MARKER" {
					t.Fatalf("stopSequences[0] = %q, want END_MARKER; body=%s", got, encoded)
				}
				if mode := request.Get("toolConfig.functionCallingConfig.mode").String(); mode != "" && mode != "AUTO" {
					t.Fatalf("toolConfig mode = %q, want AUTO or omitted for Claude 5.5; body=%s", mode, encoded)
				}
				for _, field := range []string{"responseSchema", "responseJsonSchema", "responseMimeType"} {
					if got := request.Get("generationConfig." + field); got.Exists() {
						t.Fatalf("generationConfig.%s = %s, want omitted after Claude 5.5 prompt fallback", field, got.Raw)
					}
				}

				var systemText strings.Builder
				for _, part := range request.Get("systemInstruction.parts").Array() {
					systemText.WriteString(part.Get("text").String())
					systemText.WriteByte('\n')
				}
				prompt := systemText.String()
				foundSchema := false
				for offset, ch := range prompt {
					if ch != '{' {
						continue
					}
					var decoded map[string]any
					if errDecode := json.NewDecoder(strings.NewReader(prompt[offset:])).Decode(&decoded); errDecode == nil && reflect.DeepEqual(decoded, wantSchema) {
						foundSchema = true
						break
					}
				}
				if !foundSchema {
					t.Fatalf("systemInstruction missing structured output schema; prompt=%q", prompt)
				}
			})
		}
	})

	t.Run("claude-5-5 does not leak responseMimeType when schema is missing", func(t *testing.T) {
		model := "claude-sonnet-5-5-high"
		invalidFormat := []byte(`{
			"model": "claude-sonnet-5-5-high",
			"max_tokens": 1024,
			"output_config": {"format": {"type": "json_schema"}},
			"messages": [{"role": "user", "content": "hi"}]
		}`)
		translated := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, model, invalidFormat, false)
		body := buildRequestBodyFromRawPayload(t, model, translated)
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatalf("marshal outbound request: %v", errMarshal)
		}
		request := ojson.GetBytes(encoded, "request")
		for _, field := range []string{"responseSchema", "responseJsonSchema", "responseMimeType"} {
			if got := request.Get("generationConfig." + field); got.Exists() {
				t.Fatalf("generationConfig.%s = %s, want omitted when schema is absent", field, got.Raw)
			}
		}
	})

	t.Run("antigravity gemini preserves cleaned responseSchema and stopSequences", func(t *testing.T) {
		model := "gemini-3.8-flash-high"
		translated := sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatAntigravity, model, rawClaudePayload, false)
		body := buildRequestBodyFromRawPayload(t, model, translated)
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatalf("marshal outbound request: %v", errMarshal)
		}
		request := ojson.GetBytes(encoded, "request")

		if got := request.Get("generationConfig.stopSequences.0").String(); got != "END_MARKER" {
			t.Fatalf("stopSequences[0] = %q, want END_MARKER; body=%s", got, encoded)
		}
		if got := request.Get("generationConfig.responseMimeType").String(); got != "application/json" {
			t.Fatalf("responseMimeType = %q, want application/json; body=%s", got, encoded)
		}
		if request.Get("generationConfig.responseJsonSchema").Exists() {
			t.Fatalf("responseJsonSchema must not be sent to Antigravity; body=%s", encoded)
		}
		if got := request.Get("generationConfig.responseSchema.properties.title.type").String(); !strings.EqualFold(got, "string") {
			t.Fatalf("responseSchema.properties.title.type = %q, want string/STRING; body=%s", got, encoded)
		}
	})
}

func TestGeminiAndVertexExecutors_ClaudeGenerationConfigAndMaxTokens(t *testing.T) {
	claudePayload := []byte(`{
		"model": "gemini-3.1-pro-preview",
		"max_tokens": 500000,
		"stop_sequences": ["STOP_NOW"],
		"output_config": {
			"format": {
				"type": "json_schema",
				"schema": {
					"type": "object",
					"properties": {"answer": {"type": "string"}},
					"required": ["answer"],
					"additionalProperties": false
				}
			}
		},
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	geminiResp := `{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"answer\":\"ok\"}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	if !ojson.Valid(geminiResp) {
		t.Fatalf("invalid test fixture geminiResp: %s", geminiResp)
	}

	assertOutbound := func(t *testing.T, upstreamBody []byte, wantMax int64) {
		t.Helper()
		genCfg := ojson.GetBytes(upstreamBody, "generationConfig")
		if got := genCfg.Get("maxOutputTokens").Int(); got != wantMax {
			t.Fatalf("maxOutputTokens = %d, want %d; body=%s", got, wantMax, upstreamBody)
		}
		if got := genCfg.Get("stopSequences.0").String(); got != "STOP_NOW" {
			t.Fatalf("stopSequences[0] = %q, want STOP_NOW; body=%s", got, upstreamBody)
		}
		if got := genCfg.Get("responseMimeType").String(); got != "application/json" {
			t.Fatalf("responseMimeType = %q, want application/json; body=%s", got, upstreamBody)
		}
		if got := genCfg.Get("responseJsonSchema.properties.answer.type").String(); got != "string" {
			t.Fatalf("responseJsonSchema.properties.answer.type = %q, want string; body=%s", got, upstreamBody)
		}
	}

	collectClaudeStreamText := func(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) string {
		t.Helper()
		var text strings.Builder
		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
			for _, line := range strings.Split(string(chunk.Payload), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if ojson.Get(data, "type").String() == "content_block_delta" && ojson.Get(data, "delta.type").String() == "text_delta" {
					text.WriteString(ojson.Get(data, "delta.text").String())
				}
			}
		}
		return text.String()
	}

	readCapturedBody := func(t *testing.T, captured <-chan []byte) []byte {
		t.Helper()
		select {
		case body := <-captured:
			return body
		default:
			t.Fatal("expected upstream request body to be captured")
			return nil
		}
	}

	t.Run("gemini executor maps fields and caps max_tokens", func(t *testing.T) {
		for _, stream := range []bool{false, true} {
			name := "execute"
			if stream {
				name = "stream"
			}
			t.Run(name, func(t *testing.T) {
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request body: %v", errRead)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					select {
					case captured <- append([]byte(nil), body...):
					default:
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: " + geminiResp + "\n\n"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(geminiResp))
				}))
				defer server.Close()

				exec := NewGeminiExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				}}
				req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: claudePayload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: stream}
				if stream {
					res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					if got := collectClaudeStreamText(t, res.Chunks); got != `{"answer":"ok"}` {
						t.Fatalf("stream text = %q, want {\"answer\":\"ok\"}", got)
					}
				} else {
					resp, errExec := exec.Execute(context.Background(), auth, req, opts)
					if errExec != nil {
						t.Fatalf("Execute() error = %v", errExec)
					}
					if got := ojson.GetBytes(resp.Payload, "content.0.text").String(); got != `{"answer":"ok"}` {
						t.Fatalf("response text = %q, want {\"answer\":\"ok\"}; payload=%s", got, resp.Payload)
					}
				}
				assertOutbound(t, readCapturedBody(t, captured), 65536)
			})
		}
	})

	t.Run("gemini interactions executor maps fields without silently capping max_tokens, and keeps payload override final", func(t *testing.T) {
		interactionsResp := `{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"{\"answer\":\"ok\"}"}]}],"usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}`
		interactionsSSE := strings.Join([]string{
			`event: interaction.created`,
			`data: {"event_type":"interaction.created","interaction":{"id":"interaction_1","model":"gemini-4-neon"}}`,
			``,
			`event: step.start`,
			`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
			``,
			`event: step.delta`,
			`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"{\"answer\":\"ok\"}"}}`,
			``,
			`event: step.stop`,
			`data: {"event_type":"step.stop","index":0}`,
			``,
			`event: interaction.completed`,
			`data: {"event_type":"interaction.completed","interaction":{"id":"interaction_1","model":"gemini-4-neon","usage":{"total_input_tokens":1,"total_output_tokens":1}}}`,
			``,
			`event: done`,
			`data: [DONE]`,
			``,
			``,
		}, "\n")

		for _, tc := range []struct {
			name     string
			stream   bool
			override int64
			wantMax  int64
		}{
			{name: "execute_preserves_requested_max", stream: false, wantMax: 500000},
			{name: "stream_preserves_requested_max", stream: true, wantMax: 500000},
			{name: "execute_payload_override_final", stream: false, override: 123456, wantMax: 123456},
			{name: "stream_payload_override_final", stream: true, override: 123456, wantMax: 123456},
		} {
			t.Run(tc.name, func(t *testing.T) {
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request body: %v", errRead)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					select {
					case captured <- append([]byte(nil), body...):
					default:
					}
					if tc.stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, interactionsSSE)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, interactionsResp)
				}))
				defer server.Close()

				cfg := &config.Config{}
				if tc.override > 0 {
					cfg.Payload = config.PayloadConfig{
						Override: []config.PayloadRule{{
							Models: []config.PayloadModelRule{{Name: "gemini-4-neon", Protocol: "interactions"}},
							Params: map[string]any{"generation_config.max_output_tokens": tc.override},
						}},
					}
				}
				exec := NewGeminiInteractionsExecutor(cfg)
				auth := &cliproxyauth.Auth{
					Provider: "gemini-interactions",
					Attributes: map[string]string{
						"api_key":  "test-key",
						"base_url": server.URL,
					},
				}
				req := cliproxyexecutor.Request{Model: "gemini-4-neon", Payload: claudePayload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: tc.stream}
				if tc.stream {
					res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					if got := collectClaudeStreamText(t, res.Chunks); got != `{"answer":"ok"}` {
						t.Fatalf("stream text = %q, want {\"answer\":\"ok\"}", got)
					}
				} else {
					resp, errExec := exec.Execute(context.Background(), auth, req, opts)
					if errExec != nil {
						t.Fatalf("Execute() error = %v", errExec)
					}
					if got := ojson.GetBytes(resp.Payload, "content.0.text").String(); got != `{"answer":"ok"}` {
						t.Fatalf("response text = %q, want {\"answer\":\"ok\"}; payload=%s", got, resp.Payload)
					}
				}

				upstreamBody := readCapturedBody(t, captured)
				if got := ojson.GetBytes(upstreamBody, "generation_config.max_output_tokens").Int(); got != tc.wantMax {
					t.Fatalf("generation_config.max_output_tokens = %d, want %d; body=%s", got, tc.wantMax, upstreamBody)
				}
				if got := ojson.GetBytes(upstreamBody, "generation_config.stop_sequences.0").String(); got != "STOP_NOW" {
					t.Fatalf("generation_config.stop_sequences[0] = %q, want STOP_NOW; body=%s", got, upstreamBody)
				}
				if got := ojson.GetBytes(upstreamBody, "response_format.type").String(); got != "text" {
					t.Fatalf("response_format.type = %q, want text; body=%s", got, upstreamBody)
				}
				if got := ojson.GetBytes(upstreamBody, "response_format.mime_type").String(); got != "application/json" {
					t.Fatalf("response_format.mime_type = %q, want application/json; body=%s", got, upstreamBody)
				}
				if got := ojson.GetBytes(upstreamBody, "response_format.schema.properties.answer.type").String(); got != "string" {
					t.Fatalf("response_format.schema.properties.answer.type = %q, want string; body=%s", got, upstreamBody)
				}
			})
		}
	})

	t.Run("vertex executor maps fields without silently capping max_tokens, and keeps payload override final", func(t *testing.T) {
		for _, authMode := range []string{"api_key", "service_account"} {
			for _, tc := range []struct {
				name     string
				stream   bool
				override int64
				wantMax  int64
			}{
				{name: "execute_preserves_requested_max", stream: false, wantMax: 500000},
				{name: "stream_preserves_requested_max", stream: true, wantMax: 500000},
				{name: "execute_payload_override_final", stream: false, override: 123456, wantMax: 123456},
				{name: "stream_payload_override_final", stream: true, override: 123456, wantMax: 123456},
			} {
				t.Run(authMode+"/"+tc.name, func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Errorf("read request body: %v", errRead)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						select {
						case captured <- append([]byte(nil), body...):
						default:
						}
						if tc.stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("data: " + geminiResp + "\n\n"))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(geminiResp))
					}))
					defer server.Close()

					cfg := &config.Config{}
					if tc.override > 0 {
						cfg.Payload = config.PayloadConfig{
							Override: []config.PayloadRule{{
								Models: []config.PayloadModelRule{{Name: "gemini-3.1-pro-preview", Protocol: "gemini"}},
								Params: map[string]any{"generationConfig.maxOutputTokens": tc.override},
							}},
						}
					}
					exec := NewGeminiVertexExecutor(cfg)
					ctx := context.Background()
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
					if authMode == "api_key" {
						auth.Attributes = map[string]string{
							"api_key":  "test-vertex-key",
							"base_url": server.URL,
						}
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

					req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: claudePayload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: tc.stream}
					if tc.stream {
						res, errStream := exec.ExecuteStream(ctx, auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						if got := collectClaudeStreamText(t, res.Chunks); got != `{"answer":"ok"}` {
							t.Fatalf("stream text = %q, want {\"answer\":\"ok\"}", got)
						}
					} else {
						resp, errExec := exec.Execute(ctx, auth, req, opts)
						if errExec != nil {
							t.Fatalf("Execute() error = %v", errExec)
						}
						if got := ojson.GetBytes(resp.Payload, "content.0.text").String(); got != `{"answer":"ok"}` {
							t.Fatalf("response text = %q, want {\"answer\":\"ok\"}; payload=%s", got, resp.Payload)
						}
					}
					assertOutbound(t, readCapturedBody(t, captured), tc.wantMax)
				})
			}
		}
	})
}
