package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeRelaxedSystemPolicyPreservesCallerLayout(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-5", "claude-opus-5", "claude-fable-5-1", "claude-future"} {
		for _, system := range []string{
			`" caller <guidance> "`,
			`[{"type":"text","text":"first","citations":[{"type":"custom","value":"kept"}]},{"type":"text","text":"second"}]`,
			`[{"type":"text","text":" "},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"x-anthropic-billing-header: old;"},{"type":"text","text":"last"}]`,
			`[]`,
		} {
			t.Run(model+"/"+system, func(t *testing.T) {
				payload := []byte(`{"model":"` + model + `","system":` + system + `,"messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":[{"type":"text","text":"follow-up"}]}]}`)
				result := applyClaudeSystemInstructionPolicy(payload, claudeCloakSettings{relaxedSystemPrompt: true}, true, "2.1.280", "cli", "", "2026-10-05", false, "", "", false)
				if got, want := gjson.GetBytes(result, "messages").Raw, gjson.GetBytes(payload, "messages").Raw; got != want {
					t.Fatalf("system policy changed messages: %s", got)
				}
				blocks := gjson.GetBytes(result, "system").Array()
				wanted := collectRelaxedClaudeSystemPromptBlocks(gjson.GetBytes(payload, "system"))
				if len(blocks) != len(wanted)+2 {
					t.Fatalf("unexpected system layout: %s", result)
				}
				if !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") || blocks[1].Get("text").String() != claudeCodeCLIIdentity {
					t.Fatalf("required identity blocks missing: %s", result)
				}
				for i, raw := range wanted {
					if blocks[i+2].Raw != raw {
						t.Fatalf("caller block %d changed: %s", i, blocks[i+2].Raw)
					}
				}
				if bytes.Contains(result, []byte("# currentDate")) || bytes.Contains(result, []byte(claudeCodeFableReportingOutcomes)) {
					t.Fatalf("generated guidance in relaxed system: %s", result)
				}
				repeated := applyClaudeSystemInstructionPolicy(result, claudeCloakSettings{relaxedSystemPrompt: true}, true, "2.1.280", "cli", "", "2026-10-06", false, "", "", false)
				if !bytes.Equal(result, repeated) {
					t.Fatalf("repeated system policy changed layout: %s", repeated)
				}
			})
		}
	}
}

func TestClaudeRelaxedSystemPolicyPreservesUpstreamPlacement(t *testing.T) {
	for _, keepCallerSystemTopLevel := range []bool{false, true} {
		for _, relaxed := range []bool{false, true} {
			t.Run(fmt.Sprintf("keep-top-level=%t/relaxed=%t", keepCallerSystemTopLevel, relaxed), func(t *testing.T) {
				payload := []byte(`{"model":"claude-opus-5","system":"caller guidance","messages":[{"role":"user","content":"hello"}]}`)
				result := applyClaudeSystemInstructionPolicy(payload, claudeCloakSettings{relaxedSystemPrompt: relaxed}, false, "2.1.280", "cli", "", "2026-10-05", false, "", "", keepCallerSystemTopLevel)
				if relaxed || keepCallerSystemTopLevel {
					if gjson.GetBytes(result, "system.#").Int() != 3 || gjson.GetBytes(result, "system.2.text").String() != "caller guidance" || gjson.GetBytes(result, "messages.#").Int() != 1 {
						t.Fatalf("caller system was not kept at top level: %s", result)
					}
				} else {
					if gjson.GetBytes(result, "system.#").Int() != 2 || gjson.GetBytes(result, "messages.#").Int() != 2 || gjson.GetBytes(result, "messages.1.role").String() != "system" || gjson.GetBytes(result, "messages.1.content.0.text").String() != "caller guidance" {
						t.Fatalf("upstream caller system relocation changed: %s", result)
					}
				}
				if hasCurrentDate := bytes.Contains(result, []byte("# currentDate")); hasCurrentDate != !relaxed {
					t.Fatalf("current-date policy changed: %s", result)
				}
			})
		}
	}
}

func TestClaudeRelaxedSystemPolicyStrictPrecedence(t *testing.T) {
	payload := []byte(`{"model":"claude-fable-5-1","system":"caller guidance","messages":[{"role":"user","content":"hello"}]}`)
	strict := checkSystemInstructionsWithSigningModeAt(payload, true, false, "2.1.280", "cli", "", "2026-10-05", false, "", "", false)
	both := applyClaudeSystemInstructionPolicy(payload, claudeCloakSettings{strictMode: true, relaxedSystemPrompt: true}, false, "2.1.280", "cli", "", "2026-10-05", false, "", "", false)
	if !bytes.Equal(strict, both) {
		t.Fatalf("strict precedence changed: %s", both)
	}

}

