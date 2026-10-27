package executor

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeIssue7000Tools = `[{"name":"lookup_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`

func claudeIssue7000Payload(model, choice string) []byte {
	payload := `{"model":"` + model + `","max_tokens":4096,"messages":[{"role":"user","content":"Weather in Paris?"}],"tools":` + claudeIssue7000Tools
	if choice != "" {
		payload += `,"tool_choice":` + choice
	}
	return []byte(payload + `}`)
}

type claudeIssue7000Executor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

// Capture only the network boundary; translation and all request preparation stay real.
func captureClaudeIssue7000(t *testing.T, executor claudeIssue7000Executor, auth *cliproxyauth.Auth, request cliproxyexecutor.Request, options cliproxyexecutor.Options, method string) ([]byte, http.Header) {
	t.Helper()
	var captured []byte
	var headers http.Header
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		if errClose := req.Body.Close(); errClose != nil {
			return nil, errClose
		}
		captured, headers = body, req.Header.Clone()
		contentType := "application/json"
		model := gjson.GetBytes(body, "model").String()
		response := `{"id":"msg_issue7000","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":17,"output_tokens":1}}`
		if strings.HasSuffix(req.URL.Path, "/count_tokens") {
			response = `{"input_tokens":17}`
		} else if gjson.GetBytes(body, "stream").Bool() {
			contentType, response = "text/event-stream", strings.ReplaceAll(claudeIssue6698SSE, "claude-opus-5-5", model)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	switch method {
	case "Execute":
		if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
			t.Fatalf("Execute: %v", errExecute)
		}
	case "ExecuteStream":
		result, errStream := executor.ExecuteStream(ctx, auth, request, options)
		if errStream != nil {
			t.Fatalf("ExecuteStream: %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk: %v", chunk.Err)
			}
		}
	case "CountTokens":
		response, errCount := executor.CountTokens(ctx, auth, request, options)
		if errCount != nil {
			t.Fatalf("CountTokens: %v", errCount)
		}
		if got := gjson.GetBytes(response.Payload, "input_tokens").Int(); got != 17 {
			t.Fatalf("upstream input_tokens = %d, want 17", got)
		}
	default:
		t.Fatalf("unknown method %q", method)
	}
	if !gjson.ValidBytes(captured) {
		t.Fatalf("expected captured upstream JSON, got %q", captured)
	}
	return captured, headers
}

func TestClaudeExecutor_Issue7000ModelAndChoiceBoundaries(t *testing.T) {
	const forced = `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":false}`
	const auto = `{"type":"auto","disable_parallel_tool_use":false}`
	for _, tc := range []struct {
		name, route, bodyModel, choice, normalized, want string
	}{
		{name: "opus date", route: "claude-opus-5-5-20260901", choice: forced, want: auto},
		{name: "sonnet bracket", route: "claude-sonnet-5-5[1m]", choice: forced, want: auto},
		{name: "high date bracket thinking suffix", route: "claude-opus-5-5-high-20260901[1m](high)", choice: forced, want: auto},
		{name: "reasoning variant", route: "claude-sonnet-5-5-reasoning", choice: forced, want: auto},
		{name: "old opus", route: "claude-opus-5", choice: forced, want: forced},
		{name: "old sonnet", route: "claude-sonnet-4-6", choice: forced, want: forced},
		{name: "haiku", route: "claude-haiku-5-5", choice: forced, want: forced},
		{name: "near match opus", route: "claude-opus-5-50", choice: forced, want: forced},
		{name: "near match sonnet", route: "claude-sonnet-5-50", choice: forced, want: forced},
		{name: "invalid family variant", route: "claude-opus-5-5-custom", choice: forced, want: forced},
		{name: "auto", route: "claude-opus-5-5", choice: auto, want: auto},
		{name: "none", route: "claude-sonnet-5-5", choice: `{"type":"none"}`, want: `{"type":"none"}`},
		{name: "omitted", route: "claude-opus-5-5"},
		{name: "body cannot spoof target", route: "claude-opus-5", bodyModel: "claude-opus-5-5", choice: forced, want: forced},
		{name: "body cannot hide target", route: "claude-opus-5-5", bodyModel: "custom-alias", choice: forced, want: auto},
		{name: "alias resolved upstream", route: "custom-alias", normalized: "claude-sonnet-5-5", choice: forced, want: auto},
		{name: "upstream is old despite alias", route: "claude-opus-5-5", normalized: "claude-opus-5", choice: forced, want: forced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := NewClaudeExecutor(&config.Config{})
			if tc.normalized != "" {
				executor.upstreamModelNormalizer = func(string) string { return tc.normalized }
			}
			bodyModel := tc.bodyModel
			if bodyModel == "" {
				bodyModel = tc.route
			}
			body, _ := captureClaudeIssue7000(t, executor, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000"}}, cliproxyexecutor.Request{Model: tc.route, Payload: claudeIssue7000Payload(bodyModel, tc.choice)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, "Execute")
			if got, want := gjson.GetBytes(body, "tool_choice").Value(), gjson.Parse(tc.want).Value(); !reflect.DeepEqual(got, want) {
				t.Fatalf("tool_choice = %#v, want %#v; body=%s", got, want, body)
			}
		})
	}
}

