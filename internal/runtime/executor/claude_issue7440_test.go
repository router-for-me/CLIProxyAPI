package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutor_CloakedOAuthOmitsServerSideFallbackByDefault_Issue7440(t *testing.T) {
	models := []string{"claude-opus-5-5", "claude-fable-5-1"}
	formats := []struct {
		name    string
		format  sdktranslator.Format
		payload func(model string) []byte
	}{
		{
			name:   "openai-chat",
			format: sdktranslator.FormatOpenAI,
			payload: func(model string) []byte {
				return []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))
			},
		},
		{
			name:   "claude-messages",
			format: sdktranslator.FormatClaude,
			payload: func(model string) []byte {
				return []byte(fmt.Sprintf(`{"model":%q,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hello"}]}`, model))
			},
		},
	}

	for _, model := range models {
		for _, source := range formats {
			for _, stream := range []bool{false, true} {
				mode := "execute"
				if stream {
					mode = "stream"
				}
				t.Run(model+"/"+source.name+"/"+mode, func(t *testing.T) {
					var seenBody []byte
					var seenHeaders http.Header
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						seenBody, _ = io.ReadAll(r.Body)
						seenHeaders = r.Header.Clone()
						if ojson.GetBytes(seenBody, "stream").Bool() {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w,
								"event: message_start\n"+
									`data: {"type":"message_start","message":{"id":"msg_7440","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}`+"\n\n"+
									"event: content_block_start\n"+
									`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
									"event: content_block_delta\n"+
									`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`+"\n\n"+
									"event: content_block_stop\n"+
									`data: {"type":"content_block_stop","index":0}`+"\n\n"+
									"event: message_delta\n"+
									`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\n"+
									"event: message_stop\n"+
									`data: {"type":"message_stop"}`+"\n\n",
							)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"msg_7440","type":"message","model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`))
					}))
					defer server.Close()

					auth := &cliproxyauth.Auth{
						ID:       "oauth-issue-7440-" + model,
						Metadata: claudeOAuthTestMetadata(),
						Attributes: map[string]string{
							"api_key":    "sk-ant-oat-issue-7440",
							"base_url":   server.URL,
							"cloak_mode": "always",
						},
					}
					executor := NewClaudeExecutor(&config.Config{})
					req := cliproxyexecutor.Request{
						Model:   model,
						Payload: source.payload(model),
					}
					opts := cliproxyexecutor.Options{
						SourceFormat: source.format,
						Stream:       stream,
					}

					if stream {
						res, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						var chunks [][]byte
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
							if len(chunk.Payload) > 0 {
								chunks = append(chunks, chunk.Payload)
							}
						}
						if source.format == sdktranslator.FormatOpenAI {
							if len(chunks) == 0 {
								t.Fatal("expected non-empty translated OpenAI stream chunks")
							}
							for i, rawChunk := range chunks {
								gotModel := ojson.GetBytesNoCopy(rawChunk, "model").String()
								if gotModel != "claude-opus-4-8" {
									t.Fatalf("chunk[%d] model = %q, want claude-opus-4-8; payload=%s", i, gotModel, rawChunk)
								}
							}
						}
					} else {
						resp, errExec := executor.Execute(context.Background(), auth, req, opts)
						if errExec != nil {
							t.Fatalf("Execute() error = %v", errExec)
						}
						if source.format == sdktranslator.FormatOpenAI {
							if gotModel := ojson.GetBytesNoCopy(resp.Payload, "model").String(); gotModel != "claude-opus-4-8" {
								t.Fatalf("response model = %q, want claude-opus-4-8; payload=%s", gotModel, resp.Payload)
							}
						}
					}

					if ojson.GetBytesNoCopy(seenBody, "fallbacks").Exists() {
						t.Fatalf("default cloaking injected unsolicited fallbacks: %s", ojson.GetBytesNoCopy(seenBody, "fallbacks").Raw)
					}
					betas := seenHeaders.Get("Anthropic-Beta")
					if strings.Contains(betas, claudeServerSideFallbackBeta) {
						t.Fatalf("Anthropic-Beta = %q, must not include %s by default", betas, claudeServerSideFallbackBeta)
					}
					if strings.Contains(betas, claudeFallbackCreditBeta) {
						t.Fatalf("Anthropic-Beta = %q, must not include %s by default", betas, claudeFallbackCreditBeta)
					}
				})
			}
		}
	}
}

