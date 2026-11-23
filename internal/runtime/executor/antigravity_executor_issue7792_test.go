package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

func TestAntigravityBuildRequest_ClaudePreservesToolChoiceNoneAcrossFormats(t *testing.T) {
	t.Parallel()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-issue-7792",
		Provider: "antigravity",
		Metadata: map[string]any{"project_id": "project-7792"},
	}
	to := sdktranslator.FromString("antigravity")

	models := []string{
		"claude-sonnet-4-6",
		"claude-opus-4-6-thinking",
		"claude-opus-5-5-high",
		"claude-sonnet-5-5-high",
		"Claude-Sonnet-4-6",
	}

	cases := []struct {
		name           string
		format         sdktranslator.Format
		payload        string
		expectTools    bool
		expectedToolFn string
	}{
		{
			name:   "openai_chat_string_none",
			format: sdktranslator.FormatOpenAI,
			payload: `{
				"messages":[{"role":"user","content":"Answer directly without tools."}],
				"tools":[{"type":"function","function":{"name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}],
				"tool_choice":"none"
			}`,
			expectTools:    true,
			expectedToolFn: "bash",
		},
		{
			name:   "openai_chat_object_none",
			format: sdktranslator.FormatOpenAI,
			payload: `{
				"messages":[{"role":"user","content":"Answer directly without tools."}],
				"tools":[{"type":"function","function":{"name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}],
				"tool_choice":{"type":"none"}
			}`,
			expectTools:    true,
			expectedToolFn: "bash",
		},
		{
			name:   "openai_responses_string_none",
			format: sdktranslator.FormatOpenAIResponse,
			payload: `{
				"input":[{"role":"user","content":[{"type":"input_text","text":"Answer directly without tools."}]}],
				"tools":[{"type":"function","name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],
				"tool_choice":"none"
			}`,
			expectTools:    true,
			expectedToolFn: "bash",
		},
		{
			name:   "openai_responses_object_none",
			format: sdktranslator.FormatOpenAIResponse,
			payload: `{
				"input":[{"role":"user","content":[{"type":"input_text","text":"Answer directly without tools."}]}],
				"tools":[{"type":"function","name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],
				"tool_choice":{"type":"none"}
			}`,
			expectTools:    true,
			expectedToolFn: "bash",
		},
		{
			name:   "claude_object_none",
			format: sdktranslator.FormatClaude,
			payload: `{
				"messages":[{"role":"user","content":"Answer directly without tools."}],
				"tools":[{"name":"bash","description":"Run shell","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],
				"tool_choice":{"type":"none"}
			}`,
			expectTools: false,
		},
		{
			name:   "gemini_mode_none",
			format: sdktranslator.FormatGemini,
			payload: `{
				"contents":[{"role":"user","parts":[{"text":"Answer directly without tools."}]}],
				"tools":[{"functionDeclarations":[{"name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}],
				"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}
			}`,
			expectTools:    true,
			expectedToolFn: "bash",
		},
	}

	for _, modelName := range models {
		for _, tc := range cases {
			t.Run(modelName+"/"+tc.name, func(t *testing.T) {
				reqDoc := ojson.NewDoc([]byte(tc.payload))
				if errSet := reqDoc.Set("model", modelName); errSet != nil {
					t.Fatalf("set model: %v", errSet)
				}
				rawPayload := reqDoc.Bytes()

				translationReq := sdktranslator.RequestEnvelope{Format: tc.format, Model: modelName}
				_, translated, errTranslate := helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(
					context.Background(), nil, exec.cfg, tc.format, to, translationReq, rawPayload, rawPayload,
				)
				if errTranslate != nil {
					t.Fatalf("translate error: %v", errTranslate)
				}

				_, finalPayload, errBuild := exec.buildRequestDoc(
					context.Background(), auth, "token", modelName, ojson.NewDoc(translated), true, "", "https://example.com",
				)
				if errBuild != nil {
					t.Fatalf("buildRequestDoc error: %v", errBuild)
				}

				if got := ojson.GetBytes(finalPayload, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
					t.Fatalf("request.toolConfig.functionCallingConfig.mode = %q, want NONE; payload=%s", got, finalPayload)
				}
				hasTools := ojson.GetBytes(finalPayload, "request.tools").Exists()
				if hasTools != tc.expectTools {
					t.Fatalf("request.tools exists = %t, want %t; payload=%s", hasTools, tc.expectTools, finalPayload)
				}
				if tc.expectTools {
					if got := ojson.GetBytes(finalPayload, "request.tools.0.functionDeclarations.0.name").String(); got != tc.expectedToolFn {
						t.Fatalf("tool name = %q, want %q; payload=%s", got, tc.expectedToolFn, finalPayload)
					}
				}
			})
		}
	}
}