func TestKimiExecutor_Issue7000DoesNotRewriteDelegatedToolChoice(t *testing.T) {
	const choice = `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`
	body, _ := captureClaudeIssue7000(t, NewKimiExecutor(&config.Config{}), &cliproxyauth.Auth{Provider: "kimi", Attributes: map[string]string{"api_key": "kimi-issue7000"}}, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: claudeIssue7000Payload("claude-opus-5-5", choice)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, "Execute")
	if got := gjson.GetBytes(body, "tool_choice").Value(); !reflect.DeepEqual(got, gjson.Parse(choice).Value()) {
		t.Fatalf("delegated provider tool_choice changed: %s", body)
	}
}

func TestClaudeExecutor_Issue7000OpenAITranslation(t *testing.T) {
	const schema = `{"type":"object","properties":{"issue7000_title":{"type":"string"}},"required":["issue7000_title"],"additionalProperties":false}`
	for _, source := range []struct {
		name, prefix, functionChoice, schemaField, method string
		format                                            sdktranslator.Format
	}{
		{name: "chat", format: sdktranslator.FormatOpenAI, prefix: `"messages":[{"role":"user","content":"Weather in Paris?"}],"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"lookup_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`, functionChoice: `{"type":"function","function":{"name":"lookup_weather"}}`, schemaField: `"response_format":{"type":"json_schema","json_schema":{"name":"weather","strict":true,"schema":` + schema + `}}`, method: "Execute"},
		{name: "responses", format: sdktranslator.FormatOpenAIResponse, prefix: `"input":[{"role":"user","content":[{"type":"input_text","text":"Weather in Paris?"}]}],"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"lookup_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]`, functionChoice: `{"type":"function","name":"lookup_weather"}`, schemaField: `"text":{"format":{"type":"json_schema","name":"weather","strict":true,"schema":` + schema + `}}`, method: "ExecuteStream"},
	} {
		for _, choice := range []string{`"required"`, source.functionChoice} {
			for _, withSchema := range []bool{false, true} {
				name := source.name + "/" + choice + "/plain"
				if withSchema {
					name = source.name + "/" + choice + "/schema"
				}
				t.Run(name, func(t *testing.T) {
					payload := `{"model":"claude-sonnet-5-5",` + source.prefix + `,"tool_choice":` + choice + `,"parallel_tool_calls":false`
					if withSchema {
						payload += `,` + source.schemaField
					}
					body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000"}}, cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: []byte(payload + `}`)}, cliproxyexecutor.Options{SourceFormat: source.format}, source.method)
					for path, want := range map[string]any{"tool_choice.type": "auto", "tool_choice.disable_parallel_tool_use": true, "tools.0.name": "lookup_weather", "tools.0.input_schema.required.0": "city", "thinking.type": "adaptive", "output_config.effort": "high"} {
						if got := gjson.GetBytes(body, path).Value(); !reflect.DeepEqual(got, want) {
							t.Errorf("%s = %#v, want %#v; body=%s", path, got, want, body)
						}
					}
					if gjson.GetBytes(body, "tool_choice.name").Exists() {
						t.Errorf("forced tool name survived: %s", body)
					}
					if withSchema && (!strings.Contains(gjson.GetBytes(body, "system").Raw, "issue7000_title") || !strings.Contains(gjson.GetBytes(body, "system").Raw, "JSON")) {
						t.Errorf("existing schema prompt fallback missing: %s", body)
					}
					if gjson.GetBytes(body, "output_config.format").Exists() {
						t.Errorf("translated schema should retain prompt fallback, not native output format: %s", body)
					}
				})
			}
		}
	}
}