func TestClaudeExecutor_ServerSideFallbackOptInAndPayloadFilter_Issue7440(t *testing.T) {
	optInCfg, errParse := config.ParseConfigBytes([]byte("config-version: 8\nupstream:\n  claude:\n    server-side-fallback: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		if ojson.GetBytes(seenBody, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w,
				"event: message_start\n"+
					`data: {"type":"message_start","message":{"id":"msg_optin","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":4,"output_tokens":0}}}`+"\n\n"+
					"event: content_block_start\n"+
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
					"event: content_block_delta\n"+
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+"\n\n"+
					"event: content_block_stop\n"+
					`data: {"type":"content_block_stop","index":0}`+"\n\n"+
					"event: message_delta\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
					"event: message_stop\n"+
					`data: {"type":"message_stop"}`+"\n\n",
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_optin","type":"message","model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       "oauth-optin-7440",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":    "sk-ant-oat-optin-7440",
			"base_url":   server.URL,
			"cloak_mode": "always",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`),
	}

	if _, errExec := NewClaudeExecutor(optInCfg).Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); errExec != nil {
		t.Fatalf("Execute(optIn) error = %v", errExec)
	}
	if got := ojson.GetBytesNoCopy(seenBody, "fallbacks.0.model").String(); got != "claude-opus-4-8" {
		t.Fatalf("opt-in fallbacks.0.model = %q, want claude-opus-4-8; body=%s", got, seenBody)
	}
	for _, beta := range []string{claudeServerSideFallbackBeta, claudeFallbackCreditBeta} {
		if !strings.Contains(seenHeaders.Get("Anthropic-Beta"), beta) {
			t.Fatalf("Anthropic-Beta = %q, missing %s when server-side-fallback is enabled", seenHeaders.Get("Anthropic-Beta"), beta)
		}
	}

	// Payload filter remains the final barrier even when server-side-fallback is enabled.
	filteredCfg, errParseFilter := config.ParseConfigBytes([]byte("config-version: 8\nupstream:\n  claude:\n    server-side-fallback: true\nrequests:\n  payload:\n    filter:\n      - models:\n          - name: \"claude-opus-5-5\"\n            protocol: \"claude\"\n        params:\n          - \"fallbacks\"\n"))
	if errParseFilter != nil {
		t.Fatalf("ParseConfigBytes(filtered) error = %v", errParseFilter)
	}
	if _, errExec := NewClaudeExecutor(filteredCfg).Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); errExec != nil {
		t.Fatalf("Execute(filtered) error = %v", errExec)
	}
	if ojson.GetBytesNoCopy(seenBody, "fallbacks").Exists() {
		t.Fatalf("payload.filter failed to remove fallbacks: %s", ojson.GetBytesNoCopy(seenBody, "fallbacks").Raw)
	}
	for _, beta := range []string{claudeServerSideFallbackBeta, claudeFallbackCreditBeta} {
		if strings.Contains(seenHeaders.Get("Anthropic-Beta"), beta) {
			t.Fatalf("Anthropic-Beta = %q, must omit %s after payload.filter removes fallbacks", seenHeaders.Get("Anthropic-Beta"), beta)
		}
	}
}

func TestClaudeExecutor_DefaultNoFallbackPreservesCallerFallbackAndRefusalTransparency_Issue7440(t *testing.T) {
	// 1. Explicit caller fallbacks are still honored when server-side-fallback defaults to false.
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_caller_fb","type":"message","model":"claude-sonnet-5","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       "oauth-caller-fb-7440",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":    "sk-ant-oat-caller-fb-7440",
			"base_url":   server.URL,
			"cloak_mode": "always",
		},
	}
	callerFallbackReq := cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","fallbacks":[{"model":"claude-sonnet-5"}],"messages":[{"role":"user","content":"hello"}]}`),
	}
	if _, errExec := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, callerFallbackReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); errExec != nil {
		t.Fatalf("Execute(caller fallback) error = %v", errExec)
	}
	if got := ojson.GetBytesNoCopy(seenBody, "fallbacks.0.model").String(); got != "claude-sonnet-5" {
		t.Fatalf("caller fallback = %q, want claude-sonnet-5", got)
	}
	if !strings.Contains(seenHeaders.Get("Anthropic-Beta"), claudeServerSideFallbackBeta) {
		t.Fatalf("Anthropic-Beta = %q, want %s for explicit caller fallback", seenHeaders.Get("Anthropic-Beta"), claudeServerSideFallbackBeta)
	}

	// 2. When server-side-fallback is off and Opus 5.5 declines with reasoning_extraction refusal,
	// both Execute and ExecuteStream surface the refusal transparently on OpenAI Chat Completions.
	refusalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if ojson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w,
				"event: message_start\n"+
					`data: {"type":"message_start","message":{"id":"msg_refusal_7440","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":6,"output_tokens":0}}}`+"\n\n"+
					"event: message_delta\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"reasoning_extraction","explanation":"Request declined under reasoning extraction safeguards."}},"usage":{"output_tokens":0}}`+"\n\n"+
					"event: message_stop\n"+
					`data: {"type":"message_stop"}`+"\n\n",
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_refusal_7440","type":"message","model":"claude-opus-5-5","role":"assistant","content":[],"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"reasoning_extraction","explanation":"Request declined under reasoning extraction safeguards."},"usage":{"input_tokens":6,"output_tokens":0}}`))
	}))
	defer refusalServer.Close()

	refusalAuth := &cliproxyauth.Auth{
		ID:       "oauth-refusal-7440",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":    "sk-ant-oat-refusal-7440",
			"base_url":   refusalServer.URL,
			"cloak_mode": "always",
		},
	}
	chatReq := cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`),
	}

	_, errRefusalExec := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), refusalAuth, chatReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errRefusalExec == nil || !strings.Contains(errRefusalExec.Error(), "reasoning_extraction") {
		t.Fatalf("Execute(refusal) err = %v, want reasoning_extraction refusal error", errRefusalExec)
	}

	streamRes, errRefusalStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(context.Background(), refusalAuth, chatReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if errRefusalStream != nil {
		t.Fatalf("ExecuteStream(refusal) initial err = %v", errRefusalStream)
	}
	sawContentFilter := false
	var terminalStreamErr error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			terminalStreamErr = chunk.Err
			continue
		}
		if ojson.GetBytesNoCopy(chunk.Payload, "choices.0.finish_reason").String() == "content_filter" {
			sawContentFilter = true
		}
		if gotModel := ojson.GetBytesNoCopy(chunk.Payload, "model").String(); gotModel != "claude-opus-5-5" {
			t.Fatalf("refusal stream chunk model = %q, want claude-opus-5-5", gotModel)
		}
	}
	if !sawContentFilter {
		t.Fatal("expected finish_reason=content_filter chunk before refusal termination")
	}
	if terminalStreamErr == nil || !strings.Contains(terminalStreamErr.Error(), "reasoning_extraction") {
		t.Fatalf("refusal stream terminal err = %v, want reasoning_extraction refusal error", terminalStreamErr)
	}
}

