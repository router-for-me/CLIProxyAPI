package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestClaudeExecutor_Execute_AppliesRequestReplacementsAfterPayloadConfig(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:  "key-123",
			BaseURL: server.URL,
			Cloak: &config.CloakConfig{
				Mode: "always",
				RequestReplacements: []config.TextReplacement{{
					Find:    "OpenClaw",
					Replace: "OCPlatform",
				}},
			},
		}},
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "claude-3-5-sonnet-20241022", Protocol: "claude"}},
				Params: map[string]any{"messages.0.content.0.text": "OpenClaw"},
			}},
		},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	executor := NewClaudeExecutor(cfg)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	bodyText := string(seenBody)
	if !strings.Contains(bodyText, "OCPlatform") {
		t.Fatalf("expected final outbound body to include replaced payload text, got %s", bodyText)
	}
	if strings.Contains(bodyText, "OpenClaw") {
		t.Fatalf("expected final outbound body to exclude unreplaced payload text, got %s", bodyText)
	}
}

func TestClaudeExecutor_ExecuteStream_AppliesResponseReplacementsAcrossDeltaBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Open"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Claw"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, line+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey:  "key-123",
			BaseURL: server.URL,
			Cloak: &config.CloakConfig{
				Mode: "always",
				ResponseReplacements: []config.TextReplacement{{
					Find:    "OpenClaw",
					Replace: "OCPlatform",
				}},
			},
		}},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	executor := NewClaudeExecutor(cfg)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var out strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		out.Write(chunk.Payload)
	}

	streamText := out.String()
	if !strings.Contains(streamText, `"text":"OCPlatform"`) {
		t.Fatalf("expected rewritten stream delta in output, got %s", streamText)
	}
	if strings.Contains(streamText, `"text":"Open"`) || strings.Contains(streamText, `"text":"Claw"`) {
		t.Fatalf("expected boundary-spanning upstream deltas to be suppressed, got %s", streamText)
	}
}

func TestApplyClaudeCloakResponseReplacements_AppliesReverseMap(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak: &config.CloakConfig{
				Mode: "always",
				ResponseReplacements: []config.TextReplacement{
					{Find: "OCPlatform", Replace: "OpenClaw"},
					{Find: "create_task", Replace: "sessions_spawn"},
				},
			},
		}},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}

	out := applyClaudeCloakResponseReplacements(context.Background(), cfg, auth, []byte(`{"text":"OCPlatform create_task"}`))
	text := string(out)
	if strings.Contains(text, "OCPlatform") || strings.Contains(text, "create_task") {
		t.Fatalf("expected reverse mapping to restore original text, got %s", text)
	}
	if !strings.Contains(text, "OpenClaw") || !strings.Contains(text, "sessions_spawn") {
		t.Fatalf("expected reverse mapping to restore original text, got %s", text)
	}
}
