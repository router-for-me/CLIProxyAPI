package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func applyClaudeSystemInstructionPolicy(
	payload []byte,
	settings claudeCloakSettings,
	cchSigning bool,
	version, entrypoint, workload string,
	currentDate string,
	isSubagent bool,
	prevReq, promptID string,
	keepCallerSystemTopLevel bool,
	turnOrigin ...string,
) []byte {
	doc := ojson.NewDoc(payload)
	if !applyClaudeSystemInstructionPolicyDoc(doc, settings, cchSigning, version, entrypoint, workload, currentDate, isSubagent, prevReq, promptID, keepCallerSystemTopLevel, turnOrigin...) {
		return payload
	}
	return doc.Bytes()
}

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
				if got, want := ojson.GetBytes(result, "messages").Raw, ojson.GetBytes(payload, "messages").Raw; got != want {
					t.Fatalf("system policy changed messages: %s", got)
				}
				blocks := ojson.GetBytes(result, "system").Array()
				wanted := collectRelaxedClaudeSystemPromptBlocks(ojson.GetBytes(payload, "system"))
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
					if ojson.GetBytes(result, "system.#").Int() != 3 || ojson.GetBytes(result, "system.2.text").String() != "caller guidance" || ojson.GetBytes(result, "messages.#").Int() != 1 {
						t.Fatalf("caller system was not kept at top level: %s", result)
					}
				} else {
					if ojson.GetBytes(result, "system.#").Int() != 2 || ojson.GetBytes(result, "messages.#").Int() != 2 || ojson.GetBytes(result, "messages.1.role").String() != "system" || ojson.GetBytes(result, "messages.1.content.0.text").String() != "caller guidance" {
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

func TestClaudeRelaxedSystemPolicyPreservesCallerDateWithRetention(t *testing.T) {
	for _, blockContent := range []bool{false, true} {
		for _, strict := range []bool{false, true} {
			t.Run(fmt.Sprintf("blocks=%t/strict=%t", blockContent, strict), func(t *testing.T) {
				days := 7
				cfg := &config.Config{Claude: config.ClaudeConfig{SessionDatePinDays: &days}}
				auth := directClaudeOAuthAuth()
				auth.ID = t.Name()
				auth.Metadata["cloak_relaxed_system_prompt"] = true
				auth.Metadata["cloak_strict_mode"] = fmt.Sprint(strict)
				ctx := helps.WithClaudeSessionID(context.Background(), t.Name())
				reminder := claudeCodeCurrentDateReminder("2026-08-01")
				var content any = reminder + "hello"
				if blockContent {
					content = []map[string]string{{"type": "text", "text": reminder}, {"type": "text", "text": "hello"}}
				}
				payload := []byte(`{"model":"claude-opus-5","system":"caller guidance","messages":[{"role":"user","content":""}]}`)
				doc := ojson.NewDoc(payload)
				if err := doc.Set("messages.0.content", content); err != nil {
					t.Fatal(err)
				}
				payload = doc.Bytes()
				result, cloaked, err := applyCloaking(ctx, cfg, auth, payload, "sk-ant-oat-test", false, false)
				if err != nil || !cloaked {
					t.Fatalf("applyCloaking() cloaked=%t err=%v", cloaked, err)
				}
				if !strict {
					if got, want := ojson.GetBytes(result, "messages").Raw, ojson.GetBytes(payload, "messages").Raw; got != want {
						t.Fatalf("retention changed caller messages in relaxed mode: %s", result)
					}
					return
				}
				blocks := ojson.GetBytes(result, "messages.0.content").Array()
				if len(blocks) != 2 || blocks[0].Get("text").String() != reminder || blocks[1].Get("text").String() != "hello" {
					t.Fatalf("strict mode did not retain the upstream canonical date layout: %s", result)
				}
				if ojson.GetBytes(result, "system.#").Int() != 2 {
					t.Fatalf("strict mode retained caller system guidance: %s", result)
				}
			})
		}
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
						if ojson.GetBytes(captured, "system").String() != "operator guidance" || ojson.GetBytes(captured, "messages.0.content").String() != "operator question" {
							t.Fatalf("final payload rules lost authority: %s", captured)
						}
						return
					}
					blocks := ojson.GetBytes(captured, "system").Array()
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
					messages := ojson.GetBytes(captured, "messages").Array()
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
	blocks := collectRelaxedClaudeSystemPromptBlocks(ojson.Parse(system))
	if len(blocks) != 2 || ojson.Get(blocks[0], "text").String() != " caller " || ojson.Get(blocks[1], "text").String() != "# Reporting outcomes supplied by caller" {
		t.Fatalf("wrong caller blocks: %v", blocks)
	}
}

func TestClaudeRelaxedSystemPolicyOmitsAgentSDKIdentity(t *testing.T) {
	for _, identity := range []string{
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	} {
		t.Run(identity, func(t *testing.T) {
			caller := `{"type":"text","text":"caller guidance","citations":[{"type":"custom","value":"kept"}]}`
			payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"` + identity + `"},` + caller + `],"messages":[{"role":"user","content":"hello"}]}`)
			result := applyClaudeSystemInstructionPolicy(payload, claudeCloakSettings{relaxedSystemPrompt: true}, false, "2.1.280", "cli", "", "2026-10-05", false, "", "", false)
			blocks := ojson.GetBytes(result, "system").Array()
			if len(blocks) != 3 || !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") || blocks[1].Get("text").String() != claudeCodeCLIIdentity || blocks[2].Raw != caller {
				t.Fatalf("unexpected relaxed identity or caller layout: %s", result)
			}
			if got, want := ojson.GetBytes(result, "messages").Raw, ojson.GetBytes(payload, "messages").Raw; got != want {
				t.Fatalf("system policy changed messages: %s", got)
			}
		})
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
	doc := ojson.NewDoc(payload)
	if err := doc.Set("system", []map[string]any{{"type": "text", "text": "valid"}}); err != nil {
		t.Fatal(err)
	}
	payload = doc.Bytes()
	if _, _, err = applyCloaking(context.Background(), &config.Config{}, auth, payload, "sk-ant-oat-test", false, true); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRelaxedSystemPolicyHonorsExplicitUncloak(t *testing.T) {
	for _, mode := range []string{"never", "uncloak"} {
		for _, confirmed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/native=%t", mode, confirmed), func(t *testing.T) {
				enabled := true
				cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "plain-key", Cloak: &config.CloakConfig{Mode: mode, RelaxedSystemPrompt: &enabled}}}}
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "plain-key"}}
				payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280;"},{"type":"text","text":"` + claudeCodeCLIIdentity + `"},{"type":"text","text":"caller guidance"}],"messages":[{"role":"user","content":"hello"}]}`)
				result, cloaked, err := applyCloaking(context.Background(), cfg, auth, payload, "plain-key", confirmed, true)
				if err != nil || cloaked {
					t.Fatalf("explicit %s cloaked=%t err=%v", mode, cloaked, err)
				}
				if ojson.GetBytes(result, "system.#").Int() != 1 || ojson.GetBytes(result, "system.0.text").String() != "caller guidance" || ojson.GetBytes(result, "messages").Raw != ojson.GetBytes(payload, "messages").Raw {
					t.Fatalf("relaxed option overrode upstream uncloak policy: %s", result)
				}
			})
		}
	}
}