func TestClaudeExecutorRelaxedSystemLayout(t *testing.T) {
	for _, endpoint := range []string{"messages", "stream"} {
		for _, model := range []string{"claude-opus-5", "claude-fable-5-1"} {
			for _, override := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/override=%t", endpoint, model, override), func(t *testing.T) {
					var captured []byte
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						var err error
						captured, err = io.ReadAll(req.Body)
						if err != nil {
							return nil, err
						}
						response := `{"id":"msg_relaxed","type":"message","role":"assistant","model":"` + model + `","content":[]}`
						contentType := "application/json"
						if endpoint == "stream" {
							response = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
							contentType = "text/event-stream"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					auth := directClaudeOAuthAuth()
					auth.ID = t.Name()
					auth.Attributes["base_url"] = "https://api.anthropic.com"
					auth.Metadata["cloak_relaxed_system_prompt"] = true
					cfg := &config.Config{}
					if override {
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}}, Params: map[string]any{"system": "operator guidance", "messages.0.content": "operator question"}}}
					}
					payload := []byte(`{"model":"` + model + `","max_tokens":1024,"system":[{"type":"text","text":"caller guidance"}],"messages":[{"role":"user","content":"hello"}]}`)
					executor := NewClaudeExecutor(cfg)
					request := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"User-Agent": {"pi (linux; x64)"}}}
					switch endpoint {
					case "messages":
						_, err := executor.Execute(ctx, auth, request, opts)
						if err != nil {
							t.Fatal(err)
						}
					case "stream":
						result, err := executor.ExecuteStream(ctx, auth, request, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
					if override {
						if gjson.GetBytes(captured, "system").String() != "operator guidance" || gjson.GetBytes(captured, "messages.0.content").String() != "operator question" {
							t.Fatalf("final payload rules lost authority: %s", captured)
						}
						return
					}
					blocks := gjson.GetBytes(captured, "system").Array()
					if len(blocks) != 3 || blocks[len(blocks)-1].Get("text").String() != "caller guidance" {
						t.Fatalf("caller system layout changed: %s", captured)
					}
					if !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") || blocks[1].Get("text").String() != claudeCodeCLIIdentity {
						t.Fatalf("missing identity: %s", captured)
					}
					signed, err := signAnthropicMessagesBody(captured)
					if err != nil || !bytes.Equal(captured, signed) {
						t.Fatalf("final signature mismatch: %v", err)
					}
					messages := gjson.GetBytes(captured, "messages").Array()
					if len(messages) != 1 || messages[0].Get("role").String() != "user" {
						t.Fatalf("message placement changed: %s", captured)
					}
					content := messages[0].Get("content")
					if content.IsArray() {
						content = content.Get("0.text")
					}
					if content.String() != "hello" || bytes.Contains(captured, []byte("# currentDate")) || bytes.Contains(captured, []byte(claudeCodeFableReportingOutcomes)) {
						t.Fatalf("generated context in relaxed request: %s", captured)
					}
				})
			}
		}
	}
}

func TestClaudeRelaxedSystemBlocksFilterOnlyGeneratedIdentity(t *testing.T) {
	system := `[{"type":"text","text":" "},{"type":"text","text":"` + claudeCodeCLIIdentity + `"},{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280;"},{"type":"text","text":" caller "},{"type":"text","text":"# Reporting outcomes supplied by caller"}]`
	blocks := collectRelaxedClaudeSystemPromptBlocks(gjson.Parse(system))
	if len(blocks) != 2 || gjson.Get(blocks[0], "text").String() != " caller " || gjson.Get(blocks[1], "text").String() != "# Reporting outcomes supplied by caller" {
		t.Fatalf("wrong caller blocks: %v", blocks)
	}
}

func TestClaudeRelaxedSystemPolicyRetainsNativePassthrough(t *testing.T) {
	enabled := true
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "native-key", Cloak: &config.CloakConfig{Mode: "always", RelaxedSystemPrompt: &enabled}}}}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "native-key"}}
	payload := []byte(`{"model":"claude-opus-5","system":"native instructions","messages":[{"role":"user","content":"hello"}]}`)
	ctx := helps.WithClaudeContinuityContext(context.Background(), &helps.ClaudeContinuityContext{})
	result, cloaked, err := applyCloaking(ctx, cfg, auth, payload, "native-key", true, true)
	if err != nil || cloaked || !bytes.Equal(payload, result) {
		t.Fatalf("native passthrough changed: cloaked=%t err=%v body=%s", cloaked, err, result)
	}
}

func TestClaudeRelaxedSystemPolicyRejectsUnsupportedCallerBlocks(t *testing.T) {
	auth := directClaudeOAuthAuth()
	auth.Metadata["cloak_relaxed_system_prompt"] = true
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"image","source":{"type":"base64","data":"invalid"}}],"messages":[{"role":"user","content":"hello"}]}`)
	_, _, err := applyCloaking(context.Background(), &config.Config{}, auth, payload, "sk-ant-oat-test", false, true)
	if err == nil {
		t.Fatal("unsupported caller system block was silently removed")
	}
	payload, _ = sjson.SetBytes(payload, "system", []map[string]any{{"type": "text", "text": "valid"}})
	if _, _, err = applyCloaking(context.Background(), &config.Config{}, auth, payload, "sk-ant-oat-test", false, true); err != nil {
		t.Fatal(err)
	}
}
