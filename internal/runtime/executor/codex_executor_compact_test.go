package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCompactAddsDefaultInstructionsWithoutInjectingImageTool(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing instructions",
			payload: `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
		{
			name:    "null instructions",
			payload: `{"model":"gpt-5.4","instructions":null,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}

			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Alt:          "responses/compact",
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotPath != "/responses/compact" {
				t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
			}
			if instructions := gjson.GetBytes(gotBody, "instructions"); instructions.Type != gjson.String || instructions.String() != "" {
				t.Fatalf("instructions = %s, want empty string; body=%s", instructions.Raw, gotBody)
			}
			if gjson.GetBytes(gotBody, "tools").Exists() {
				t.Fatalf("compact request injected image_generation tool: %s", gotBody)
			}
			input := gjson.GetBytes(gotBody, "input").Array()
			if len(input) != 2 || input[1].Get("type").String() != "compaction_trigger" {
				t.Fatalf("compact input order changed: %s", gotBody)
			}
			if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
				t.Fatalf("payload = %s", string(resp.Payload))
			}
		})
	}
}

// TestCodexExecutorCompactStripsUnsupportedFields guards against a regression where
// the Codex /responses/compact upstream rejects ordinary Responses request fields
// (e.g. store, temperature, client_metadata) with 400 "Unknown parameter"/"Unsupported
// parameter" errors. The rejected field list was confirmed against the live endpoint.
func TestCodexExecutorCompactStripsUnsupportedFields(t *testing.T) {
	payload := `{
		"model":"gpt-5.6-sol",
		"instructions":"",
		"reasoning":{"effort":"low"},
		"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}],
		"parallel_tool_calls":true,
		"prompt_cache_key":"keep-me",
		"text":{"format":{"type":"text"},"verbosity":"medium"},
		"store":false,
		"service_tier":"auto",
		"temperature":1.0,
		"top_p":1.0,
		"max_output_tokens":2048,
		"max_completion_tokens":2048,
		"truncation":"disabled",
		"background":false,
		"previous_response_id":null,
		"metadata":{},
		"safety_identifier":"user-1",
		"user":"user-1",
		"tool_choice":"auto",
		"include":["reasoning.encrypted_content"],
		"frequency_penalty":0.0,
		"presence_penalty":0.0,
		"conversation":null,
		"max_tool_calls":10,
		"moderation":null,
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"context_management":{"edits":[{"type":"clear_tool_uses_20250919"}]}
	}`

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(payload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, field := range codexCompactUnsupportedFields {
		if gjson.GetBytes(gotBody, field).Exists() {
			t.Fatalf("compact request still carries unsupported field %q: %s", field, gotBody)
		}
	}
	// service_tier is handled separately (kept only when "priority"); "auto" must
	// still be stripped like the rest of the unsupported fields.
	if gjson.GetBytes(gotBody, "service_tier").Exists() {
		t.Fatalf("compact request still carries unsupported service_tier: %s", gotBody)
	}

	// Responses Lite must still be detected from client_metadata before it is
	// stripped, so parallel_tool_calls is forced to false as usual.
	if got := gjson.GetBytes(gotBody, "parallel_tool_calls"); !got.Exists() || got.Bool() {
		t.Fatalf("parallel_tool_calls = %s, want false (Responses Lite detection must survive stripping): %s", got.Raw, gotBody)
	}

	for field, want := range map[string]string{
		"prompt_cache_key": "keep-me",
		"model":            "gpt-5.6-sol",
	} {
		if got := gjson.GetBytes(gotBody, field).String(); got != want {
			t.Fatalf("%s = %q, want %q: %s", field, got, want, gotBody)
		}
	}
	if got := gjson.GetBytes(gotBody, "text.verbosity").String(); got != "medium" {
		t.Fatalf("text.verbosity = %q, want medium: %s", got, gotBody)
	}
}

// TestCodexExecutorCompactPreservesPriorityServiceTier confirms the compact
// endpoint accepts service_tier="priority" (unlike "auto"/"flex"/other values),
// mirroring the conditional handling already used for ordinary /responses requests.
func TestCodexExecutorCompactPreservesPriorityServiceTier(t *testing.T) {
	payload := `{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}],
		"service_tier":"priority"
	}`

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(payload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority to be preserved: %s", got, gotBody)
	}
}

// TestCodexExecutorCompactIdentityConfuseDoesNotResurrectClientMetadata guards
// against a regression where Codex Identity Confuse rewrites
// client_metadata.x-codex-installation-id from the original user payload after
// client_metadata was already stripped for the compact upstream, resurrecting the
// unsupported field on the bytes actually sent over the wire.
func TestCodexExecutorCompactIdentityConfuseDoesNotResurrectClientMetadata(t *testing.T) {
	payload := `{
		"model":"gpt-5.6-sol",
		"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}],
		"client_metadata":{"x-codex-installation-id":"install-1"}
	}`

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	})
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex", Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(payload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gjson.GetBytes(gotBody, "client_metadata").Exists() {
		t.Fatalf("Identity Confuse resurrected unsupported client_metadata on the wire: %s", gotBody)
	}
}
