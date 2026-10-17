package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeCleanupBilling = "x-anthropic-billing-header: cc_version=2.1.302.db3; cc_entrypoint=cli; cc_prompt_id=keep;"
const claudeCleanupPayload = `{"model":"claude-haiku-5-5","max_tokens":64,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.302.db3; cc_entrypoint=cli; cch=abcde; cc_prompt_id=keep;"},{"type":"text","text":"Keep this instruction."}],"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[{"role":"user","content":"Keep literal cch=abcde; block_binding in this message."}]}`

func TestClaudeExecutor_RequestCleanup(t *testing.T) {
	for _, credential := range []string{"api-key", "oauth"} {
		for _, path := range []string{"execute", "stream", "count_tokens"} {
			for _, override := range []bool{false, true} {
				name := credential + "/" + path
				if override {
					name += "/payload-override"
				}
				t.Run(name, func(t *testing.T) {
					cfg := &config.Config{}
					const configuredBilling = "x-anthropic-billing-header: cc_version=configured; cc_entrypoint=cli; cch=12345;"
					if override {
						cfg.Payload.Override = []config.PayloadRule{{
							Models: []config.PayloadModelRule{{Name: "*", NotExist: []string{"thinking.block_binding"}}},
							Params: map[string]any{
								"system.0.text": configuredBilling,
								"thinking.block_binding.prefix_mismatch_behavior": "error",
							},
						}}
					}
					auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-cleanup", "cloak_mode": "never"}}
					if credential == "oauth" {
						auth.Attributes["api_key"] = "sk-ant-oat-cleanup"
						auth.Metadata = claudeOAuthTestMetadata()
					}
					var seenBody []byte
					transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
						seenBody, _ = io.ReadAll(req.Body)
						contentType := "application/json"
						response := `{"id":"msg_cleanup","type":"message","model":"claude-haiku-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
						if path == "stream" {
							contentType = "text/event-stream"
							response = "data: {\"type\":\"message_stop\"}\n\n"
						} else if path == "count_tokens" {
							response = `{"input_tokens":7}`
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					executor := NewClaudeExecutor(cfg)
					req := cliproxyexecutor.Request{Model: "claude-haiku-5-5", Payload: []byte(claudeCleanupPayload)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
					switch path {
					case "execute":
						if _, errExecute := executor.Execute(ctx, auth, req, opts); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count_tokens":
						if _, errCount := executor.CountTokens(ctx, auth, req, opts); errCount != nil {
							t.Fatal(errCount)
						}
					}
					if len(seenBody) == 0 {
						t.Fatal("expected upstream request")
					}
					billing := gjson.GetBytes(seenBody, "system.0.text").String()
					if override {
						if billing != configuredBilling || gjson.GetBytes(seenBody, "thinking.block_binding.prefix_mismatch_behavior").String() != "error" {
							t.Fatalf("payload rules must see cleaned body and retain final values: %s", seenBody)
						}
					} else {
						if gjson.GetBytes(seenBody, "thinking.block_binding").Exists() || strings.Contains(gjson.GetBytes(seenBody, "system").Raw, "cch=") {
							t.Fatalf("request still contains removed markers: %s", seenBody)
						}
						if path != "count_tokens" || credential != "oauth" {
							if billing != claudeCleanupBilling {
								t.Fatalf("billing = %q, want %q", billing, claudeCleanupBilling)
							}
						}
					}
					if gjson.GetBytes(seenBody, "thinking.type").String() != "adaptive" || !strings.Contains(string(seenBody), "Keep literal cch=abcde; block_binding in this message.") {
						t.Fatalf("cleanup changed unrelated fields: %s", seenBody)
					}
					if !(override && path == "count_tokens" && credential == "oauth") && !strings.Contains(string(seenBody), "Keep this instruction.") {
						t.Fatalf("cleanup lost the caller system instruction: %s", seenBody)
					}
				})
			}
		}
	}
}

func TestClaudeExecutor_LocalCountTokensUsesCleanedBody(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*", NotExist: []string{"thinking.block_binding"}, Match: []map[string]any{{"system.0.text": claudeCleanupBilling}}}},
		Params: map[string]any{"messages": []any{}},
	}}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-cleanup", "base_url": "https://gateway.example"}}
	_, errCount := NewClaudeExecutor(cfg).CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-haiku-5-5", Payload: []byte(claudeCleanupPayload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if errCount == nil || !strings.Contains(errCount.Error(), "messages must be a non-empty array") {
		t.Fatalf("payload rule must match cleaned body before local validation, got %v", errCount)
	}
}