func TestAntigravityBuildRequest_ClaudePreservesToolChoiceNoneProtoAliases(t *testing.T) {
	t.Parallel()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "auth-issue-7792-aliases",
		Provider: "antigravity",
		Metadata: map[string]any{"project_id": "project-7792"},
	}

	aliasCases := []struct {
		name          string
		toolContainer string
		callingKey    string
		mode          string
	}{
		{"camel_camel_upper", "toolConfig", "functionCallingConfig", "NONE"},
		{"camel_snake_upper", "toolConfig", "function_calling_config", "NONE"},
		{"snake_camel_upper", "tool_config", "functionCallingConfig", "NONE"},
		{"snake_snake_lower", "tool_config", "function_calling_config", " none "},
	}

	for _, tc := range aliasCases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(`{
				"request":{
					"contents":[{"role":"user","parts":[{"text":"hi"}]}],
					"tools":[{"functionDeclarations":[{"name":"bash","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}]
				}
			}`))
			modePath := "request." + tc.toolContainer + "." + tc.callingKey + ".mode"
			if errSet := doc.Set(modePath, tc.mode); errSet != nil {
				t.Fatalf("set %s: %v", modePath, errSet)
			}

			_, finalPayload, errBuild := exec.buildRequestDoc(
				context.Background(), auth, "token", "claude-sonnet-4-6", doc, true, "", "https://example.com",
			)
			if errBuild != nil {
				t.Fatalf("buildRequestDoc error: %v", errBuild)
			}

			if got := ojson.GetBytes(finalPayload, modePath).String(); got != "NONE" {
				t.Fatalf("%s = %q, want NONE; payload=%s", modePath, got, finalPayload)
			}
			if tc.toolContainer == "tool_config" && ojson.GetBytes(finalPayload, "request.toolConfig").Exists() {
				t.Fatalf("unexpected conflicting camelCase request.toolConfig added alongside tool_config: %s", finalPayload)
			}
			if tc.callingKey == "function_calling_config" && ojson.GetBytes(finalPayload, "request."+tc.toolContainer+".functionCallingConfig").Exists() {
				t.Fatalf("unexpected conflicting camelCase functionCallingConfig added alongside function_calling_config: %s", finalPayload)
			}
			if got := ojson.GetBytes(finalPayload, "request.tools.0.functionDeclarations.0.name").String(); got != "bash" {
				t.Fatalf("tool name = %q, want bash; payload=%s", got, finalPayload)
			}
		})
	}

	for _, mode := range []string{"", "AUTO", "ANY"} {
		name := mode
		if name == "" {
			name = "omitted"
		}
		t.Run("non_none_still_validated/"+name, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(`{
				"request":{
					"contents":[{"role":"user","parts":[{"text":"hi"}]}],
					"tools":[{"functionDeclarations":[{"name":"bash","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}]
				}
			}`))
			if mode != "" {
				if errSet := doc.Set("request.toolConfig.functionCallingConfig.mode", mode); errSet != nil {
					t.Fatalf("set mode: %v", errSet)
				}
			}

			_, finalPayload, errBuild := exec.buildRequestDoc(
				context.Background(), auth, "token", "claude-sonnet-4-6", doc, true, "", "https://example.com",
			)
			if errBuild != nil {
				t.Fatalf("buildRequestDoc error: %v", errBuild)
			}
			if got := ojson.GetBytes(finalPayload, "request.toolConfig.functionCallingConfig.mode").String(); got != "VALIDATED" {
				t.Fatalf("mode = %q, want VALIDATED for non-NONE mode %q; payload=%s", got, mode, finalPayload)
			}
		})
	}
}

func TestAntigravityExecute_ClaudeToolChoiceNoneRespectsFinalPayloadRules(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"execute", "stream"} {
		for _, ruleMode := range []string{"none", "override", "filter"} {
			t.Run(operation+"/"+ruleMode, func(t *testing.T) {
				var capturedBody []byte
				rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					capturedBody = body
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2,\"totalTokenCount\":12}}}\n\n")),
					}, nil
				})

				cfg := &config.Config{DisableCooling: true}
				models := []config.PayloadModelRule{{Name: "claude-*", Protocol: "antigravity"}}
				switch ruleMode {
				case "override":
					cfg.Payload.Override = []config.PayloadRule{{
						Models: models,
						Params: map[string]any{"toolConfig.functionCallingConfig.mode": "AUTO"},
					}}
				case "filter":
					cfg.Payload.Filter = []config.PayloadFilterRule{{
						Models: models,
						Params: []string{"toolConfig"},
					}}
				}

				exec := NewAntigravityExecutor(cfg)
				auth := &cliproxyauth.Auth{
					ID:       "auth-issue-7792-rules-" + operation + "-" + ruleMode,
					Provider: "antigravity",
					Metadata: map[string]any{
						"access_token": "token",
						"project_id":   "project-7792",
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
					},
				}
				payload := []byte(`{
					"model":"claude-sonnet-4-6",
					"messages":[{"role":"user","content":"Answer directly without tools."}],
					"tools":[{"type":"function","function":{"name":"bash","description":"Run shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}}],
					"tool_choice":"none"
				}`)
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
				req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}

				switch operation {
				case "execute":
					if _, errExec := exec.Execute(ctx, auth, req, opts); errExec != nil {
						t.Fatalf("Execute error: %v", errExec)
					}
				case "stream":
					opts.Stream = true
					streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream error: %v", errStream)
					}
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error: %v", chunk.Err)
						}
					}
				}

				switch ruleMode {
				case "none":
					if got := ojson.GetBytes(capturedBody, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
						t.Fatalf("mode = %q, want NONE; body=%s", got, capturedBody)
					}
					if got := ojson.GetBytes(capturedBody, "request.tools.0.functionDeclarations.0.name").String(); got != "bash" {
						t.Fatalf("tool name = %q, want bash; body=%s", got, capturedBody)
					}
				case "override":
					if got := ojson.GetBytes(capturedBody, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
						t.Fatalf("payload.override lost: mode = %q, want AUTO; body=%s", got, capturedBody)
					}
				case "filter":
					if ojson.GetBytes(capturedBody, "request.toolConfig").Exists() {
						t.Fatalf("payload.filter lost: request.toolConfig survived; body=%s", capturedBody)
					}
				}
			})
		}
	}
}