func TestClaudeRelaxedSystemPolicyLeavesCountTokensUnchanged(t *testing.T) {
	const responseBody = `{"input_tokens":34,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}`
	var baseline []byte
	for _, setting := range []struct {
		name  string
		value *bool
	}{
		{name: "omitted"},
		{name: "disabled", value: new(false)},
		{name: "enabled", value: new(true)},
	} {
		t.Run(setting.name, func(t *testing.T) {
			var captured []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var errRead error
				captured, errRead = io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "count-key"}}
			cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "count-key", Cloak: &config.CloakConfig{Mode: "always", RelaxedSystemPrompt: setting.value}}}}
			payload := []byte(`{"model":"claude-opus-5","system":"caller guidance","messages":[{"role":"user","content":"hello"}]}`)
			response, errCount := NewClaudeExecutor(cfg).CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if errCount != nil {
				t.Fatal(errCount)
			}
			if baseline == nil {
				baseline = bytes.Clone(captured)
			} else if !bytes.Equal(captured, baseline) {
				t.Fatalf("relaxed setting changed count_tokens request: %s", captured)
			}
			if string(response.Payload) != responseBody {
				t.Fatalf("count_tokens response fields changed: %s", response.Payload)
			}
		})
	}
}

func TestClaudeRelaxedHomePolicyRetainsSensitiveWordPipeline(t *testing.T) {
	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "home-key", "auth_kind": cliproxyauth.AuthKindAPIKey},
		Metadata: map[string]any{
			"cloak_sensitive_words_replace": map[string]any{"Codex": "wrong replacement"},
			"credential_options": map[string]any{"cloak": map[string]any{
				"mode":                    "always",
				"relaxed-system-prompt":   true,
				"sensitive-words":         []any{"ACME, Inc."},
				"sensitive-words-replace": map[string]any{"Codex": "Assistant"},
			}},
		},
	}
	payload := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"ACME, Inc. Codex","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hello"}]}`)
	result, cloaked, err := applyCloaking(context.Background(), cfg, auth, payload, "home-key", false, false)
	if err != nil || !cloaked {
		t.Fatalf("applyCloaking() cloaked=%t err=%v", cloaked, err)
	}
	blocks := ojson.GetBytes(result, "system").Array()
	if len(blocks) != 3 || blocks[2].Get("text").String() != "A\u200BCME, Inc. Assistant" || blocks[2].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("Home relaxed layout lost sensitive-word settings or caller fields: %s", result)
	}
	if ojson.GetBytes(result, "messages.0.content").String() != "hello" || ojson.GetBytes(result, "messages.#").Int() != 1 {
		t.Fatalf("relaxed system policy moved caller messages: %s", result)
	}
}