func TestClaudeExecutor_Issue7000KeepsNativeOutputFormat(t *testing.T) {
	const format = `{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}`
	payload := claudeIssue7000Payload("claude-opus-5-5", `{"type":"any"}`)
	payload, _ = sjson.SetRawBytes(payload, "output_config", []byte(`{"effort":"high","format":`+format+`}`))
	payload, _ = sjson.SetRawBytes(payload, "thinking", []byte(`{"type":"adaptive"}`))
	body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000"}}, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, "Execute")
	if got := gjson.GetBytes(body, "output_config.format").Value(); !reflect.DeepEqual(got, gjson.Parse(format).Value()) {
		t.Errorf("native output format changed: %s", body)
	}
	if gjson.GetBytes(body, "tool_choice.type").String() != "auto" || gjson.GetBytes(body, "output_config.effort").String() != "high" {
		t.Errorf("native schema must not prevent tool-choice compatibility or remove effort: %s", body)
	}
}

func TestClaudeExecutor_Issue7000OAuthPreservesThinkingHistory(t *testing.T) {
	for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
		t.Run(method, func(t *testing.T) {
			payload := preservedThinkingReplayPayload("claude-opus-5-5")
			payload, _ = sjson.DeleteBytes(payload, "thinking")
			payload, _ = sjson.SetRawBytes(payload, "tools", []byte(claudeIssue7000Tools))
			payload, _ = sjson.SetRawBytes(payload, "tool_choice", []byte(`{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`))
			metadata := claudeOAuthTestMetadata()
			metadata["access_token"] = "sk-ant-oat01-issue7000"
			body, headers := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Provider: "claude", Metadata: metadata}, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, method)
			for path, want := range map[string]any{"tool_choice.type": "auto", "tool_choice.disable_parallel_tool_use": true, "thinking.type": "adaptive", "thinking.block_binding.prefix_mismatch_behavior": "drop_block"} {
				if got := gjson.GetBytes(body, path).Value(); !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v; body=%s", path, got, want, body)
				}
			}
			if gjson.GetBytes(body, "tool_choice.name").Exists() || !strings.Contains(string(body), preservedThinkingTestSignature) {
				t.Errorf("forced name survived or signed thinking was lost: %s", body)
			}
			if headers.Get("Authorization") != "Bearer sk-ant-oat01-issue7000" {
				t.Error("OAuth request did not use bearer authentication")
			}
		})
	}
}

func TestClaudeExecutor_Issue7000CountTokensUpstreamAndLocal(t *testing.T) {
	const model = "claude-sonnet-5-5"
	for _, choice := range []string{`{"type":"any","disable_parallel_tool_use":true}`, `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`} {
		t.Run(choice, func(t *testing.T) {
			payload := claudeIssue7000Payload("spoofed-body-model", choice)
			body, headers := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000"}}, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, "CountTokens")
			if got := gjson.GetBytes(body, "tool_choice").Value(); !reflect.DeepEqual(got, gjson.Parse(`{"type":"auto","disable_parallel_tool_use":true}`).Value()) {
				t.Errorf("upstream count choice = %#v, want auto with parallel flag", got)
			}
			if gjson.GetBytes(body, "model").String() != model || headers.Get("X-Api-Key") != "key-issue7000" {
				t.Error("upstream count did not preserve routed model/API-key authentication")
			}
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000", "base_url": "https://gateway.invalid"}}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("local count must not call upstream")
				return nil, io.ErrUnexpectedEOF
			})))
			count := func(input []byte) int64 {
				t.Helper()
				response, errCount := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: input}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
				if errCount != nil {
					t.Fatalf("local CountTokens: %v", errCount)
				}
				return gjson.GetBytes(response.Payload, "input_tokens").Int()
			}
			want := count(claudeIssue7000Payload(model, `{"type":"auto","disable_parallel_tool_use":true}`))
			if got := count(payload); got != want || got <= 0 {
				t.Errorf("local forced-choice count = %d, want auto count %d", got, want)
			}
		})
	}
}