func TestClaudeExecutor_OpenAIResponsesSurfacesServingModelAndFallbackWhenOptedIn_Issue7440(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"event: message_start\n"+
				`data: {"type":"message_start","message":{"id":"msg_resp_e2e_7440","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":6,"output_tokens":0}}}`+"\n\n"+
				"event: content_block_start\n"+
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
				"event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fallback via responses"}}`+"\n\n"+
				"event: content_block_stop\n"+
				`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				"event: message_delta\n"+
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4,"iterations":[{"type":"message","model":"claude-opus-5-5","output_tokens":0},{"type":"fallback_message","model":"claude-opus-4-8","output_tokens":4}]}}`+"\n\n"+
				"event: message_stop\n"+
				`data: {"type":"message_stop"}`+"\n\n",
		)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       "oauth-responses-fb-7440",
		Metadata: claudeOAuthTestMetadata(),
		Attributes: map[string]string{
			"api_key":    "sk-ant-oat-responses-fb-7440",
			"base_url":   server.URL,
			"cloak_mode": "always",
		},
	}
	cfg := &config.Config{Claude: config.ClaudeConfig{ServerSideFallback: true}}
	exec := NewClaudeExecutor(cfg)
	req := cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","input":[{"role":"user","content":"hello"}]}`),
	}

	nonStreamResp, errExec := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExec != nil {
		t.Fatalf("Execute(OpenAIResponses) error = %v", errExec)
	}
	if got := ojson.GetBytesNoCopy(nonStreamResp.Payload, "model").String(); got != "claude-opus-4-8" {
		t.Fatalf("Execute(OpenAIResponses) model = %q, want claude-opus-4-8; payload=%s", got, string(nonStreamResp.Payload))
	}
	if fb := ojson.GetBytesNoCopy(nonStreamResp.Payload, "fallback"); !fb.Exists() || fb.Get("from.model").String() != "claude-opus-5-5" || fb.Get("to.model").String() != "claude-opus-4-8" {
		t.Fatalf("Execute(OpenAIResponses) fallback = %s, want from=5-5 to=4-8", fb.Raw)
	}

	streamResp, errStream := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream(OpenAIResponses) error = %v", errStream)
	}
	var completedModel string
	var sawCompletedFallback bool
	for chunk := range streamResp.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			ev := ojson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
			if ev.Get("type").String() == "response.completed" {
				completedModel = ev.Get("response.model").String()
				sawCompletedFallback = ev.Get("response.fallback.to.model").String() == "claude-opus-4-8"
			}
		}
	}
	if completedModel != "claude-opus-4-8" || !sawCompletedFallback {
		t.Fatalf("ExecuteStream(OpenAIResponses) completedModel=%q sawCompletedFallback=%v, want claude-opus-4-8 and true", completedModel, sawCompletedFallback)
	}
}