func TestClaudeExecutor_Issue7000PayloadRulesRemainFinal(t *testing.T) {
	const model = "claude-opus-5-5"
	for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
		for _, filter := range []bool{false, true} {
			name := method + "/override"
			if filter {
				name = method + "/filter"
			}
			t.Run(name, func(t *testing.T) {
				// Conditions must see auto and surviving thinking, not the incoming forced choice.
				models := []config.PayloadModelRule{{Name: model, Exist: []string{"thinking"}, Match: []map[string]any{{"tool_choice.type": "auto"}}}}
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"tool_choice": map[string]any{"type": "tool", "name": "user_override"}, "observed_builtin": true}}}}}
				if filter {
					cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"tool_choice", "thinking", "output_config.effort", "tools"}}}}
				}
				payload := claudeIssue7000Payload(model, `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`)
				payload, _ = sjson.SetRawBytes(payload, "thinking", []byte(`{"type":"adaptive"}`))
				payload, _ = sjson.SetRawBytes(payload, "output_config", []byte(`{"effort":"high"}`))
				body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(cfg), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000"}}, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, method)
				if filter {
					for _, path := range []string{"tool_choice", "thinking", "output_config.effort", "tools"} {
						if gjson.GetBytes(body, path).Exists() {
							t.Errorf("filtered %s was restored: %s", path, body)
						}
					}
				} else if gjson.GetBytes(body, "tool_choice.type").String() != "tool" || gjson.GetBytes(body, "tool_choice.name").String() != "user_override" || !gjson.GetBytes(body, "observed_builtin").Bool() || gjson.GetBytes(body, "thinking.type").String() != "adaptive" {
					t.Errorf("payload override did not remain final with thinking intact: %s", body)
				}
			})
		}
	}
}

func TestClaudeExecutor_Issue7000LocalCountPayloadRulesRemainFinal(t *testing.T) {
	const model = "claude-opus-5-5"
	const forced = `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`
	for _, filter := range []bool{false, true} {
		name := "override"
		if filter {
			name = "filter"
		}
		t.Run(name, func(t *testing.T) {
			models := []config.PayloadModelRule{{Name: model, Match: []map[string]any{{"tool_choice.type": "auto", "model": model}}}}
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"tool_choice": map[string]any{"type": "tool", "name": "explicit_user_override_name_with_extra_tokens"}}}}}}
			controlChoice := `{"type":"tool","name":"explicit_user_override_name_with_extra_tokens"}`
			if filter {
				cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: models, Params: []string{"tool_choice"}}}}
				controlChoice = ""
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7000", "base_url": "https://gateway.invalid"}}
			count := func(executor *ClaudeExecutor, route string, payload []byte) int64 {
				t.Helper()
				resp, errCount := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: route, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
				if errCount != nil {
					t.Fatalf("local CountTokens: %v", errCount)
				}
				return gjson.GetBytes(resp.Payload, "input_tokens").Int()
			}
			// The legacy model supplies an independent baseline without 5.5 normalization.
			want := count(NewClaudeExecutor(&config.Config{}), "claude-opus-5", claudeIssue7000Payload("claude-opus-5", controlChoice))
			if got := count(NewClaudeExecutor(cfg), model, claudeIssue7000Payload("spoofed-body-model", forced)); got != want {
				t.Errorf("local configured count = %d, want %d after final %s", got, want, name)
			}
		})
	}
}

func TestClaudeExecutor_Issue7000ManagerRetryKeepsAuto(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			const model = "claude-sonnet-5-5"
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
			authID := uuid.NewString() + "-issue7000"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{ID: authID, Provider: "claude", Attributes: map[string]string{"api_key": "key-issue7000"}}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			var bodies [][]byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				if errClose := req.Body.Close(); errClose != nil {
					return nil, errClose
				}
				bodies = append(bodies, body)
				if len(bodies) == 1 {
					return nil, io.ErrUnexpectedEOF
				}
				contentType := "application/json"
				response := `{"id":"msg_retry","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
				if stream {
					contentType, response = "text/event-stream", strings.ReplaceAll(claudeIssue6698SSE, "claude-opus-5-5", model)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			payload := claudeIssue7000Payload(model, `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`)
			request := cliproxyexecutor.Request{Model: model, Payload: payload}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			if stream {
				result, errStream := manager.ExecuteStream(ctx, []string{"claude"}, request, options)
				if errStream != nil {
					t.Fatalf("manager ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("manager stream chunk: %v", chunk.Err)
					}
				}
			} else if _, errExecute := manager.Execute(ctx, []string{"claude"}, request, options); errExecute != nil {
				t.Fatalf("manager Execute: %v", errExecute)
			}
			if len(bodies) != 2 {
				t.Fatalf("upstream attempts = %d, want initial plus one retry", len(bodies))
			}
			for attempt, body := range bodies {
				if got := gjson.GetBytes(body, "tool_choice").Value(); !reflect.DeepEqual(got, gjson.Parse(`{"type":"auto","disable_parallel_tool_use":true}`).Value()) {
					t.Errorf("attempt %d restored forced choice: %s", attempt+1, body)
				}
			}
			if gjson.GetBytes(payload, "tool_choice.type").String() != "tool" {
				t.Error("executor mutated the caller payload reused by retries")
			}
		})
	}
}
